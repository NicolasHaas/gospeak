package server

import (
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
	"github.com/NicolasHaas/gospeak/pkg/rbac"
	"github.com/NicolasHaas/gospeak/pkg/store"
)

// ControlHandler handles TCP/TLS control plane connections.
type ControlHandler struct {
	server  *Server
	store   *store.Store
	mu      sync.RWMutex
	connMap map[uint32]net.Conn // sessionID -> TLS conn for sending events

	// Rate limiting for temp sub-channel creation: userID -> last creation time
	tempChanMu    sync.Mutex
	tempChanTimes map[int64]time.Time
}

// newControlHandler creates a control handler.
func newControlHandler(srv *Server, st *store.Store) *ControlHandler {
	return &ControlHandler{
		server:        srv,
		store:         st,
		connMap:       make(map[uint32]net.Conn),
		tempChanTimes: make(map[int64]time.Time),
	}
}

// setConn registers a TLS connection for a session (for sending events).
func (ch *ControlHandler) setConn(sessionID uint32, conn net.Conn) {
	ch.mu.Lock()
	ch.connMap[sessionID] = conn
	ch.mu.Unlock()
}

// removeConn removes a session's connection.
func (ch *ControlHandler) removeConn(sessionID uint32) {
	ch.mu.Lock()
	delete(ch.connMap, sessionID)
	ch.mu.Unlock()
}

// broadcastToChannel sends a control message to all sessions in a channel.
func (ch *ControlHandler) broadcastToChannel(channelID int64, msg *pb.ControlMessage, excludeSession uint32) {
	members := ch.server.channels.Members(channelID)
	ch.mu.RLock()
	defer ch.mu.RUnlock()
	for _, sid := range members {
		if sid == excludeSession {
			continue
		}
		if conn, ok := ch.connMap[sid]; ok {
			if err := protocol.WriteControlMessage(conn, msg); err != nil {
				slog.Error("broadcast write failed", "session", sid, "err", err)
			}
		}
	}
}

// StartControl starts the TCP/TLS control listener.
func (s *Server) StartControl(st *store.Store) error {
	cert, err := loadOrGenerateTLS(s.cfg)
	if err != nil {
		return fmt.Errorf("server: tls: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	ln, err := tls.Listen("tcp", s.cfg.ControlAddr, tlsCfg)
	if err != nil {
		return fmt.Errorf("server: listen control: %w", err)
	}
	s.controlConn = ln

	handler := newControlHandler(s, st)
	slog.Info("control plane listening", "addr", s.cfg.ControlAddr)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-s.ctx.Done():
					return
				default:
					slog.Error("accept error", "err", err)
					continue
				}
			}
			go s.handleControlConn(handler, conn, st)
		}
	}()

	return nil
}

