package server

import (
	"bytes"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type idleDeadlineConn struct {
	net.Conn
	deadlines chan time.Time
}

func (c *idleDeadlineConn) SetReadDeadline(at time.Time) error {
	if time.Until(at) > time.Minute {
		c.deadlines <- at
	}
	return c.Conn.SetReadDeadline(at)
}
func TestControlIdleDeadlineResetsOnlyForCompleteMessage(t *testing.T) {
	srv, st, handler := newTestServer(t)
	srv.cfg.AllowNoToken = true
	server, peer := net.Pipe()
	defer peer.Close() //nolint:errcheck
	conn := &idleDeadlineConn{server, make(chan time.Time, 4)}
	done := make(chan struct{})
	srv.startWorker(func() { defer close(done); srv.handleControlConn(handler, conn, st) })
	if err := protocol.WriteControlMessage(peer, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{Username: "idle", MediaCiphers: []string{"aes128"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.ReadControlMessage(peer); err != nil {
		t.Fatal(err)
	}
	select {
	case at := <-conn.deadlines:
		if left := time.Until(at); left < 4*time.Minute || left > 5*time.Minute {
			t.Fatal("wrong idle horizon", left)
		}
	case <-time.After(time.Second):
		t.Fatal("authenticated idle deadline absent")
	}
	if err := protocol.WriteControlMessage(peer, &pb.ControlMessage{Ping: &pb.Ping{Timestamp: 123}}); err != nil {
		t.Fatal(err)
	}
	response, err := protocol.ReadControlMessage(peer)
	if err != nil || response.Pong == nil {
		t.Fatal("ping failed", err)
	}
	select {
	case <-conn.deadlines:
	case <-time.After(time.Second):
		t.Fatal("complete message did not reset deadline")
	}
	// A partial frame cannot advance the deadline. Expire the pipe's existing
	// deadline directly rather than sleeping through the production horizon.
	if _, err := peer.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	if len(conn.deadlines) != 0 {
		t.Fatal("partial frame reset deadline")
	}
	if err := server.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("partial frame survived expiry")
	}
}

func TestControlIdleExpiryAccounting(t *testing.T) {
	for _, tc := range []struct {
		name    string
		frame   []byte
		invalid int64
	}{
		{name: "silent"},
		{name: "partial_header", frame: []byte{0}, invalid: 1},
		{name: "partial_payload", frame: []byte{0, 0, 0, 2, '{'}, invalid: 1},
		{name: "malformed", frame: []byte{0, 0, 0, 2, '{', '}'}, invalid: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			srv, st, handler := newTestServer(t)
			srv.cfg.AllowNoToken = true
			server, peer := net.Pipe()
			defer peer.Close() //nolint:errcheck
			conn := &idleDeadlineConn{server, make(chan time.Time, 4)}
			done := make(chan struct{})
			srv.startWorker(func() { defer close(done); srv.handleControlConn(handler, conn, st) })
			if err := protocol.WriteControlMessage(peer, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{Username: "idle-accounting", MediaCiphers: []string{"aes128"}}}); err != nil {
				t.Fatal(err)
			}
			response, err := protocol.ReadControlMessage(peer)
			if err != nil || response.AuthResponse == nil {
				t.Fatal("authentication failed", err)
			}
			select {
			case <-conn.deadlines:
			case <-time.After(time.Second):
				t.Fatal("missing idle deadline")
			}
			if len(tc.frame) != 0 {
				if _, err := peer.Write(tc.frame); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name != "malformed" {
				if err := server.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("handler survived expiry/rejection")
			}
			if got := srv.metrics.ControlInvalidMessages.Load(); got != tc.invalid {
				t.Errorf("invalid messages = %d, want %d", got, tc.invalid)
			}
			if got := int64(strings.Count(logs.String(), "level=WARN")); got != tc.invalid {
				t.Errorf("warnings = %d, want %d; logs=%s", got, tc.invalid, logs.String())
			}
		})
	}
}

func TestShutdownUnblocksAuthenticatedControlRead(t *testing.T) {
	srv, st, handler := newTestServer(t)
	srv.cfg.AllowNoToken = true
	server, peer := net.Pipe()
	defer peer.Close() //nolint:errcheck
	done := make(chan struct{})
	srv.startWorker(func() { defer close(done); srv.handleControlConn(handler, server, st) })
	if err := protocol.WriteControlMessage(peer, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{Username: "shutdown-idle", MediaCiphers: []string{"aes128"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.ReadControlMessage(peer); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { srv.Shutdown(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown waited on authenticated read")
	}
	<-done
	if srv.metrics.ActiveConnections.Load() != 0 {
		t.Fatal("active connection leaked")
	}
}
