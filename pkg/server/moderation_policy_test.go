package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func requireOpen(t *testing.T, conn *closeTrackingConn) {
	t.Helper()
	select {
	case <-conn.closed:
		t.Fatal("connection was unexpectedly closed")
	default:
	}
}

func TestKickTargetHierarchy(t *testing.T) {
	tests := []struct {
		name       string
		actorRole  model.Role
		targetRole model.Role
		wantClose  bool
	}{
		{name: "moderator kicks user", actorRole: model.RoleModerator, targetRole: model.RoleUser, wantClose: true},
		{name: "moderator cannot kick moderator", actorRole: model.RoleModerator, targetRole: model.RoleModerator},
		{name: "moderator cannot kick admin", actorRole: model.RoleModerator, targetRole: model.RoleAdmin},
		{name: "admin kicks user", actorRole: model.RoleAdmin, targetRole: model.RoleUser, wantClose: true},
		{name: "admin kicks moderator", actorRole: model.RoleAdmin, targetRole: model.RoleModerator, wantClose: true},
		{name: "admin kicks admin", actorRole: model.RoleAdmin, targetRole: model.RoleAdmin, wantClose: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, st, handler := newTestServer(t)
			actorUser, err := st.NonTx().CreateUser("actor", tt.actorRole)
			if err != nil {
				t.Fatalf("CreateUser(actor): %v", err)
			}
			targetUser, err := st.NonTx().CreateUser("target", tt.targetRole)
			if err != nil {
				t.Fatalf("CreateUser(target): %v", err)
			}
			actor := mustCreateSession(t, srv.sessions, actorUser.ID, actorUser.Username, actorUser.Role)
			target := mustCreateSession(t, srv.sessions, targetUser.ID, targetUser.Username, targetUser.Role)
			targetConn := registerTestConn(handler, target.ID)

			srv.handleKickUser(handler, actor.ID, &pb.KickUserRequest{UserID: targetUser.ID, Reason: "policy-test"}, st, &nopConn{})
			if tt.wantClose {
				requireClosed(t, targetConn)
			} else {
				requireOpen(t, targetConn)
			}
		})
	}
}

func TestModerationRejectsSelfTarget(t *testing.T) {
	srv, st, handler := newTestServer(t)
	adminUser, err := st.NonTx().CreateUser("self-admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	admin := mustCreateSession(t, srv.sessions, adminUser.ID, adminUser.Username, adminUser.Role)
	adminConn := registerTestConn(handler, admin.ID)

	srv.handleKickUser(handler, admin.ID, &pb.KickUserRequest{UserID: adminUser.ID}, st, &nopConn{})
	requireOpen(t, adminConn)
	srv.handleBanUser(handler, admin.ID, &pb.BanUserRequest{UserID: adminUser.ID}, st, &nopConn{})
	banned, err := st.NonTx().IsUserBanned(adminUser.ID)
	if err != nil {
		t.Fatalf("IsUserBanned: %v", err)
	}
	if banned {
		t.Fatal("self-ban persisted")
	}
	requireOpen(t, adminConn)
}

func TestInvalidBanDurationsAreRejected(t *testing.T) {
	for _, duration := range []int64{-1, maxBanDurationSeconds + 1} {
		t.Run(fmt.Sprintf("duration_%d", duration), func(t *testing.T) {
			srv, st, handler := newTestServer(t)
			adminUser, err := st.NonTx().CreateUser("duration-admin", model.RoleAdmin)
			if err != nil {
				t.Fatalf("CreateUser(admin): %v", err)
			}
			targetUser, err := st.NonTx().CreateUser("duration-target", model.RoleUser)
			if err != nil {
				t.Fatalf("CreateUser(target): %v", err)
			}
			admin := mustCreateSession(t, srv.sessions, adminUser.ID, adminUser.Username, adminUser.Role)
			mustCreateSession(t, srv.sessions, targetUser.ID, targetUser.Username, targetUser.Role)
			responseConn := &bufferConn{}
			srv.handleBanUser(handler, admin.ID, &pb.BanUserRequest{UserID: targetUser.ID, DurationSeconds: duration}, st, responseConn)
			response, err := protocol.ReadControlMessage(responseConn)
			if err != nil || response.ErrorResponse == nil || response.ErrorResponse.Message != "invalid ban duration" {
				t.Fatalf("response = %#v, err=%v", response, err)
			}
			banned, err := st.NonTx().IsUserBanned(targetUser.ID)
			if err != nil || banned {
				t.Fatalf("IsUserBanned = %t, %v", banned, err)
			}
		})
	}
}

func TestAdminCanBanAnotherAdmin(t *testing.T) {
	srv, st, handler := newTestServer(t)
	actorUser, err := st.NonTx().CreateUser("actor-admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(actor): %v", err)
	}
	targetUser, err := st.NonTx().CreateUser("target-admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(target): %v", err)
	}
	actor := mustCreateSession(t, srv.sessions, actorUser.ID, actorUser.Username, actorUser.Role)
	target := mustCreateSession(t, srv.sessions, targetUser.ID, targetUser.Username, targetUser.Role)
	targetConn := registerTestConn(handler, target.ID)

	srv.handleBanUser(handler, actor.ID, &pb.BanUserRequest{UserID: targetUser.ID}, st, &nopConn{})
	requireClosed(t, targetConn)
	banned, err := st.NonTx().IsUserBanned(targetUser.ID)
	if err != nil {
		t.Fatalf("IsUserBanned: %v", err)
	}
	if !banned {
		t.Fatal("admin-to-admin ban was not persisted")
	}
}