// handleControlConn handles a single control connection lifecycle.
func (s *Server) handleControlConn(handler *ControlHandler, conn net.Conn, st *store.Store) {
	defer func() { _ = conn.Close() }()

	remoteAddr := conn.RemoteAddr().String()
	s.metrics.TotalConnections.Add(1)
	s.metrics.ActiveConnections.Add(1)
	slog.Debug("new control connection", "remote", remoteAddr)

	// First message must be AuthRequest
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	msg, err := protocol.ReadControlMessage(conn)
	if err != nil {
		slog.Error("auth read failed", "remote", remoteAddr, "err", err)
		return
	}
	_ = conn.SetReadDeadline(time.Time{}) // clear deadline

	if msg.AuthRequest == nil {
		sendError(conn, 1, "first message must be auth_request")
		return
	}

	// Validate token
	authReq := msg.AuthRequest

	// Validate username
	if !isValidUsername(authReq.Username) {
		sendError(conn, 2, "invalid username: must be 1-32 alphanumeric/underscore characters")
		return
	}

	var tokenRole model.Role
	var autoToken string // set when server generates a token for token-less join

	if authReq.Token == "" {
		// Token-less join
		if !s.cfg.AllowNoToken {
			s.metrics.FailedAuths.Add(1)
			sendError(conn, 2, "authentication failed: token required")
			return
		}
		tokenRole = model.RoleUser
	} else {
		tokenHash := crypto.HashToken(authReq.Token)
		var err error
		tokenRole, err = st.ValidateToken(tokenHash)
		if err != nil {
			s.metrics.FailedAuths.Add(1)
			sendError(conn, 2, "authentication failed: "+err.Error())
			return
		}
	}

	// Create or get user — existing users keep their stored role
	user, err := st.GetUserByUsername(authReq.Username)
	if err != nil {
		sendError(conn, 3, "internal error")
		return
	}

	var sessionRole model.Role
	if user == nil {
		// New user: role comes from the token
		user, err = st.CreateUser(authReq.Username, tokenRole)
		if err != nil {
			sendError(conn, 3, "failed to create user: "+err.Error())
			return
		}
		sessionRole = tokenRole

		// Auto-generate a personal token for identification (token-less join)
		if authReq.Token == "" {
			rawToken, err := crypto.GenerateToken()
			if err == nil {
				hash := crypto.HashToken(rawToken)
				_ = st.CreateToken(hash, model.RoleUser, 0, 0, 0, st.ZeroTime()) // unlimited, no expiry
				autoToken = rawToken
				slog.Debug("auto-generated token for token-less user", "user", user.Username)
			}
		}
	} else {
		// Existing user: use their stored/persisted role (honors SetUserRole changes)
		sessionRole = user.Role
	}

	// Check ban
	banned, err := st.IsUserBanned(user.ID)
	if err != nil {
		sendError(conn, 3, "internal error")
		return
	}
	if banned {
		sendError(conn, 4, "you are banned from this server")
		return
	}

	// Create session (voice key is shared server-wide for SFU model)
	session := s.sessions.Create(user.ID, user.Username, sessionRole)
	handler.setConn(session.ID, conn)
	defer func() {
		// Cleanup on disconnect
		chID := s.channels.Leave(session.ID)
		handler.removeConn(session.ID)
		s.sessions.Remove(session.ID)
		s.metrics.ActiveConnections.Add(-1)
		s.metrics.TotalDisconnects.Add(1)
		slog.Info("client disconnected", "user", user.Username, "session", session.ID)

		if chID > 0 {
			handler.broadcastToChannel(chID, &pb.ControlMessage{
				ChannelLeftEvent: &pb.ChannelLeftEvent{
					ChannelID: chID,
					UserID:    user.ID,
					Username:  user.Username,
				},
			}, session.ID)

			// Auto-delete temp channels when empty
			s.cleanupTempChannel(chID, st)
		}

		// Broadcast updated state to all remaining clients
		s.broadcastServerState(st, handler)
	}()

	// Build channel list
	channels, _ := st.ListChannels()
	channelInfos := s.buildChannelInfos(channels)

	// Send auth response
	authResp := &pb.ControlMessage{
		AuthResponse: &pb.AuthResponse{
			SessionID:     session.ID,
			Username:      user.Username,
			Role:          sessionRole.String(),
			EncryptionKey: s.voiceKey,
			Channels:      channelInfos,
			AutoToken:     autoToken,
		},
	}
	if err := protocol.WriteControlMessage(conn, authResp); err != nil {
		slog.Error("auth response write failed", "err", err)
		return
	}

	slog.Info("client authenticated", "user", user.Username, "role", sessionRole, "session", session.ID)
	s.metrics.SuccessfulAuths.Add(1)

	// Message loop
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		msg, err := protocol.ReadControlMessage(conn)
		if err != nil {
			if err == io.EOF || isClosedErr(err) {
				return
			}
			slog.Error("read error", "user", user.Username, "err", err)
			return
		}

		s.handleMessage(handler, session, msg, st, conn)
	}
}

// handleMessage dispatches a control message to the appropriate handler.
func (s *Server) handleMessage(handler *ControlHandler, session *model.Session, msg *pb.ControlMessage, st *store.Store, conn net.Conn) {
	switch {
	case msg.JoinChannelRequest != nil:
		s.handleJoinChannel(handler, session, msg.JoinChannelRequest, st, conn)

	case msg.LeaveChannelRequest != nil:
		s.handleLeaveChannel(handler, session, st, conn)

	case msg.ChannelListRequest != nil:
		s.handleChannelList(st, conn)

	case msg.UserStateUpdate != nil:
		s.handleUserState(handler, session, msg.UserStateUpdate, st)

	case msg.CreateChannelReq != nil:
		s.handleCreateChannel(session, msg.CreateChannelReq, st, conn, handler)

	case msg.DeleteChannelReq != nil:
		s.handleDeleteChannel(session, msg.DeleteChannelReq, st, conn, handler)

	case msg.CreateTokenReq != nil:
		s.handleCreateToken(session, msg.CreateTokenReq, st, conn)

	case msg.KickUserReq != nil:
		s.handleKickUser(handler, session, msg.KickUserReq, conn)

	case msg.BanUserReq != nil:
		s.handleBanUser(handler, session, msg.BanUserReq, st, conn)

	case msg.ChatMsg != nil:
		s.handleChatMessage(handler, session, msg.ChatMsg)

	case msg.SetUserRoleReq != nil:
		s.handleSetUserRole(handler, session, msg.SetUserRoleReq, st, conn)

	case msg.ExportDataReq != nil:
		s.handleExportData(session, msg.ExportDataReq, st, conn)

	case msg.ImportChannelsReq != nil:
		s.handleImportChannels(session, msg.ImportChannelsReq, st, conn, handler)

	case msg.Ping != nil:
		_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{
			Pong: &pb.Pong{Timestamp: msg.Ping.Timestamp},
		})
	}
}

