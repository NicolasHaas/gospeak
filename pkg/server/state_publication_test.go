package server

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

// Pause after the writer takes its slot, but before marshaling its message.
type stateDeadlineConn struct {
	*blockingWriteConn
	once             sync.Once
	entered, release chan struct{}
}

func (c *stateDeadlineConn) SetWriteDeadline(time.Time) error {
	c.once.Do(func() { close(c.entered); <-c.release })
	return nil
}

func TestStateQueueReplacesObsoleteSuffixAndPreservesReliable(t *testing.T) {
	wire := &stateDeadlineConn{blockingWriteConn: newBlockingWriteConn(), entered: make(chan struct{}), release: make(chan struct{})}
	close(wire.releaseFirst) // this test blocks before marshaling, not inside Write
	c := newControlClient(1, wire)
	done := make(chan struct{})
	go func() { c.writeLoop(); close(done) }()
	var release sync.Once
	defer func() { release.Do(func() { close(wire.release) }); _ = c.Close(); <-done }()
	state := func(id int64) *pb.ControlMessage {
		return &pb.ControlMessage{ServerStateEvent: &pb.ServerStateEvent{Channels: []pb.ChannelInfo{{ID: id}}}}
	}
	if err := c.send(state(1)); err != nil {
		t.Fatal(err)
	}
	<-wire.entered
	ack := &pb.ControlMessage{ChannelJoinResponse: &pb.ChannelJoinResponse{ChannelID: 2, Success: true}}
	for _, msg := range []*pb.ControlMessage{state(2), state(3), ack, state(4), state(5)} {
		if err := c.send(msg); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(c.sendQueue); got != 3 {
		t.Fatalf("queue has %d entries, want state3, ACK, state5", got)
	}
	result := make(chan error, 1)
	if err := c.enqueue(queuedControlMessage{message: &pb.ControlMessage{ErrorResponse: &pb.ErrorResponse{Code: 99}}, result: result}); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(wire.release) })
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal result not delivered")
	}
	reader := bytes.NewReader(wire.bytes())
	for i, want := range []int64{1, 3, 0, 5, 0} {
		got, err := protocol.ReadControlMessage(reader)
		if err != nil {
			t.Fatal(err)
		}
		if want != 0 {
			if got.ServerStateEvent == nil || got.ServerStateEvent.Channels[0].ID != want {
				t.Fatalf("snapshot order at %d: %#v", i, got)
			}
		} else if i == 2 {
			if got.ChannelJoinResponse == nil {
				t.Fatal("ACK barrier lost")
			}
		} else if got.ErrorResponse == nil || got.ErrorResponse.Code != 99 {
			t.Fatal("terminal error lost")
		}
	}
	if reader.Len() != 0 {
		t.Fatal("obsolete frames retained")
	}
}

func TestStatePublicationSlowClientIsolated(t *testing.T) {
	srv, st, h := newTestServer(t)
	h.statePublisher.Do(func() {})
	blocked, slowPeer := net.Pipe()
	fast, fastPeer := net.Pipe()
	defer func() { _ = slowPeer.Close(); _ = fastPeer.Close() }()
	slow := h.setConn(1, blocked)
	h.setConn(2, fast)
	srv.broadcastServerState(st, h)
	srv.flushServerState(st, h)
	_ = fastPeer.SetReadDeadline(time.Now().Add(time.Second))
	if msg, err := protocol.ReadControlMessage(fastPeer); err != nil || msg.ServerStateEvent == nil {
		t.Fatalf("slow client blocked healthy writer: %#v %v", msg, err)
	}
	for range 1000 {
		if err := slow.send(&pb.ControlMessage{ServerStateEvent: &pb.ServerStateEvent{}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(slow.sendQueue) > 1 {
		t.Fatal("slow client's obsolete suffix grew")
	}
}
