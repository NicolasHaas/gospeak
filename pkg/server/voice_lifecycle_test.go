package server

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestVoiceLifecycleStateIncludesLobbyAndRetiresDisconnect(t *testing.T) {
	st := newChannelImportStore(t)
	s := New(DefaultConfig(), Dependencies{Store: st})
	defer s.Shutdown()
	session, err := s.sessions.Create(1, "peer", model.RoleUser, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	handler := newControlHandler(s, st)
	check := func(want int, broadcast bool) {
		t.Helper()
		server, peer := net.Pipe()
		defer server.Close()
		defer peer.Close()
		if broadcast {
			defer handler.removeConn(session.ID)
		}
		if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			if broadcast {
				handler.setConn(session.ID, server)
				handler.stateDirty.Store(true)
				s.flushServerState(st, handler)
			} else {
				s.sendServerState(st, handler, server)
			}
		}()
		msg, err := protocol.ReadControlMessage(peer)
		<-done
		if err != nil {
			t.Fatal(err)
		}
		if msg.ServerStateEvent == nil {
			t.Fatal("missing server state event")
		}
		ids := msg.ServerStateEvent.LiveSessionIDs
		if ids == nil || len(ids) != want {
			t.Fatalf("live IDs = %v, want nonnil length %d", ids, want)
		}
	}
	check(1, false)
	check(1, true)
	s.sessions.Remove(session.ID)
	check(0, false)
	check(0, true)
	var wire bytes.Buffer
	if err := protocol.WriteControlMessage(&wire, &pb.ControlMessage{ServerStateEvent: &pb.ServerStateEvent{}}); err != nil {
		t.Fatal(err)
	}
}