func (s *Server) handleJoinChannel(handler *ControlHandler, session *model.Session, req *pb.JoinChannelRequest, st *store.Store, conn net.Conn) {
	// Verify channel exists
	ch, err := st.GetChannel(req.ChannelID)
	if err != nil || ch == nil {
		sendError(conn, 10, "channel not found")
		return
	}

	// Check max users
	if ch.MaxUsers > 0 && s.channels.MembersCount(ch.ID) >= ch.MaxUsers {
		sendError(conn, 11, "channel is full")
		return
	}

	prevCh := s.channels.Join(session.ID, ch.ID)
	session.ChannelID = ch.ID

	// Notify old channel
	if prevCh > 0 {
		handler.broadcastToChannel(prevCh, &pb.ControlMessage{
			ChannelLeftEvent: &pb.ChannelLeftEvent{
				ChannelID: prevCh,
				UserID:    session.UserID,
				Username:  session.Username,
			},
		}, session.ID)
	}

	// Notify new channel
	handler.broadcastToChannel(ch.ID, &pb.ControlMessage{
		ChannelJoinedEvent: &pb.ChannelJoinedEvent{
			ChannelID: ch.ID,
			User: pb.UserInfo{
				ID:       session.UserID,
				Username: session.Username,
				Role:     session.Role.String(),
				Muted:    session.Muted,
				Deafened: session.Deafened,
			},
		},
	}, session.ID)

	// Send full server state to the joining user
	s.sendServerState(st, conn)

	// Broadcast updated state to ALL clients so everyone sees the new member
	s.broadcastServerState(st, handler)
}

func (s *Server) handleLeaveChannel(handler *ControlHandler, session *model.Session, st *store.Store, conn net.Conn) {
	chID := s.channels.Leave(session.ID)
	session.ChannelID = 0

	if chID > 0 {
		handler.broadcastToChannel(chID, &pb.ControlMessage{
			ChannelLeftEvent: &pb.ChannelLeftEvent{
				ChannelID: chID,
				UserID:    session.UserID,
				Username:  session.Username,
			},
		}, session.ID)

		// Auto-delete temp channels when empty
		s.cleanupTempChannel(chID, st)
	}

	// Broadcast updated state to ALL clients
	s.broadcastServerState(st, handler)
}

func (s *Server) handleChannelList(st *store.Store, conn net.Conn) {
	channels, _ := st.ListChannels()
	infos := s.buildChannelInfos(channels)
	_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{
		ChannelListResponse: &pb.ChannelListResponse{Channels: infos},
	})
}

func (s *Server) handleUserState(handler *ControlHandler, session *model.Session, upd *pb.UserStateUpdate, st *store.Store) {
	session.Muted = upd.Muted
	session.Deafened = upd.Deafened

	// Broadcast updated server state to all clients
	s.broadcastServerState(st, handler)
}

