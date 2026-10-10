package client

import (
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type receivingDeadlineConn struct {
	net.Conn
	deadlines chan time.Time
}

func (c *receivingDeadlineConn) SetReadDeadline(at time.Time) error {
	if err := c.Conn.SetReadDeadline(at); err != nil {
		return err
	}
	c.deadlines <- at
	return nil
}

func TestControlReceivingBoundsPartialFramesWithoutIdleTimeout(t *testing.T) {
	conn, peer := net.Pipe()
	defer conn.Close() //nolint:errcheck
	defer peer.Close() //nolint:errcheck
	tracked := &receivingDeadlineConn{conn, make(chan time.Time, 4)}
	client := &ControlClient{conn: tracked, done: make(chan struct{})}
	received := make(chan struct{}, 1)
	client.SetEventHandler(func(*pb.ControlMessage) { received <- struct{}{} })
	client.StartReceiving()
	if err := protocol.WriteControlMessage(peer, &pb.ControlMessage{Pong: &pb.Pong{Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("complete frame was not delivered")
	}
	if at := <-tracked.deadlines; at.IsZero() || time.Until(at) > 5*time.Second {
		t.Fatal("wrong completion deadline", at)
	}
	if at := <-tracked.deadlines; !at.IsZero() {
		t.Fatal("completion deadline was not cleared", at)
	}
	select {
	case at := <-tracked.deadlines:
		t.Fatal("idle client read acquired a deadline", at)
	default:
	}
	if _, err := peer.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	select {
	case at := <-tracked.deadlines:
		if at.IsZero() || time.Until(at) > 5*time.Second {
			t.Fatal("partial header lacks completion deadline", at)
		}
	case <-time.After(time.Second):
		t.Fatal("partial header acquired no deadline")
	}
	if err := conn.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("partial header did not terminate reception")
	}
}
