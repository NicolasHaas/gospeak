package server

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func personalMutationActor(t *testing.T, srv *Server, st datastore.DataProviderFactory, handler *ControlHandler, name string, role model.Role, owner bool) (*model.User, *controlClient) {
	t.Helper()
	var user *model.User
	var err error
	if owner {
		if err = st.NonTx().CreateBootstrapToken("setup-" + name); err != nil {
			t.Fatal(err)
		}
		tx, err := st.Tx(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		user, err = tx.ProvisionBootstrapUser("setup-"+name, name, crypto.HashToken("personal-"+name), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.FinalizeBootstrapToken(user.ID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	} else {
		user, err = st.NonTx().CreateUser(name, role)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.NonTx().UpdateUserPersonalToken(user.ID, crypto.HashToken("personal-"+name), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	session := mustCreateSession(t, srv.sessions, user.ID, user.Username, user.Role)
	c := handler.setConn(session.ID, &bufferConn{})
	c.credentialHash = crypto.HashToken("personal-" + name)
	return user, c
}

func personalMutation(t *testing.T, srv *Server, st datastore.DataProviderFactory, handler *ControlHandler, c *controlClient, req *pb.ControlMessage) *pb.ControlMessage {
	t.Helper()
	srv.credentialWorkMu.Lock()
	srv.remoteModerationMu.Lock()
	response, retired := srv.mutatePersonalToken(handler, c.sessionID, req, st, c)
	srv.remoteModerationMu.Unlock()
	srv.credentialWorkMu.Unlock()
	srv.finishRetiredSessions(handler, st, retired, nil, nil)
	return response
}

func TestPersonalTokenRestrictedRevocationAndPersistedAuthority(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		actor, target               model.Role
		owner, targetOwner, allowed bool
		stale                       string
	}{
		{name: "admin-user", actor: model.RoleAdmin, target: model.RoleUser, allowed: true},
		{name: "admin-moderator", actor: model.RoleAdmin, target: model.RoleModerator, allowed: true},
		{name: "moderator-user", actor: model.RoleModerator, target: model.RoleUser},
		{name: "user-user", actor: model.RoleUser, target: model.RoleUser},
		{name: "admin-peer", actor: model.RoleAdmin, target: model.RoleAdmin},
		{name: "owner-admin", actor: model.RoleAdmin, target: model.RoleAdmin, owner: true, allowed: true},
		{name: "admin-owner", actor: model.RoleAdmin, target: model.RoleAdmin, targetOwner: true},
		{name: "persisted-demotion", actor: model.RoleAdmin, target: model.RoleUser, stale: "persisted-role"},
		{name: "live-demotion", actor: model.RoleAdmin, target: model.RoleUser, stale: "live-role"},
		{name: "actor-banned", actor: model.RoleAdmin, target: model.RoleUser, stale: "ban"},
		{name: "actor-revoked", actor: model.RoleAdmin, target: model.RoleUser, stale: "credential"},
		{name: "target-promoted", actor: model.RoleAdmin, target: model.RoleUser, stale: "target-role"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, st, handler := newTestServer(t)
			actor, c := personalMutationActor(t, srv, st, handler, "actor", tc.actor, tc.owner)
			target, _ := personalMutationActor(t, srv, st, handler, "target", tc.target, tc.targetOwner)
			switch tc.stale {
			case "persisted-role":
				if err := st.NonTx().UpdateUserRole(actor.ID, model.RoleUser); err != nil {
					t.Fatal(err)
				}
			case "live-role":
				srv.sessions.UpdateRoleByUserID(actor.ID, model.RoleUser)
			case "ban":
				if err := st.NonTx().CreateUserBan(actor.ID, 0, time.Time{}); err != nil {
					t.Fatal(err)
				}
			case "credential":
				if err := st.NonTx().UpdateUserPersonalToken(actor.ID, "", time.Now()); err != nil {
					t.Fatal(err)
				}
			case "target-role":
				if err := st.NonTx().UpdateUserRole(target.ID, model.RoleAdmin); err != nil {
					t.Fatal(err)
				}
			}
			version, err := st.NonTx().PersonalTokenVersion(target.ID)
			if err != nil {
				t.Fatal(err)
			}
			r := personalMutation(t, srv, st, handler, c, &pb.ControlMessage{RevokePersonalTokenReq: &pb.RevokePersonalTokenRequest{TargetID: target.ID, Version: version}}).RevokePersonalTokenResp
			if (r.Error == "") != tc.allowed {
				t.Fatalf("allowed=%v, response=%s", tc.allowed, r.Error)
			}
			stored, err := st.NonTx().GetUserByID(target.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.allowed {
				if stored.PersonalTokenHash != "" || len(srv.sessions.GetAllByUserIDSnapshots(target.ID)) != 0 {
					t.Fatal("revocation retained credential/session authority")
				}
			} else if stored.PersonalTokenHash == "" {
				t.Fatal("denied revocation changed credential")
			}
			if stored.ID != target.ID || stored.Username != target.Username || stored.ChannelScope != target.ChannelScope {
				t.Fatal("identity changed")
			}
		})
	}
}

func TestPersonalRecoveryWireLossRestartAndStaleRevoke(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, st, handler := newTestServer(t)
	admin, adminConn := personalMutationActor(t, srv, st, handler, "recovery-admin", model.RoleAdmin, false)
	target, targetConn := personalMutationActor(t, srv, st, handler, "recovery-user", model.RoleModerator, false)
	version, err := st.NonTx().PersonalTokenVersion(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	addr, pin := personalCredentialListener(t, srv, handler, st, true)
	// The actual admin wire path issues a temporary bearer only.
	ctrl, _, err := client.OpenPersonalTokenControl(context.Background(), addr, pin, admin.Username, "personal-"+admin.Username)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	result, err := ctrl.PersonalTokenExchange(context.Background(), &pb.ControlMessage{IssuePersonalRecoveryReq: &pb.IssuePersonalRecoveryRequest{TargetID: target.ID, Version: version}})
	if err != nil {
		t.Fatal(err)
	}
	issued := result.IssuePersonalRecoveryResp
	if issued.TargetID != target.ID || !crypto.IsGeneratedToken(issued.Grant) || issued.Version != version+1 || issued.ExpiresAt <= time.Now().Unix() {
		t.Fatal("invalid recovery grant completion")
	}
	if _, live := srv.sessions.GetSnapshot(targetConn.sessionID); live {
		t.Fatal("grant issuance retained target session")
	}
	candidate, _ := crypto.GenerateToken()
	attempt, _ := crypto.GenerateToken()
	bs := client.NewBookmarkStore()
	if err := bs.Save(); err != nil {
		t.Fatal(err)
	}
	p := client.PendingPersonalCredential{ControlAddr: addr, ServerIdentity: pin, Username: target.Username, AccountID: target.ID, Version: issued.Version, RecoveryGrant: issued.Grant, Candidate: candidate, Attempt: attempt}
	if err := bs.StagePersonalCredential(p, true); err != nil {
		t.Fatal(err)
	}
	// Wrong target consumes nothing. No occupied-username provisioning path exists.
	wrong := p
	wrong.AccountID = admin.ID
	c, err := client.NewControlClient(addr, pin)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.PersonalTokenExchange(context.Background(), &pb.ControlMessage{RedeemPersonalRecoveryReq: &pb.RedeemPersonalRecoveryRequest{TargetID: wrong.AccountID, Username: wrong.Username, Grant: wrong.RecoveryGrant, Replacement: wrong.Candidate}})
	_ = c.Close()
	if err == nil {
		t.Fatal("wrong account redeemed grant")
	}
	// The real server commits, but its completion transport fails before ACK.
	if err := bs.SubmitPersonalCredential(context.Background(), p); err == nil {
		t.Fatal("lost recovery acknowledgement was not injected")
	}
	restarted := client.NewBookmarkStore()
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	saved, found, err := restarted.PendingPersonalCredential(addr, target.Username, target.ID)
	if err != nil || !found || saved != p {
		t.Fatal("restart changed pending recovery")
	}
	if err := restarted.ReconcilePersonalCredential(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	if len(restarted.PendingCredentials) != 0 || restarted.Bookmarks[0].Token != candidate {
		t.Fatal("candidate was not privately finalized")
	}
	stale := personalMutation(t, srv, st, handler, adminConn, &pb.ControlMessage{RevokePersonalTokenReq: &pb.RevokePersonalTokenRequest{TargetID: target.ID, Version: issued.Version}})
	if stale.RevokePersonalTokenResp.Error == "" {
		t.Fatal("stale admin retry revoked recovered credential")
	}
	reused, err := client.NewControlClient(addr, pin)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reused.PersonalTokenExchange(context.Background(), &pb.ControlMessage{RedeemPersonalRecoveryReq: &pb.RedeemPersonalRecoveryRequest{TargetID: target.ID, Username: target.Username, Grant: issued.Grant, Replacement: attempt}})
	_ = reused.Close()
	if err == nil {
		t.Fatal("reused recovery accepted")
	}
	stored, err := st.NonTx().GetUserByPersonalTokenHash(crypto.HashToken(candidate))
	if err != nil || stored == nil || stored.ID != target.ID || stored.Role != target.Role {
		t.Fatal("recovered identity changed")
	}
}

func TestPersonalTokenRetiresScreenBindingAndQueuedSender(t *testing.T) {
	srv, st, handler := newTestServer(t)
	_, admin := personalMutationActor(t, srv, st, handler, "admin", model.RoleAdmin, false)
	target, c := personalMutationActor(t, srv, st, handler, "sender", model.RoleUser, false)
	viewer, _ := personalMutationActor(t, srv, st, handler, "viewer", model.RoleUser, false)
	targetSession, _ := srv.sessions.GetSnapshot(c.sessionID)
	srv.sessions.SetChannel(targetSession.ID, 1)
	srv.channels.Join(targetSession.ID, 1)
	if _, err := srv.screenShare.Start(1, targetSession.ID, target.ID, target.Username, 100, 100); err != nil {
		t.Fatal(err)
	}
	screen, peer := net.Pipe()
	defer peer.Close()
	if _, ok := srv.bindScreenConn(targetSession.ID, targetSession.ScreenAuthToken, screen); !ok {
		t.Fatal("screen binding refused")
	}
	version, _ := st.NonTx().PersonalTokenVersion(target.ID)
	response := personalMutation(t, srv, st, handler, admin, &pb.ControlMessage{RevokePersonalTokenReq: &pb.RevokePersonalTokenRequest{TargetID: target.ID, Version: version}})
	if response.RevokePersonalTokenResp.Error != "" {
		t.Fatal(response.RevokePersonalTokenResp.Error)
	}
	if _, ok := srv.bindScreenConn(targetSession.ID, targetSession.ScreenAuthToken, &bufferConn{}); ok {
		t.Fatal("retired session rebound screen")
	}
	if srv.screenShare.IsSharer(targetSession.ID, 1) || srv.channels.ChannelOf(targetSession.ID) != 0 {
		t.Fatal("retired share/channel authority remains")
	}
	// Actual writer drops a frame already queued with the retired sender ID.
	viewerSessions := srv.sessions.GetAllByUserIDSnapshots(viewer.ID)
	frame := []byte{0, 0, 0, 9, 0, 0, 0, 0, 0, 0, 0, 1, 1}
	binary.BigEndian.PutUint32(frame[4:8], targetSession.ID)
	sink := &bufferConn{}
	if err := srv.writeLiveScreenFrame(viewerSessions[0].ID, &screenClientConn{conn: sink}, frame); err != nil {
		t.Fatal(err)
	}
	if sink.buffer.Len() != 0 {
		t.Fatal("retired sender frame emitted")
	}
}