func (s *Server) handleCreateChannel(session *model.Session, req *pb.CreateChannelRequest, st *store.Store, conn net.Conn, handler *ControlHandler) {
	// Validate and sanitize channel name
	name := sanitizeText(strings.TrimSpace(req.Name))
	if len(name) == 0 || len(name) > 64 {
		sendError(conn, 31, "channel name must be 1-64 characters")
		return
	}

	if req.ParentID > 0 && req.IsTemp {
		// Temp sub-channel creation: any user can create if parent AllowSubChannels
		parent, err := st.GetChannel(req.ParentID)
		if err != nil || parent == nil {
			sendError(conn, 31, "parent channel not found")
			return
		}
		if !parent.AllowSubChannels {
			sendError(conn, 31, "parent channel does not allow sub-channels")
			return
		}
		// Rate limit: 1 temp channel per user per 10 seconds
		handler.tempChanMu.Lock()
		last, ok := handler.tempChanTimes[session.UserID]
		if ok && time.Since(last) < 10*time.Second {
			handler.tempChanMu.Unlock()
			sendError(conn, 31, "please wait before creating another sub-channel")
			return
		}
		handler.tempChanTimes[session.UserID] = time.Now()
		handler.tempChanMu.Unlock()
	} else {
		// Permanent channel: require PermCreateChannel (admin/mod)
		if errMsg := rbac.RequirePermission(session.Role, model.PermCreateChannel); errMsg != "" {
			sendError(conn, 30, errMsg)
			return
		}
	}

	desc := sanitizeText(strings.TrimSpace(req.Description))
	if len(desc) > 256 {
		desc = desc[:256]
	}

	ch, err := st.CreateChannelFull(name, desc, int(req.MaxUsers), req.ParentID, req.IsTemp, req.AllowSubChannels)
	if err != nil {
		sendError(conn, 31, "failed to create channel: "+err.Error())
		return
	}

	slog.Info("channel created", "name", ch.Name, "parent", ch.ParentID, "temp", ch.IsTemp, "by", session.Username)
	s.metrics.ChannelsCreated.Add(1)

	// Broadcast updated state to all connected clients
	s.broadcastServerState(st, handler)
}

func (s *Server) handleDeleteChannel(session *model.Session, req *pb.DeleteChannelRequest, st *store.Store, conn net.Conn, handler *ControlHandler) {
	if errMsg := rbac.RequirePermission(session.Role, model.PermDeleteChannel); errMsg != "" {
		sendError(conn, 30, errMsg)
		return
	}

	if err := st.DeleteChannel(req.ChannelID); err != nil {
		sendError(conn, 31, "failed to delete channel: "+err.Error())
		return
	}

	// Move users out of deleted channel
	members := s.channels.Members(req.ChannelID)
	for _, sid := range members {
		s.channels.Leave(sid)
		if sess := s.sessions.Get(sid); sess != nil {
			sess.ChannelID = 0
		}
	}

	slog.Info("channel deleted", "id", req.ChannelID, "by", session.Username)
	s.metrics.ChannelsDeleted.Add(1)
	s.broadcastServerState(st, handler)
}

func (s *Server) handleCreateToken(session *model.Session, req *pb.CreateTokenRequest, st *store.Store, conn net.Conn) {
	if errMsg := rbac.RequirePermission(session.Role, model.PermManageTokens); errMsg != "" {
		sendError(conn, 30, errMsg)
		return
	}

	rawToken, err := crypto.GenerateToken()
	if err != nil {
		sendError(conn, 31, "failed to generate token")
		return
	}

	var expiresAt time.Time
	if req.ExpiresInSeconds > 0 {
		expiresAt = time.Now().Add(time.Duration(req.ExpiresInSeconds) * time.Second)
	}

	hash := crypto.HashToken(rawToken)
	role := model.ParseRole(req.Role)

	if err := st.CreateToken(hash, role, req.ChannelScope, session.UserID, int(req.MaxUses), expiresAt); err != nil {
		sendError(conn, 31, "failed to store token: "+err.Error())
		return
	}

	slog.Info("token created", "role", role, "by", session.Username)
	s.metrics.TokensCreated.Add(1)

	_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{
		CreateTokenResp: &pb.CreateTokenResponse{Token: rawToken},
	})
}

func (s *Server) handleKickUser(handler *ControlHandler, session *model.Session, req *pb.KickUserRequest, conn net.Conn) {
	if errMsg := rbac.RequirePermission(session.Role, model.PermKickUser); errMsg != "" {
		sendError(conn, 30, errMsg)
		return
	}

	reason := sanitizeText(strings.TrimSpace(req.Reason))
	if len(reason) > 256 {
		reason = reason[:256]
	}

	target := s.sessions.GetByUserID(req.UserID)
	if target == nil {
		sendError(conn, 32, "user not online")
		return
	}

	// Close their connection (will trigger cleanup in handleControlConn)
	handler.mu.RLock()
	targetConn, ok := handler.connMap[target.ID]
	handler.mu.RUnlock()
	if ok {
		sendError(targetConn, 99, "you have been kicked: "+reason)
		_ = targetConn.Close()
	}

	slog.Info("user kicked", "target", target.Username, "by", session.Username, "reason", reason)
	s.metrics.KickCount.Add(1)
}

