package server

import (
	"context"
	"log/slog"
	"net"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
	"github.com/NicolasHaas/gospeak/pkg/rbac"
)

func (s *Server) handleInvites(sessionID uint32, msg *pb.ControlMessage, st datastore.DataProviderFactory, conn net.Conn) {
	response := &pb.ControlMessage{}
	if msg.ListInvitesReq != nil {
		response.ListInvitesResp = &pb.ListInvitesResponse{Invites: []pb.InviteInfo{}}
	} else {
		response.RevokeInviteResp = &pb.RevokeInviteResponse{}
	}
	// Return an operation-specific completion even on denial, so clients can clear in-flight state.
	operationError := s.manageInvites(sessionID, msg, response, st, conn)
	if response.ListInvitesResp != nil {
		response.ListInvitesResp.Error = operationError
		if operationError != "" {
			response.ListInvitesResp.Invites = nil
			response.ListInvitesResp.HasMore = false
		}
	} else {
		response.RevokeInviteResp.Error = operationError
		if operationError != "" {
			response.RevokeInviteResp.Revoked = false
		}
	}
	if err := writeControlMessage(conn, response); err != nil {
		_ = conn.Close()
	}
}

func (s *Server) manageInvites(sessionID uint32, msg, response *pb.ControlMessage, st datastore.DataProviderFactory, conn net.Conn) string {
	s.remoteModerationMu.Lock()
	defer s.remoteModerationMu.Unlock()
	session, ok := s.sessions.GetSnapshot(sessionID)
	if !ok || rbac.RequirePermission(session.Role, rbac.PermManageTokens) != "" {
		return "permission denied"
	}
	tx, err := st.Tx(context.Background())
	if err != nil {
		slog.Error("manage invites transaction", "err", err)
		return "could not manage invites"
	}
	defer func() { _ = tx.Rollback() }()
	// Role/ban checks and the mutation share the provisioning writer transaction.
	session, ok = s.sessions.GetSnapshot(sessionID)
	if !ok || rbac.RequirePermission(session.Role, rbac.PermManageTokens) != "" {
		return "permission denied"
	}
	user, err := tx.GetUserByID(session.UserID)
	if err != nil || user == nil || rbac.RequirePermission(user.Role, rbac.PermManageTokens) != "" {
		return "permission denied"
	}
	banned, err := tx.IsUserBanned(user.ID)
	if err != nil || banned {
		return "permission denied"
	}
	owner, err := tx.IsBootstrapUser(user.ID)
	if err != nil {
		return "permission denied"
	}
	if ip, err := canonicalControlPeerIP(conn.RemoteAddr()); err == nil && ip != "" {
		banned, err := tx.IsIPBanned(ip)
		if err != nil || (banned && !owner) {
			return "permission denied"
		}
	}
	if req := msg.ListInvitesReq; req != nil {
		limit := req.Limit
		if limit == 0 {
			limit = datastore.MaxInvitePageSize
		}
		if req.AfterID < 0 || limit < 1 || limit > datastore.MaxInvitePageSize {
			return "invalid invite page"
		}
		invites, more, err := tx.ListInvites(req.AfterID, limit)
		if err != nil {
			slog.Error("list invites", "err", err)
			return "could not list invites"
		}
		for _, invite := range invites {
			info := pb.InviteInfo{ID: invite.ID, Role: invite.Role.String(), ChannelScope: invite.ChannelScope, CreatedBy: invite.CreatedBy, MaxUses: int64(invite.MaxUses), UseCount: int64(invite.UseCount), CreatedAt: invite.CreatedAt.Unix()}
			if !invite.ExpiresAt.IsZero() {
				info.ExpiresAt = invite.ExpiresAt.Unix()
			}
			response.ListInvitesResp.Invites = append(response.ListInvitesResp.Invites, info)
		}
		response.ListInvitesResp.HasMore = more
	} else {
		if msg.RevokeInviteReq.InviteID <= 0 {
			return "invalid invite ID"
		}
		response.RevokeInviteResp.Revoked, err = tx.RevokeInvite(msg.RevokeInviteReq.InviteID)
		if err != nil {
			slog.Error("revoke invite", "err", err)
			return "could not revoke invite"
		}
	}
	if err := tx.Commit(); err != nil {
		slog.Error("manage invites commit", "err", err)
		return "could not manage invites"
	}
	return ""
}