func TestBootstrapAdminCannotBeBannedOrDemotedRemotely(t *testing.T) {
	srv, st, handler := newTestServer(t)
	if err := st.NonTx().CreateBootstrapToken("bootstrap-policy"); err != nil {
		t.Fatalf("CreateBootstrapToken: %v", err)
	}
	tx, err := st.Tx(context.Background())
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	owner, err := tx.ProvisionBootstrapUser("bootstrap-policy", "bootstrap-owner", "bootstrap-personal", time.Now().UTC())
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("ProvisionBootstrapUser: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	actorUser, err := st.NonTx().CreateUser("other-admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(actor): %v", err)
	}
	actor := mustCreateSession(t, srv.sessions, actorUser.ID, actorUser.Username, actorUser.Role)
	ownerSession := mustCreateSession(t, srv.sessions, owner.ID, owner.Username, owner.Role)
	ownerConn := registerTestConn(handler, ownerSession.ID)

	srv.handleBanUser(handler, actor.ID, &pb.BanUserRequest{UserID: owner.ID}, st, &nopConn{})
	banned, err := st.NonTx().IsUserBanned(owner.ID)
	if err != nil {
		t.Fatalf("IsUserBanned: %v", err)
	}
	if banned {
		t.Fatal("bootstrap owner was banned")
	}
	requireOpen(t, ownerConn)

	srv.handleSetUserRole(handler, actor.ID, &pb.SetUserRoleRequest{TargetUserID: owner.ID, NewRole: model.RoleUser.String()}, st, &nopConn{})
	persisted, err := st.NonTx().GetUserByID(owner.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if persisted.Role != model.RoleAdmin {
		t.Fatalf("bootstrap owner role = %s, want admin", persisted.Role)
	}
}

func TestBanManagementResponsesUseSerializedWriter(t *testing.T) {
	srv, st, _ := newTestServer(t)
	adminUser, err := st.NonTx().CreateUser("serialized-ban-admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(admin): %v", err)
	}
	admin := mustCreateSession(t, srv.sessions, adminUser.ID, adminUser.Username, adminUser.Role)
	targetUser, err := st.NonTx().CreateUser("serialized-ban-target", model.RoleUser)
	if err != nil {
		t.Fatalf("CreateUser(target): %v", err)
	}
	if err := st.NonTx().CreateUserBan(targetUser.ID, adminUser.ID, time.Time{}); err != nil {
		t.Fatalf("CreateUserBan: %v", err)
	}
	if err := st.NonTx().CreateIPBan("192.0.2.200", adminUser.ID, time.Time{}); err != nil {
		t.Fatalf("CreateIPBan: %v", err)
	}
	if err := st.NonTx().CreateIPBan("192.0.2.201", adminUser.ID, time.Time{}); err != nil {
		t.Fatalf("CreateIPBan: %v", err)
	}
	bans, _, err := st.NonTx().ListActiveBans(0, datastore.MaxBanPageSize)
	if err != nil || len(bans) != 3 {
		t.Fatalf("ListActiveBans = %#v, %v", bans, err)
	}

	listWire := &bufferConn{}
	listClient := newControlClient(admin.ID, listWire)
	srv.handleListBans(admin.ID, &pb.ListBansRequest{Limit: 1}, st, listClient)
	if listWire.buffer.Len() != 0 || len(listClient.sendQueue) != 1 {
		t.Fatalf("list response bypassed queue: wire=%d queue=%d", listWire.buffer.Len(), len(listClient.sendQueue))
	}
	listItem := <-listClient.sendQueue
	if listItem.message.ListBansResp == nil || len(listItem.message.ListBansResp.Bans) != 1 || !listItem.message.ListBansResp.HasMore {
		t.Fatalf("queued list response = %#v", listItem.message)
	}
	if got := listItem.message.ListBansResp.Bans[0].Username; got != targetUser.Username {
		t.Fatalf("listed username = %q, want %q", got, targetUser.Username)
	}

	unbanWire := &bufferConn{}
	unbanClient := newControlClient(admin.ID, unbanWire)
	srv.handleUnban(admin.ID, &pb.UnbanRequest{BanID: bans[0].ID}, st, unbanClient)
	if unbanWire.buffer.Len() != 0 || len(unbanClient.sendQueue) != 1 {
		t.Fatalf("unban response bypassed queue: wire=%d queue=%d", unbanWire.buffer.Len(), len(unbanClient.sendQueue))
	}
	unbanItem := <-unbanClient.sendQueue
	if unbanItem.message.UnbanResp == nil || !unbanItem.message.UnbanResp.Success {
		t.Fatalf("queued unban response = %#v", unbanItem.message)
	}
}