func (s *Server) handleBanUser(handler *ControlHandler, session *model.Session, req *pb.BanUserRequest, st *store.Store, conn net.Conn) {
	if errMsg := rbac.RequirePermission(session.Role, model.PermBanUser); errMsg != "" {
		sendError(conn, 30, errMsg)
		return
	}

	reason := sanitizeText(strings.TrimSpace(req.Reason))
	if len(reason) > 256 {
		reason = reason[:256]
	}

	var expiresAt time.Time
	if req.DurationSeconds > 0 {
		expiresAt = time.Now().Add(time.Duration(req.DurationSeconds) * time.Second)
	}

	if err := st.CreateBan(req.UserID, "", reason, session.UserID, expiresAt); err != nil {
		sendError(conn, 31, "failed to create ban")
		return
	}

	// Also kick them if online
	target := s.sessions.GetByUserID(req.UserID)
	if target != nil {
		handler.mu.RLock()
		targetConn, ok := handler.connMap[target.ID]
		handler.mu.RUnlock()
		if ok {
			sendError(targetConn, 99, "you have been banned: "+reason)
			_ = targetConn.Close()
		}
	}

	slog.Info("user banned", "user_id", req.UserID, "by", session.Username)
	s.metrics.BanCount.Add(1)
}

// channelUsers returns UserInfo for all sessions in a channel.
func (s *Server) channelUsers(channelID int64) []pb.UserInfo {
	members := s.channels.Members(channelID)
	users := make([]pb.UserInfo, 0, len(members))
	for _, sid := range members {
		sess := s.sessions.Get(sid)
		if sess != nil {
			users = append(users, pb.UserInfo{
				ID:       sess.UserID,
				Username: sess.Username,
				Role:     sess.Role.String(),
				Muted:    sess.Muted,
				Deafened: sess.Deafened,
			})
		}
	}
	return users
}

func (s *Server) handleChatMessage(handler *ControlHandler, session *model.Session, chat *pb.ChatMessage) {
	chID := s.channels.ChannelOf(session.ID)
	if chID == 0 {
		return // not in a channel
	}

	// Validate and sanitize message
	text := sanitizeText(strings.TrimSpace(chat.Text))
	if len(text) == 0 || len(text) > 2000 {
		return // empty or too long, silently drop
	}

	event := &pb.ControlMessage{
		ChatEvent: &pb.ChatMessage{
			ChannelID:  chID,
			SenderID:   session.UserID,
			SenderName: session.Username,
			Text:       text,
			Timestamp:  time.Now().Unix(),
		},
	}

	// Broadcast to all channel members including sender (for confirmation)
	handler.broadcastToChannel(chID, event, 0)
	s.metrics.ChatMessagesSent.Add(1)
}

func (s *Server) handleSetUserRole(handler *ControlHandler, session *model.Session, req *pb.SetUserRoleRequest, st *store.Store, conn net.Conn) {
	if errMsg := rbac.RequirePermission(session.Role, model.PermManageRoles); errMsg != "" {
		sendError(conn, 30, errMsg)
		return
	}

	// Prevent self-role change
	if req.TargetUserID == session.UserID {
		sendError(conn, 31, "cannot change your own role")
		return
	}

	newRole := model.ParseRole(req.NewRole)

	// Prevent escalation: cannot grant a role higher than your own
	if newRole > session.Role {
		sendError(conn, 31, "cannot grant a role higher than your own")
		return
	}
	if err := st.UpdateUserRole(req.TargetUserID, newRole); err != nil {
		sendError(conn, 31, "failed to update role: "+err.Error())
		return
	}

	// Update the session if the target user is online
	target := s.sessions.GetByUserID(req.TargetUserID)
	if target != nil {
		target.Role = newRole
	}

	slog.Info("user role changed", "target_user", req.TargetUserID, "new_role", newRole, "by", session.Username)

	_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{
		SetUserRoleResp: &pb.SetUserRoleResponse{Success: true, Message: "role updated"},
	})

	// Broadcast updated state to all clients
	s.broadcastServerState(st, handler)
}

// sendServerState sends the full server state to a single connection.
func (s *Server) sendServerState(st *store.Store, conn net.Conn) {
	channels, _ := st.ListChannels()
	infos := s.buildChannelInfos(channels)
	_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{
		ServerStateEvent: &pb.ServerStateEvent{Channels: infos},
	})
}

