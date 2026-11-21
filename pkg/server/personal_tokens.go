package server

import (
	"context"
	"net"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func isPersonalTokenRequest(m *pb.ControlMessage) bool {
	return m.PersonalTokenStatusReq != nil || m.RotatePersonalTokenReq != nil || m.RevokePersonalTokenReq != nil || m.IssuePersonalRecoveryReq != nil || m.RevokePersonalRecoveryReq != nil
}

// Each request has a completion even when denied or capacity-limited. No
// credential is echoed except the explicitly requested temporary recovery grant.
func personalTokenResponse(m *pb.ControlMessage, message string) *pb.ControlMessage {
	switch {
	case m.PersonalTokenStatusReq != nil:
		return &pb.ControlMessage{PersonalTokenStatusResp: &pb.PersonalTokenStatusResponse{Error: message}}
	case m.RotatePersonalTokenReq != nil:
		return &pb.ControlMessage{RotatePersonalTokenResp: &pb.RotatePersonalTokenResponse{Error: message}}
	case m.RevokePersonalTokenReq != nil:
		return &pb.ControlMessage{RevokePersonalTokenResp: &pb.RevokePersonalTokenResponse{Error: message}}
	case m.IssuePersonalRecoveryReq != nil:
		return &pb.ControlMessage{IssuePersonalRecoveryResp: &pb.IssuePersonalRecoveryResponse{Error: message}}
	default:
		return &pb.ControlMessage{RevokePersonalRecoveryResp: &pb.RevokePersonalRecoveryResponse{Error: message}}
	}
}

func (s *Server) handlePersonalToken(handler *ControlHandler, sessionID uint32, m *pb.ControlMessage, st datastore.DataProviderFactory, conn net.Conn) {
	// Work drain precedes policy and DB locks. Regular handlers and actual media
	// writes take its read side; none can retain a stale snapshot through retirement.
	s.credentialWorkMu.Lock()
	s.remoteModerationMu.Lock()
	response, retired := s.mutatePersonalToken(handler, sessionID, m, st, conn)
	s.remoteModerationMu.Unlock()
	s.credentialWorkMu.Unlock()
	s.finishRetiredSessions(handler, st, retired, conn, response)
}

func (s *Server) mutatePersonalToken(handler *ControlHandler, sessionID uint32, m *pb.ControlMessage, st datastore.DataProviderFactory, conn net.Conn) (*pb.ControlMessage, []retiredControlSession) {
	fail := func(message string) (*pb.ControlMessage, []retiredControlSession) {
		return personalTokenResponse(m, message), nil
	}
	session, live := s.sessions.GetSnapshot(sessionID)
	client, queued := conn.(*controlClient)
	if !live || !queued || client.sessionID != sessionID {
		return fail("permission denied")
	}
	handler.mu.RLock()
	current := handler.connMap[sessionID] == client
	handler.mu.RUnlock()
	if !current {
		return fail("permission denied")
	}
	tx, err := st.Tx(context.Background())
	if err != nil {
		return fail("could not change credential")
	}
	defer tx.Rollback()
	actor, err := tx.GetUserByID(session.UserID)
	if err != nil || actor == nil || actor.PersonalTokenHash == "" || actor.PersonalTokenHash != client.credentialHash {
		return fail("permission denied")
	}
	banned, err := tx.IsUserBanned(actor.ID)
	if err != nil || banned {
		return fail("permission denied")
	}
	actorOwner, err := tx.IsBootstrapUser(actor.ID)
	if err != nil {
		return fail("could not change credential")
	}
	if ip, err := canonicalControlPeerIP(conn.RemoteAddr()); err == nil && ip != "" {
		banned, err := tx.IsIPBanned(ip)
		if err != nil || (banned && !actorOwner) {
			return fail("permission denied")
		}
	}
	targetID, version := actor.ID, int64(-1)
	switch {
	case m.PersonalTokenStatusReq != nil:
		if m.PersonalTokenStatusReq.TargetID != 0 {
			targetID = m.PersonalTokenStatusReq.TargetID
		}
	case m.RotatePersonalTokenReq != nil:
		version = m.RotatePersonalTokenReq.Version
	case m.RevokePersonalTokenReq != nil:
		targetID, version = m.RevokePersonalTokenReq.TargetID, m.RevokePersonalTokenReq.Version
	case m.IssuePersonalRecoveryReq != nil:
		targetID, version = m.IssuePersonalRecoveryReq.TargetID, m.IssuePersonalRecoveryReq.Version
	case m.RevokePersonalRecoveryReq != nil:
		targetID, version = m.RevokePersonalRecoveryReq.TargetID, m.RevokePersonalRecoveryReq.Version
	}
	if targetID <= 0 {
		return fail("invalid target")
	}
	target, err := tx.GetUserByID(targetID)
	if err != nil || target == nil {
		return fail("target unavailable")
	}
	targetOwner, err := tx.IsBootstrapUser(target.ID)
	if err != nil {
		return fail("could not change credential")
	}
	banned, err = tx.IsUserBanned(target.ID)
	if err != nil || banned {
		return fail("target unavailable")
	}
	if targetID != actor.ID {
		if session.Role != model.RoleAdmin || actor.Role != model.RoleAdmin || targetOwner || (target.Role == model.RoleAdmin && !actorOwner) {
			return fail("permission denied")
		}
		for _, liveTarget := range s.sessions.GetAllByUserIDSnapshots(target.ID) {
			if liveTarget.Role != target.Role {
				return fail("target authority changed; inspect again")
			}
		}
	} else if m.RotatePersonalTokenReq == nil && m.PersonalTokenStatusReq == nil {
		return fail("use self rotation")
	}
	if m.IssuePersonalRecoveryReq != nil || m.RevokePersonalRecoveryReq != nil {
		if targetOwner || (target.Role != model.RoleUser && target.Role != model.RoleModerator) {
			return fail("remote recovery requires an ordinary account")
		}
	}
	currentVersion, err := tx.PersonalTokenVersion(target.ID)
	if err != nil {
		return fail("could not read credential version")
	}
	response := personalTokenResponse(m, "")
	if m.PersonalTokenStatusReq != nil {
		response.PersonalTokenStatusResp = &pb.PersonalTokenStatusResponse{TargetID: target.ID, Username: target.Username, Role: target.Role.String(), ChannelScope: target.ChannelScope, Version: currentVersion}
		return response, nil
	}
	if version < 0 || version != currentVersion {
		return fail("credential changed; inspect before a new explicit attempt")
	}
	now := time.Now().UTC()
	var changed bool
	switch {
	case m.RotatePersonalTokenReq != nil:
		req := m.RotatePersonalTokenReq
		if !crypto.IsGeneratedToken(req.Replacement) || req.CurrentToken == "" || crypto.HashToken(req.CurrentToken) != actor.PersonalTokenHash || crypto.HashToken(req.Replacement) == actor.PersonalTokenHash {
			return fail("invalid credential replacement")
		}
		if actorOwner {
			state, stateErr := tx.BootstrapTokenState()
			if stateErr != nil || state != datastore.BootstrapTokenFinalized {
				return fail("authenticate with the current personal credential before owner rotation")
			}
		}
		changed, err = tx.ReplacePersonalToken(actor.ID, version, actor.PersonalTokenHash, crypto.HashToken(req.Replacement), now)
	case m.RevokePersonalTokenReq != nil:
		changed, err = tx.ReplacePersonalToken(target.ID, version, target.PersonalTokenHash, "", now)
	case m.IssuePersonalRecoveryReq != nil:
		grant, generateErr := crypto.GenerateToken()
		if generateErr != nil {
			return fail("could not issue recovery")
		}
		changed, err = tx.IssuePersonalRecovery(target.ID, version, crypto.HashToken(grant), now, actor.ID)
		response.IssuePersonalRecoveryResp = &pb.IssuePersonalRecoveryResponse{TargetID: target.ID, Grant: grant, ExpiresAt: now.Add(datastore.PersonalRecoveryLifetime).Unix(), Version: version + 1}
	case m.RevokePersonalRecoveryReq != nil:
		changed, err = tx.RevokePersonalRecovery(target.ID, version)
	}
	if err != nil || !changed {
		return fail("credential changed or unavailable")
	}
	if err := tx.Commit(); err != nil {
		return fail("credential outcome uncertain; retain saved candidate and reconcile")
	}
	if m.RevokePersonalRecoveryReq != nil {
		return response, nil
	}
	var retired []retiredControlSession
	for _, targetSession := range s.sessions.GetAllByUserIDSnapshots(target.ID) {
		retired = append(retired, s.detachControlSession(handler, targetSession.ID))
	}
	return response, retired
}

// Redemption is a pre-auth operation, never provisioning. Its normal pre-auth
// IP and concurrency budgets apply and it returns no session or media authority.
func (s *Server) redeemPersonalRecovery(handler *ControlHandler, st datastore.DataProviderFactory, conn net.Conn, req *pb.RedeemPersonalRecoveryRequest) bool {
	response := &pb.ControlMessage{RedeemPersonalRecoveryResp: &pb.RedeemPersonalRecoveryResponse{Error: "recovery unavailable"}}
	if req.TargetID <= 0 || req.Username == "" || !crypto.IsGeneratedToken(req.Grant) || !crypto.IsGeneratedToken(req.Replacement) || req.Grant == req.Replacement {
		_ = writeControlMessage(conn, response)
		return false
	}
	s.credentialWorkMu.Lock()
	s.remoteModerationMu.Lock()
	var retired []retiredControlSession
	changed := func() bool {
		tx, err := st.Tx(context.Background())
		if err != nil {
			return false
		}
		defer tx.Rollback()
		target, err := tx.GetUserByID(req.TargetID)
		if err != nil || target == nil || target.Username != req.Username {
			return false
		}
		ip, err := canonicalControlPeerIP(conn.RemoteAddr())
		if err != nil {
			return false
		}
		banned, err := tx.IsIPBanned(ip)
		if err != nil || banned {
			return false
		}
		changed, err := tx.RedeemPersonalRecovery(req.TargetID, crypto.HashToken(req.Grant), crypto.HashToken(req.Replacement), time.Now().UTC())
		if err != nil || !changed || tx.Commit() != nil {
			return false
		}
		for _, targetSession := range s.sessions.GetAllByUserIDSnapshots(req.TargetID) {
			retired = append(retired, s.detachControlSession(handler, targetSession.ID))
		}
		return true
	}()
	s.remoteModerationMu.Unlock()
	s.credentialWorkMu.Unlock()
	if changed {
		response.RedeemPersonalRecoveryResp.Error = ""
	}
	s.finishRetiredSessions(handler, st, retired, conn, response)
	return changed
}

type retiredControlSession struct {
	session   SessionSnapshot
	client    *controlClient
	screen    *screenClientConn
	channelID int64
	stopEvent *pb.ScreenShareEvent
}

// Called with credentialWorkMu and remoteModerationMu exclusively held. Only
// detaches authority and queues: socket closure and publication happen outside.
func (s *Server) detachControlSession(handler *ControlHandler, sessionID uint32) retiredControlSession {
	session, live := s.sessions.GetSnapshot(sessionID)
	if !live {
		return retiredControlSession{}
	}
	retired := retiredControlSession{session: session}
	retired.screen = s.detachSessionAndScreenConn(sessionID)
	retired.stopEvent, _ = s.screenShare.StopBySession(sessionID)
	retired.channelID = s.channels.Leave(sessionID)
	handler.mu.Lock()
	retired.client = handler.connMap[sessionID]
	delete(handler.connMap, sessionID)
	delete(handler.chatSelection, sessionID)
	handler.mu.Unlock()
	if retired.client != nil {
		retired.client.mu.Lock()
		retired.client.terminal = true
		retired.client.pendingState = nil
	drain:
		for {
			if retired.client.beforeCredentialDrain != nil {
				retired.client.beforeCredentialDrain()
			}
			// The writer receives outside mu and may already own the last item.
			select {
			case <-retired.client.sendQueue:
			default:
				break drain
			}
		}
		retired.client.mu.Unlock()
	}
	s.removeVoiceStat(sessionID)
	s.metrics.TotalDisconnects.Add(1)
	return retired
}

func (s *Server) finishRetiredSessions(handler *ControlHandler, st datastore.DataProviderFactory, retired []retiredControlSession, initiating net.Conn, response *pb.ControlMessage) {
	terminal := false
	for _, r := range retired {
		if r.screen != nil {
			r.screen.close()
		}
		if r.client == nil {
			continue
		}
		if r.client == initiating {
			terminal = true
			continue
		}
		_ = r.client.Close()
		// Close interrupts an in-flight writer; join it before reporting completion.
		r.client.writeMu.Lock()
		r.client.writeMu.Unlock()
	}
	if response != nil {
		if terminal {
			c := initiating.(*controlClient)
			c.writeMu.Lock()
			_ = c.Conn.SetWriteDeadline(time.Now().Add(controlWriteTimeout))
			_ = protocol.WriteControlMessage(c.Conn, response)
			c.writeMu.Unlock()
			_ = c.Close()
		} else {
			_ = writeControlMessage(initiating, response)
		}
	}
	for _, r := range retired {
		if r.session.ID == 0 {
			continue
		}
		if r.stopEvent != nil {
			s.metrics.ScreenSharesStopped.Add(1)
			handler.broadcastToChannel(r.stopEvent.ChannelID, &pb.ControlMessage{ScreenShareEvent: r.stopEvent}, r.session.ID)
		}
		if r.channelID > 0 {
			handler.broadcastToChannel(r.channelID, &pb.ControlMessage{ChannelLeftEvent: &pb.ChannelLeftEvent{ChannelID: r.channelID, UserID: r.session.UserID, Username: r.session.Username}}, r.session.ID)
			s.cleanupTempChannel(r.channelID, st)
		}
	}
	s.metrics.ScreenShareSubscribers.Store(s.screenShare.SubscriberCount())
	if len(retired) > 0 {
		s.broadcastServerState(st, handler)
	}
}

func (s *Server) cleanupControlSession(handler *ControlHandler, st datastore.DataProviderFactory, sessionID uint32) {
	s.credentialWorkMu.Lock()
	s.remoteModerationMu.Lock()
	retired := s.detachControlSession(handler, sessionID)
	s.remoteModerationMu.Unlock()
	s.credentialWorkMu.Unlock()
	s.finishRetiredSessions(handler, st, []retiredControlSession{retired}, nil, nil)
}
