package server

import (
	"context"
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
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
	"github.com/NicolasHaas/gospeak/pkg/rbac"
)

// ControlHandler handles TCP/TLS control plane connections.
type ControlHandler struct {
	server  *Server
	store   datastore.DataProviderFactory
	mu      sync.RWMutex
	connMap map[uint32]net.Conn // sessionID -> TLS conn for sending events

	// Rate limiting for temp sub-channel creation: userID -> last creation time
	tempChanMu    sync.Mutex
	tempChanTimes map[int64]time.Time
}

// newControlHandler creates a control handler.
func newControlHandler(srv *Server, st datastore.DataProviderFactory) *ControlHandler {
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
func (s *Server) StartControl(st datastore.DataProviderFactory) error {
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
func (s *Server) handleControlConn(handler *ControlHandler, conn net.Conn, st datastore.DataProviderFactory) {
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

		ctx := context.Background()
		tx, err := st.Tx(ctx)
		if err != nil {
			sendError(conn, 3, "could not establish transaction: "+err.Error())
		}
		defer func() {
			if err := tx.Rollback(); err != nil {
				sendError(conn, 3, "could not rollback transaction: "+err.Error())
			}
		}()

		tokenRole, err = tx.ValidateToken(tokenHash)
		if err != nil {
			s.metrics.FailedAuths.Add(1)
			sendError(conn, 2, "authentication failed: "+err.Error())
			return
		}
		if err := tx.Commit(); err != nil {
			sendError(conn, 3, "error committing transaction: "+err.Error())
		}
	}

	// Create or get user — existing users keep their stored role
	user, err := st.NonTx().GetUserByUsername(authReq.Username)
	if err != nil {
		sendError(conn, 3, "internal error")
		return
	}

	var sessionRole model.Role
	if user == nil {
		// New user: role comes from the token
		user, err = st.NonTx().CreateUser(authReq.Username, tokenRole)
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
				_ = st.NonTx().CreateToken(hash, model.RoleUser, 0, 0, 0, st.NonTx().ZeroTime()) // unlimited, no expiry
				autoToken = rawToken
				slog.Debug("auto-generated token for token-less user", "user", user.Username)
			}
		}
	} else {
		// Existing user: use their stored/persisted role (honors SetUserRole changes)
		sessionRole = user.Role
	}

	// Check ban
	banned, err := st.NonTx().IsUserBanned(user.ID)
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
	sessionID := session.ID

	handler.setConn(sessionID, conn)
	defer func() {
		// Cleanup on disconnect
		chID := s.channels.Leave(sessionID)
		handler.removeConn(sessionID)
		s.sessions.Remove(sessionID)
		s.metrics.ActiveConnections.Add(-1)
		s.metrics.TotalDisconnects.Add(1)
		slog.Info("client disconnected", "user", user.Username, "session", sessionID)

		if chID > 0 {
			handler.broadcastToChannel(chID, &pb.ControlMessage{
				ChannelLeftEvent: &pb.ChannelLeftEvent{
					ChannelID: chID,
					UserID:    user.ID,
					Username:  user.Username,
				},
			}, sessionID)

			// Auto-delete temp channels when empty
			s.cleanupTempChannel(chID, st)
		}

		// Broadcast updated state to all remaining clients
		s.broadcastServerState(st, handler)
	}()

	// Build channel list
	channels, _ := st.NonTx().ListChannels()
	channelInfos := s.buildChannelInfos(channels)

	// Send auth response
	authResp := &pb.ControlMessage{
		AuthResponse: &pb.AuthResponse{
			SessionID:     sessionID,
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

	slog.Info("client authenticated", "user", user.Username, "role", sessionRole, "session", sessionID)
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

		s.handleMessage(handler, sessionID, msg, st, conn)
	}
}

// handleMessage dispatches a control message to the appropriate handler.
func (s *Server) handleMessage(handler *ControlHandler, sessionID uint32, msg *pb.ControlMessage, st datastore.DataProviderFactory, conn net.Conn) {
	switch {
	case msg.JoinChannelRequest != nil:
		s.handleJoinChannel(handler, sessionID, msg.JoinChannelRequest, st, conn)

	case msg.LeaveChannelRequest != nil:
		s.handleLeaveChannel(handler, sessionID, st, conn)

	case msg.ChannelListRequest != nil:
		s.handleChannelList(st, conn)

	case msg.UserStateUpdate != nil:
		s.handleUserState(handler, sessionID, msg.UserStateUpdate, st)

	case msg.CreateChannelReq != nil:
		s.handleCreateChannel(sessionID, msg.CreateChannelReq, st, conn, handler)

	case msg.DeleteChannelReq != nil:
		s.handleDeleteChannel(sessionID, msg.DeleteChannelReq, st, conn, handler)

	case msg.CreateTokenReq != nil:
		s.handleCreateToken(sessionID, msg.CreateTokenReq, st, conn)

	case msg.KickUserReq != nil:
		s.handleKickUser(handler, sessionID, msg.KickUserReq, conn)

	case msg.BanUserReq != nil:
		s.handleBanUser(handler, sessionID, msg.BanUserReq, st, conn)

	case msg.ChatMsg != nil:
		s.handleChatMessage(handler, sessionID, msg.ChatMsg)

	case msg.SetUserRoleReq != nil:
		s.handleSetUserRole(handler, sessionID, msg.SetUserRoleReq, st, conn)

	case msg.ExportDataReq != nil:
		s.handleExportData(sessionID, msg.ExportDataReq, st, conn)

	case msg.ImportChannelsReq != nil:
		s.handleImportChannels(sessionID, msg.ImportChannelsReq, st, conn, handler)

	case msg.Ping != nil:
		_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{
			Pong: &pb.Pong{Timestamp: msg.Ping.Timestamp},
		})
	}
}

