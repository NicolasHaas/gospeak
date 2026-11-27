package server

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func awaitIdleDeadline(t *testing.T, conn *frameDeadlineConn) time.Time {
	t.Helper()
	for {
		select {
		case at := <-conn.deadlines:
			if time.Until(at) > time.Minute {
				return at
			}
		case <-time.After(time.Second):
			t.Fatal("missing applied idle deadline")
		}
	}
}

func awaitFrameDeadline(t *testing.T, conn *frameDeadlineConn) {
	t.Helper()
	select {
	case at := <-conn.deadlines:
		if left := time.Until(at); left <= 0 || left > 5*time.Second {
			t.Fatalf("frame completion horizon = %v, want at most 5s", left)
		}
	case <-time.After(time.Second):
		t.Fatal("missing applied frame deadline")
	}
}
func TestControlIdleDeadlineResetsOnlyForCompleteMessage(t *testing.T) {
	srv, st, handler := newTestServer(t)
	srv.cfg.AllowNoToken = true
	server, peer := newControlPipe()
	defer peer.Close() //nolint:errcheck
	conn := &frameDeadlineConn{server, make(chan time.Time, 8)}
	done := make(chan struct{})
	srv.startWorker(func() { defer close(done); srv.handleControlConn(handler, conn, st) })
	if err := protocol.WriteControlMessage(peer, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{Username: "idle", MediaCiphers: []string{"aes128"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.ReadControlMessage(peer); err != nil {
		t.Fatal(err)
	}
	at := awaitIdleDeadline(t, conn)
	if left := time.Until(at); left < 4*time.Minute || left > 5*time.Minute {
		t.Fatal("wrong idle horizon", left)
	}
	if err := protocol.WriteControlMessage(peer, &pb.ControlMessage{Ping: &pb.Ping{Timestamp: 123}}); err != nil {
		t.Fatal(err)
	}
	response, err := protocol.ReadControlMessage(peer)
	if err != nil || response.Pong == nil {
		t.Fatal("ping failed", err)
	}
	awaitFrameDeadline(t, conn)
	awaitIdleDeadline(t, conn)
	// A partial frame must install its completion deadline before we expire
	// it, but cannot renew the authenticated idle deadline.
	if _, err := peer.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	awaitFrameDeadline(t, conn)
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
			server, peer := newControlPipe()
			defer peer.Close() //nolint:errcheck
			conn := &frameDeadlineConn{server, make(chan time.Time, 8)}
			done := make(chan struct{})
			srv.startWorker(func() { defer close(done); srv.handleControlConn(handler, conn, st) })
			if err := protocol.WriteControlMessage(peer, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{Username: "idle-accounting", MediaCiphers: []string{"aes128"}}}); err != nil {
				t.Fatal(err)
			}
			response, err := protocol.ReadControlMessage(peer)
			if err != nil || response.AuthResponse == nil {
				t.Fatal("authentication failed", err)
			}
			awaitIdleDeadline(t, conn)
			if len(tc.frame) != 0 {
				if _, err := peer.Write(tc.frame); err != nil {
					t.Fatal(err)
				}
			}
			if len(tc.frame) != 0 && tc.name != "malformed" {
				awaitFrameDeadline(t, conn)
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
	server, peer := newControlPipe()
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
	requireSourceCapacity(t, srv.sessions, 0, 0, 0)
}
