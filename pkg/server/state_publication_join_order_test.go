package server

import (
	"runtime"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestStatePublicationConcurrentJoinACKBarrier(t *testing.T) {
	srv, st, h := newTestServer(t)
	h.statePublisher.Do(func() {})
	session := mustCreateSession(t, srv.sessions, 1, "ack-order", model.RoleUser)
	ch := &model.Channel{Name: "ack-channel"}
	if err := st.NonTx().CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	c := newControlClient(session.ID, &nopConn{})
	h.connMap[session.ID] = c
	// Hold screen cleanup after membership commits. The ACK must already be
	// queued before a concurrent flush can observe that membership.
	srv.screenShare.mu.Lock()
	joined := make(chan struct{})
	go func() {
		srv.handleJoinChannel(h, session.ID, &pb.JoinChannelRequest{ChannelID: ch.ID}, st, c)
		close(joined)
	}()
	deadline := time.Now().Add(time.Second)
	for srv.channels.ChannelOf(session.ID) != ch.ID {
		if time.Now().After(deadline) {
			srv.screenShare.mu.Unlock()
			<-joined
			t.Fatal("join did not commit membership")
		}
		runtime.Gosched()
	}
	srv.broadcastServerState(st, h)
	srv.flushServerState(st, h)
	srv.screenShare.mu.Unlock()
	<-joined
	first := <-c.sendQueue
	if first.message.ChannelJoinResponse == nil {
		t.Fatalf("post-join snapshot preceded reliable ACK: %#v", first.message)
	}
}