func (s *Server) handleJoinChannel(handler *ControlHandler, sessionID uint32, req *pb.JoinChannelRequest, st datastore.DataProviderFactory, conn net.Conn) {
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		sendError(conn, 3, "session not found")
		return
	}
	// Verify channel exists
	ch, err := st.NonTx().GetChannel(req.ChannelID)
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
	s.sessions.SetChannel(session.ID, ch.ID)

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

func (s *Server) handleLeaveChannel(handler *ControlHandler, sessionID uint32, st datastore.DataProviderFactory, conn net.Conn) {
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		sendError(conn, 3, "session not found")
		return
	}
	chID := s.channels.Leave(session.ID)
	s.sessions.SetChannel(session.ID, 0)

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

func (s *Server) handleChannelList(st datastore.DataProviderFactory, conn net.Conn) {
	channels, _ := st.NonTx().ListChannels()
	infos := s.buildChannelInfos(channels)
	_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{
		ChannelListResponse: &pb.ChannelListResponse{Channels: infos},
	})
}

func (s *Server) handleUserState(handler *ControlHandler, sessionID uint32, upd *pb.UserStateUpdate, st datastore.DataProviderFactory) {
	s.sessions.UpdateUserState(sessionID, upd.Muted, upd.Deafened)

	// Broadcast updated server state to all clients
	s.broadcastServerState(st, handler)
}

func (s *Server) handleCreateChannel(sessionID uint32, req *pb.CreateChannelRequest, st datastore.DataProviderFactory, conn net.Conn, handler *ControlHandler) {
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		sendError(conn, 3, "session not found")
		return
	}
	// Validate and sanitize channel name
	name := sanitizeText(strings.TrimSpace(req.Name))
	if len(name) == 0 || len(name) > 64 {
		sendError(conn, 31, "channel name must be 1-64 characters")
		return
	}

	if req.ParentID > 0 && req.IsTemp {
		// Temp sub-channel creation: any user can create if parent AllowSubChannels
		parent, err := st.NonTx().GetChannel(req.ParentID)
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
		if errMsg := rbac.RequirePermission(session.Role, rbac.PermCreateChannel); errMsg != "" {
			sendError(conn, 30, errMsg)
			return
		}
	}

	desc := sanitizeText(strings.TrimSpace(req.Description))
	if len(desc) > 256 {
		desc = desc[:256]
	}

	ch := &model.Channel{
		Name:             name,
		Description:      desc,
		MaxUsers:         int(req.MaxUsers),
		ParentID:         req.ParentID,
		IsTemp:           req.IsTemp,
		AllowSubChannels: req.AllowSubChannels,
	}
	if err := st.NonTx().CreateChannel(ch); err != nil {
		sendError(conn, 31, "failed to create channel: "+err.Error())
		return
	}

	slog.Info("channel created", "name", ch.Name, "parent", ch.ParentID, "temp", ch.IsTemp, "by", session.Username)
	s.metrics.ChannelsCreated.Add(1)

	// Broadcast updated state to all connected clients
	s.broadcastServerState(st, handler)
}

func (s *Server) handleDeleteChannel(sessionID uint32, req *pb.DeleteChannelRequest, st datastore.DataProviderFactory, conn net.Conn, handler *ControlHandler) {
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		sendError(conn, 3, "session not found")
		return
	}
	if errMsg := rbac.RequirePermission(session.Role, rbac.PermDeleteChannel); errMsg != "" {
		sendError(conn, 30, errMsg)
		return
	}

	if err := st.NonTx().DeleteChannel(req.ChannelID); err != nil {
		sendError(conn, 31, "failed to delete channel: "+err.Error())
		return
	}

	// Move users out of deleted channel
	members := s.channels.Members(req.ChannelID)
	for _, sid := range members {
		s.channels.Leave(sid)
		s.sessions.SetChannel(sid, 0)
	}

	slog.Info("channel deleted", "id", req.ChannelID, "by", session.Username)
	s.metrics.ChannelsDeleted.Add(1)
	s.broadcastServerState(st, handler)
}

