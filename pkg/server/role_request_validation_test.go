package server

import (
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestTokenRequestRejectsInvalidRole(t *testing.T) {
	for _, req := range []pb.CreateTokenRequest{
		{Role: "Admin"}, {Role: " moderator"}, {Role: ""},
	} {
		srv, st, _ := newTestServer(t)
		actor := mustCreateSession(t, srv.sessions, 1, "admin", model.RoleAdmin)
		conn := &bufferConn{}
		srv.handleCreateToken(actor.ID, &req, st, conn)
		response, err := protocol.ReadControlMessage(conn)
		if err != nil || response.ErrorResponse == nil {
			t.Errorf("invalid request %#v accepted: %#v %v", req, response, err)
		}
		if srv.metrics.TokensCreated.Load() != 0 {
			t.Error("invalid request minted token")
		}
	}
}

func TestUnknownRoleDoesNotDemoteTarget(t *testing.T) {
	srv, st, handler := newTestServer(t)
	user, err := st.NonTx().CreateUser("target", model.RoleModerator)
	if err != nil {
		t.Fatal(err)
	}
	actor := mustCreateSession(t, srv.sessions, 99, "admin", model.RoleAdmin)
	target := mustCreateSession(t, srv.sessions, user.ID, user.Username, user.Role)
	conn := &bufferConn{}
	srv.handleSetUserRole(handler, actor.ID, &pb.SetUserRoleRequest{TargetUserID: user.ID, NewRole: "Moderator"}, st, conn)
	response, err := protocol.ReadControlMessage(conn)
	if err != nil || response.ErrorResponse == nil {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	persisted, err := st.NonTx().GetUserByID(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := srv.sessions.GetSnapshot(target.ID)
	if persisted.Role != model.RoleModerator || snapshot.Role != model.RoleModerator {
		t.Fatal("unknown role demoted target")
	}
}