func TestListAndRemoveBansAreAdminOnly(t *testing.T) {
	srv, st, _ := newTestServer(t)
	adminUser, err := st.NonTx().CreateUser("ban-admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(admin): %v", err)
	}
	moderatorUser, err := st.NonTx().CreateUser("ban-moderator", model.RoleModerator)
	if err != nil {
		t.Fatalf("CreateUser(moderator): %v", err)
	}
	admin := mustCreateSession(t, srv.sessions, adminUser.ID, adminUser.Username, adminUser.Role)
	moderator := mustCreateSession(t, srv.sessions, moderatorUser.ID, moderatorUser.Username, moderatorUser.Role)
	if err := st.NonTx().CreateIPBan("192.0.2.123", adminUser.ID, time.Time{}); err != nil {
		t.Fatalf("CreateIPBan: %v", err)
	}
	bans, _, err := st.NonTx().ListActiveBans(0, datastore.MaxBanPageSize)
	if err != nil || len(bans) != 1 {
		t.Fatalf("ListActiveBans = %#v, %v", bans, err)
	}

	moderatorList := &bufferConn{}
	srv.handleListBans(moderator.ID, &pb.ListBansRequest{}, st, moderatorList)
	response, err := protocol.ReadControlMessage(moderatorList)
	if err != nil {
		t.Fatalf("Read moderator list response: %v", err)
	}
	if response.ErrorResponse == nil {
		t.Fatalf("moderator list response = %#v, want rejection", response)
	}

	moderatorDelete := &bufferConn{}
	srv.handleUnban(moderator.ID, &pb.UnbanRequest{BanID: bans[0].ID}, st, moderatorDelete)
	remaining, _, err := st.NonTx().ListActiveBans(0, datastore.MaxBanPageSize)
	if err != nil || len(remaining) != 1 {
		t.Fatalf("moderator changed bans: %#v, %v", remaining, err)
	}

	adminList := &bufferConn{}
	srv.handleListBans(admin.ID, &pb.ListBansRequest{}, st, adminList)
	response, err = protocol.ReadControlMessage(adminList)
	if err != nil {
		t.Fatalf("Read admin list response: %v", err)
	}
	if response.ListBansResp == nil || len(response.ListBansResp.Bans) != 1 {
		t.Fatalf("admin list response = %#v", response)
	}

	adminDelete := &bufferConn{}
	srv.handleUnban(admin.ID, &pb.UnbanRequest{BanID: bans[0].ID}, st, adminDelete)
	response, err = protocol.ReadControlMessage(adminDelete)
	if err != nil {
		t.Fatalf("Read admin unban response: %v", err)
	}
	if response.UnbanResp == nil || !response.UnbanResp.Success {
		t.Fatalf("admin unban response = %#v", response)
	}
}