func (s *Server) handleCreateToken(sessionID uint32, req *pb.CreateTokenRequest, st datastore.DataProviderFactory, conn net.Conn) {
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		sendError(conn, 3, "session not found")
		return
	}
	if errMsg := rbac.RequirePermission(session.Role, rbac.PermManageTokens); errMsg != "" {
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

	if err := st.NonTx().CreateToken(hash, role, req.ChannelScope, session.UserID, int(req.MaxUses), expiresAt); err != nil {
		sendError(conn, 31, "failed to store token: "+err.Error())
		return
	}

	slog.Info("token created", "role", role, "by", session.Username)
	s.metrics.TokensCreated.Add(1)

	_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{
		CreateTokenResp: &pb.CreateTokenResponse{Token: rawToken},
	})
}

func (s *Server) handleKickUser(handler *ControlHandler, sessionID uint32, req *pb.KickUserRequest, conn net.Conn) {
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		sendError(conn, 3, "session not found")
		return
	}
	if errMsg := rbac.RequirePermission(session.Role, rbac.PermKickUser); errMsg != "" {
		sendError(conn, 30, errMsg)
		return
	}

	reason := sanitizeText(strings.TrimSpace(req.Reason))
	if len(reason) > 256 {
		reason = reason[:256]
	}

	target, ok := s.sessions.GetByUserIDSnapshot(req.UserID)
	if !ok {
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

func (s *Server) handleBanUser(handler *ControlHandler, sessionID uint32, req *pb.BanUserRequest, st datastore.DataProviderFactory, conn net.Conn) {
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		sendError(conn, 3, "session not found")
		return
	}
	if errMsg := rbac.RequirePermission(session.Role, rbac.PermBanUser); errMsg != "" {
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

	if err := st.NonTx().CreateBan(req.UserID, "", reason, session.UserID, expiresAt); err != nil {
		sendError(conn, 31, "failed to create ban")
		return
	}

	// Also kick them if online
	if target, ok := s.sessions.GetByUserIDSnapshot(req.UserID); ok {
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
		sess, ok := s.sessions.GetSnapshot(sid)
		if ok {
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

func (s *Server) handleChatMessage(handler *ControlHandler, sessionID uint32, chat *pb.ChatMessage) {
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		return
	}
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

func (s *Server) handleSetUserRole(handler *ControlHandler, sessionID uint32, req *pb.SetUserRoleRequest, st datastore.DataProviderFactory, conn net.Conn) {
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		sendError(conn, 3, "session not found")
		return
	}
	if errMsg := rbac.RequirePermission(session.Role, rbac.PermManageRoles); errMsg != "" {
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
	if err := st.NonTx().UpdateUserRole(req.TargetUserID, newRole); err != nil {
		sendError(conn, 31, "failed to update role: "+err.Error())
		return
	}

	// Update the session if the target user is online
	if target, ok := s.sessions.GetByUserIDSnapshot(req.TargetUserID); ok {
		s.sessions.UpdateRole(target.ID, newRole)
	}

	slog.Info("user role changed", "target_user", req.TargetUserID, "new_role", newRole, "by", session.Username)

	_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{
		SetUserRoleResp: &pb.SetUserRoleResponse{Success: true, Message: "role updated"},
	})

	// Broadcast updated state to all clients
	s.broadcastServerState(st, handler)
}

// sendServerState sends the full server state to a single connection.
func (s *Server) sendServerState(st datastore.DataProviderFactory, conn net.Conn) {
	channels, _ := st.NonTx().ListChannels()
	infos := s.buildChannelInfos(channels)
	_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{
		ServerStateEvent: &pb.ServerStateEvent{Channels: infos},
	})
}

// broadcastServerState sends updated server state to ALL connected sessions.
func (s *Server) broadcastServerState(st datastore.DataProviderFactory, handler *ControlHandler) {
	channels, _ := st.NonTx().ListChannels()
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
func (s *Server) cleanupTempChannel(channelID int64, st datastore.DataProviderFactory) {
	ch, err := st.NonTx().GetChannel(channelID)
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
		if err := st.NonTx().DeleteChannel(channelID); err != nil {
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
	return model.ValidateUsername(name) == nil
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

func (s *Server) handleExportData(sessionID uint32, req *pb.ExportDataRequest, st datastore.DataProviderFactory, conn net.Conn) {
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		sendError(conn, 3, "session not found")
		return
	}
	if errMsg := rbac.RequirePermission(session.Role, rbac.PermCreateChannel); errMsg != "" {
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

func (s *Server) handleImportChannels(sessionID uint32, req *pb.ImportChannelsRequest, st datastore.DataProviderFactory, conn net.Conn, handler *ControlHandler) {
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok {
		sendError(conn, 3, "session not found")
		return
	}
	if errMsg := rbac.RequirePermission(session.Role, rbac.PermCreateChannel); errMsg != "" {
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