// broadcastServerState sends updated server state to ALL connected sessions.
func (s *Server) broadcastServerState(st *store.Store, handler *ControlHandler) {
	channels, _ := st.ListChannels()
	infos := s.buildChannelInfos(channels)
	msg := &pb.ControlMessage{
		ServerStateEvent: &pb.ServerStateEvent{Channels: infos},
	}
	handler.mu.RLock()
	for _, conn := range handler.connMap {
		_ = protocol.WriteControlMessage(conn, msg)
	}
	handler.mu.RUnlock()
}

// buildChannelInfos converts model channels to protocol channel infos.
func (s *Server) buildChannelInfos(channels []model.Channel) []pb.ChannelInfo {
	infos := make([]pb.ChannelInfo, len(channels))
	for i, ch := range channels {
		infos[i] = pb.ChannelInfo{
			ID:               ch.ID,
			Name:             ch.Name,
			Description:      ch.Description,
			MaxUsers:         int32(ch.MaxUsers), //nolint:gosec // MaxUsers is bounded by UI/config; overflow impossible in practice
			ParentID:         ch.ParentID,
			IsTemp:           ch.IsTemp,
			AllowSubChannels: ch.AllowSubChannels,
			Users:            s.channelUsers(ch.ID),
		}
	}
	return infos
}

// cleanupTempChannel schedules a temp channel for deletion after a 5-minute grace period.
// If someone rejoins within that window the deletion is cancelled.
func (s *Server) cleanupTempChannel(channelID int64, st *store.Store) {
	ch, err := st.GetChannel(channelID)
	if err != nil || ch == nil || !ch.IsTemp {
		return
	}
	if s.channels.MembersCount(channelID) > 0 {
		return
	}

	go func() {
		time.Sleep(5 * time.Minute)
		// Re-check after grace period
		if s.channels.MembersCount(channelID) > 0 {
			return
		}
		if err := st.DeleteChannel(channelID); err != nil {
			slog.Error("failed to delete empty temp channel", "id", channelID, "err", err)
			return
		}
		slog.Debug("auto-deleted empty temp channel after 5m", "name", ch.Name, "id", channelID)
	}()
}

func sendError(conn net.Conn, code int32, message string) {
	_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{
		ErrorResponse: &pb.ErrorResponse{Code: code, Message: message},
	})
}

func isClosedErr(err error) bool {
	if err == nil {
		return false
	}
	return err.Error() == "use of closed network connection" ||
		err.Error() == "tls: use of closed connection"
}

// isValidUsername checks that a username is 1-32 alphanumeric/underscore/hyphen characters.
func isValidUsername(name string) bool {
	if len(name) == 0 || len(name) > 32 {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// sanitizeText strips control characters (except newline) from user-supplied text
// to prevent UI spoofing, terminal escape injection, and null-byte attacks.
func sanitizeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' ' // collapse newlines to spaces
		}
		if unicode.IsControl(r) {
			return -1 // strip all other control chars (null, bell, ANSI escapes, etc.)
		}
		return r
	}, s)
}

func (s *Server) handleExportData(session *model.Session, req *pb.ExportDataRequest, st *store.Store, conn net.Conn) {
	if errMsg := rbac.RequirePermission(session.Role, model.PermCreateChannel); errMsg != "" {
		sendError(conn, 30, "admin only: "+errMsg)
		return
	}

	var data []byte
	var err error
	switch req.Type {
	case "channels":
		data, err = ExportChannelsYAML(st)
	case "users":
		data, err = ExportUsersYAML(st)
	default:
		sendError(conn, 31, "unknown export type: "+req.Type)
		return
	}

	if err != nil {
		sendError(conn, 31, "export failed: "+err.Error())
		return
	}

	_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{
		ExportDataResp: &pb.ExportDataResponse{
			Type: req.Type,
			Data: string(data),
		},
	})
}

func (s *Server) handleImportChannels(session *model.Session, req *pb.ImportChannelsRequest, st *store.Store, conn net.Conn, handler *ControlHandler) {
	if errMsg := rbac.RequirePermission(session.Role, model.PermCreateChannel); errMsg != "" {
		sendError(conn, 30, "admin only: "+errMsg)
		return
	}

	if err := ImportChannelsFromYAML([]byte(req.YAML), st); err != nil {
		_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{
			ImportChannelsResp: &pb.ImportChannelsResponse{
				Success: false,
				Message: "import failed: " + err.Error(),
			},
		})
		return
	}

	slog.Info("channels imported via UI", "by", session.Username)

	_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{
		ImportChannelsResp: &pb.ImportChannelsResponse{
			Success: true,
			Message: "channels imported successfully",
		},
	})

	// Broadcast updated state
	s.broadcastServerState(st, handler)
}
