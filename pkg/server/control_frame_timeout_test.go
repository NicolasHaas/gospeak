package server

import (
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type frameDeadlineConn struct {
	net.Conn
	deadlines chan time.Time
}

func (c *frameDeadlineConn) SetReadDeadline(at time.Time) error {
	if err := c.Conn.SetReadDeadline(at); err != nil {
		return err
	}
	c.deadlines <- at
	return nil
}

func TestControlFrameDeadlineStartsAtFirstByte(t *testing.T) {
	srv, st, handler := newTestServer(t)
	srv.cfg.AllowNoToken = true
	server, peer := net.Pipe()
	defer peer.Close() //nolint:errcheck
	conn := &frameDeadlineConn{server, make(chan time.Time, 8)}
	done := make(chan struct{})
	srv.startWorker(func() { defer close(done); srv.handleControlConn(handler, conn, st) })
	if err := protocol.WriteControlMessage(peer, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{Username: "frame-timeout", MediaCiphers: []string{"aes128"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.ReadControlMessage(peer); err != nil {
		t.Fatal(err)
	}
	// Drain pre-auth deadlines through the authenticated idle deadline.
	for {
		select {
		case at := <-conn.deadlines:
			if time.Until(at) > time.Minute {
				goto authenticated
			}
		case <-time.After(time.Second):
			t.Fatal("missing idle deadline")
		}
	}

authenticated:
	if _, err := peer.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	select {
	case at := <-conn.deadlines:
		if left := time.Until(at); left <= 0 || left > 5*time.Second {
			t.Fatalf("frame completion horizon = %v, want at most 5s", left)
		}
	case <-time.After(time.Second):
		t.Fatal("first frame byte did not bound completion")
	}
	if err := server.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("partial header survived completion expiry")
	}
	if srv.metrics.ControlInvalidMessages.Load() != 1 {
		t.Fatal("partial frame expiry was not counted as invalid")
	}
}
