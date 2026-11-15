package server

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestInviteManagementAuthorizationAndMetadata(t *testing.T) {
	for _, role := range []model.Role{model.RoleUser, model.RoleModerator, model.RoleAdmin} {
		t.Run(role.String(), func(t *testing.T) {
			srv, st, handler := newTestServer(t)
			user, err := st.NonTx().CreateUser("actor", role)
			if err != nil {
				t.Fatal(err)
			}
			session := mustCreateSession(t, srv.sessions, user.ID, user.Username, user.Role)
			if err := st.NonTx().CreateToken("secret-fixture-hash", model.RoleAdmin, 0, user.ID, 0, time.Time{}); err != nil {
				t.Fatal(err)
			}
			request := func(id uint32, msg *pb.ControlMessage) *pb.ControlMessage {
				t.Helper()
				conn := &bufferConn{}
				srv.handleMessage(handler, id, msg, st, conn)
				wire := append([]byte(nil), conn.buffer.Bytes()...)
				if bytes.Contains(wire, []byte("secret-fixture-hash")) || bytes.Contains(wire, []byte(`"hash"`)) || bytes.Contains(wire, []byte(`"value"`)) {
					t.Fatal("secret exposed")
				}
				response, err := protocol.ReadControlMessage(conn)
				if err != nil {
					t.Fatal(err)
				}
				return response
			}
			page := request(session.ID, &pb.ControlMessage{ListInvitesReq: &pb.ListInvitesRequest{}}).ListInvitesResp
			if role != model.RoleAdmin {
				if page == nil || page.Error == "" || len(page.Invites) != 0 {
					t.Fatal("unauthorized listing")
				}
				denied := request(session.ID, &pb.ControlMessage{RevokeInviteReq: &pb.RevokeInviteRequest{InviteID: 1}}).RevokeInviteResp
				if denied == nil || denied.Error == "" || denied.Revoked {
					t.Fatal("unauthorized revocation")
				}
				return
			}
			if page == nil || page.Error != "" || len(page.Invites) != 1 || page.Invites[0].MaxUses != 0 || page.Invites[0].ExpiresAt != 0 || page.Invites[0].Role != "admin" {
				t.Fatal("missing unlimited admin metadata")
			}
			id := page.Invites[0].ID
			for _, req := range []*pb.ListInvitesRequest{{AfterID: -1}, {Limit: -1}, {Limit: 101}} {
				if request(session.ID, &pb.ControlMessage{ListInvitesReq: req}).ListInvitesResp.Error == "" {
					t.Fatal("invalid page accepted")
				}
			}
			for i := range 2 {
				response := request(session.ID, &pb.ControlMessage{RevokeInviteReq: &pb.RevokeInviteRequest{InviteID: id}}).RevokeInviteResp
				if response.Error != "" || response.Revoked != (i == 0) {
					t.Fatal("non-idempotent revoke")
				}
			}
			if request(0, &pb.ControlMessage{ListInvitesReq: &pb.ListInvitesRequest{}}).ListInvitesResp.Error == "" {
				t.Fatal("unauthenticated list")
			}
			if request(0, &pb.ControlMessage{RevokeInviteReq: &pb.RevokeInviteRequest{InviteID: id}}).RevokeInviteResp.Error == "" {
				t.Fatal("unauthenticated revoke")
			}
			if err := st.NonTx().CreateUserBan(user.ID, 0, time.Time{}); err != nil {
				t.Fatal(err)
			}
			if request(session.ID, &pb.ControlMessage{ListInvitesReq: &pb.ListInvitesRequest{}}).ListInvitesResp.Error == "" {
				t.Fatal("banned admin listed invites")
			}
		})
	}
}

func TestInviteRevocationDoesNotRestartBootstrap(t *testing.T) {
	srv, st, _ := newTestServerWithConfig(t, func(cfg *Config) { cfg.DataDir = t.TempDir() })
	if _, err := st.NonTx().CreateUser("existing-admin", model.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := st.NonTx().CreateToken("last-invite", model.RoleAdmin, 0, 1, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	page, _, err := st.NonTx().ListInvites(0, 1)
	if err != nil || len(page) != 1 {
		t.Fatal("missing invite")
	}
	if revoked, err := st.NonTx().RevokeInvite(page[0].ID); err != nil || !revoked {
		t.Fatal("revoke failed")
	}
	retained, _, err := st.NonTx().ListInvites(0, 1)
	if err != nil || len(retained) != 1 || retained[0].ExpiresAt.Unix() != 1 || retained[0].UseCount != 0 || retained[0].MaxUses != 0 {
		t.Fatal("revocation changed usage policy or lost lifecycle row")
	}
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatal(err)
	}
	state, err := st.NonTx().BootstrapTokenState()
	if err != nil || state != datastore.BootstrapTokenAbsent {
		t.Fatal("revoking last ordinary invite created a new bootstrap credential")
	}
}

type inviteTxBarrier struct {
	datastore.DataProviderFactory
	reached chan struct{}
	release chan struct{}
}

func (b *inviteTxBarrier) Tx(ctx context.Context) (datastore.DataStoreTx, error) {
	close(b.reached)
	<-b.release
	return b.DataProviderFactory.Tx(ctx)
}

func TestInvitePolicyRecheckedInsideTransaction(t *testing.T) {
	srv, st, _ := newTestServer(t)
	user, err := st.NonTx().CreateUser("actor", model.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	session := mustCreateSession(t, srv.sessions, user.ID, user.Username, user.Role)
	if err := st.NonTx().CreateToken("fixture", model.RoleUser, 0, user.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	page, _, err := st.NonTx().ListInvites(0, 1)
	if err != nil || len(page) != 1 {
		t.Fatal("missing invite")
	}
	barrier := &inviteTxBarrier{DataProviderFactory: st, reached: make(chan struct{}), release: make(chan struct{})}
	conn := &bufferConn{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handleInvites(session.ID, &pb.ControlMessage{RevokeInviteReq: &pb.RevokeInviteRequest{InviteID: page[0].ID}}, barrier, conn)
	}()
	<-barrier.reached
	// Persisted demotion while waiting for SQLite must override the earlier session snapshot.
	err = st.NonTx().UpdateUserRole(user.ID, model.RoleUser)
	close(barrier.release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	response, err := protocol.ReadControlMessage(conn)
	if err != nil {
		t.Fatal(err)
	}
	if response.RevokeInviteResp.Error == "" || response.RevokeInviteResp.Revoked {
		t.Fatal("stale admin authorized")
	}
	remaining, _, err := st.NonTx().ListInvites(0, 1)
	if err != nil || len(remaining) != 1 {
		t.Fatal("invite changed after denial")
	}
}
