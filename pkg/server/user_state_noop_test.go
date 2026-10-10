package server

import (
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestUserStateBroadcastOnlyOnChange(t *testing.T) {
	srv, st, handler := newTestServer(t)
	session := mustCreateSession(t, srv.sessions, 1, "state", model.RoleUser)
	client := newControlClient(session.ID, &nopConn{})
	handler.connMap[session.ID] = client
	for _, tc := range []struct {
		id              uint32
		muted, deafened bool
		changed         bool
	}{
		{session.ID, false, false, false},
		{session.ID, true, false, true},
		{session.ID, true, false, false},
		{session.ID, true, true, true},
		{session.ID, false, true, true},
		{session.ID, false, false, true},
		{session.ID + 1, true, true, false},
	} {
		srv.handleUserState(handler, tc.id, &pb.UserStateUpdate{Muted: tc.muted, Deafened: tc.deafened}, st)
		select {
		case item := <-client.sendQueue:
			if !tc.changed || item.message.ServerStateEvent == nil {
				t.Fatalf("unexpected state broadcast for %+v", tc)
			}
		default:
			if tc.changed {
				t.Fatalf("missing state broadcast for %+v", tc)
			}
		}
		if tc.id == session.ID {
			current, _ := srv.sessions.GetSnapshot(session.ID)
			if current.Muted != tc.muted || current.Deafened != tc.deafened {
				t.Fatal("state mutation lost")
			}
		}
	}
}
