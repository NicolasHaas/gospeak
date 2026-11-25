package server

import (
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type invitePeerConn struct{ bufferConn }

func (*invitePeerConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
}

func TestInviteOwnerIPBanExemption(t *testing.T) {
	for _, owner := range []bool{false, true} {
		t.Run(map[bool]string{false: "admin", true: "owner"}[owner], func(t *testing.T) {
			srv, st, handler := newTestServer(t)
			actor, c := personalMutationActor(t, srv, st, handler, "actor", model.RoleAdmin, owner)
			if err := st.NonTx().CreateIPBan("127.0.0.1", 0, time.Time{}); err != nil {
				t.Fatal(err)
			}
			if err := st.NonTx().CreateToken("invite", model.RoleUser, 0, actor.ID, 1, time.Time{}); err != nil {
				t.Fatal(err)
			}
			invites, _, err := st.NonTx().ListInvites(0, 1)
			if err != nil || len(invites) != 1 {
				t.Fatal("missing invite")
			}
			for _, revoke := range []bool{false, true} {
				req, resp := &pb.ControlMessage{}, &pb.ControlMessage{}
				if revoke {
					req.RevokeInviteReq = &pb.RevokeInviteRequest{InviteID: invites[0].ID}
					resp.RevokeInviteResp = &pb.RevokeInviteResponse{}
				} else {
					req.ListInvitesReq = &pb.ListInvitesRequest{}
					resp.ListInvitesResp = &pb.ListInvitesResponse{}
				}
				result := srv.manageInvites(c.sessionID, req, resp, st, &invitePeerConn{})
				if (result == "") != owner {
					t.Fatalf("owner=%t revoke=%t result=%q", owner, revoke, result)
				}
			}
		})
	}
}
