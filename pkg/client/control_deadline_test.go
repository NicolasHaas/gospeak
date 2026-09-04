package client

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type deadlineTrackingConn struct {
	net.Conn
	mu        sync.Mutex
	deadlines []time.Time
}

func (c *deadlineTrackingConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadlines = append(c.deadlines, deadline)
	c.mu.Unlock()
	return c.Conn.SetDeadline(deadline)
}

func (c *deadlineTrackingConn) recordedDeadlines() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.deadlines...)
}

func TestControlClientAuthenticateBoundsStalledResponseRead(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	requestRead := make(chan struct{})
	go func() {
		if _, err := protocol.ReadControlMessage(serverConn); err == nil {
			close(requestRead)
		}
	}()

	client := &ControlClient{conn: clientConn}
	started := time.Now()
	result := make(chan error, 1)
	go func() {
		_, err := client.Authenticate("token", "alice")
		result <- err
	}()

	select {
	case <-requestRead:
	case <-time.After(time.Second):
		t.Fatal("server did not receive authentication request")
	}

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Authenticate() returned nil error for stalled response")
		}
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("Authenticate() error = %v, want network timeout", err)
		}
		if elapsed := time.Since(started); elapsed < connectTimeout-time.Second || elapsed > connectTimeout+time.Second {
			t.Fatalf("Authenticate() returned after %v, want approximately %v", elapsed, connectTimeout)
		}
	case <-time.After(connectTimeout + time.Second):
		t.Fatal("Authenticate() remained blocked beyond its network timeout")
	}
}

func TestControlClientAuthenticateBoundsStalledRequestWrite(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	client := &ControlClient{conn: clientConn}
	started := time.Now()
	result := make(chan error, 1)
	go func() {
		_, err := client.Authenticate("token", "alice")
		result <- err
	}()

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Authenticate() returned nil error for stalled request")
		}
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("Authenticate() error = %v, want network timeout", err)
		}
		if elapsed := time.Since(started); elapsed < connectTimeout-time.Second || elapsed > connectTimeout+time.Second {
			t.Fatalf("Authenticate() returned after %v, want approximately %v", elapsed, connectTimeout)
		}
	case <-time.After(connectTimeout + time.Second):
		t.Fatal("Authenticate() write remained blocked beyond its network timeout")
	}
}

func TestControlClientAuthenticateClearsDeadlineAfterSuccess(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	go func() {
		if _, err := protocol.ReadControlMessage(serverConn); err != nil {
			return
		}
		_ = protocol.WriteControlMessage(serverConn, &pb.ControlMessage{
			AuthResponse: &pb.AuthResponse{SessionID: 1, Username: "alice"},
		})
	}()

	tracked := &deadlineTrackingConn{Conn: clientConn}
	client := &ControlClient{conn: tracked}
	started := time.Now()
	if _, err := client.Authenticate("token", "alice"); err != nil {
		t.Fatal(err)
	}

	deadlines := tracked.recordedDeadlines()
	if len(deadlines) != 2 {
		t.Fatalf("connection deadline calls = %d, want set and clear", len(deadlines))
	}
	if !deadlines[0].After(started) || deadlines[0].After(started.Add(connectTimeout+time.Second)) {
		t.Fatalf("authentication deadline = %v, started at %v", deadlines[0], started)
	}
	if !deadlines[1].IsZero() {
		t.Fatalf("cleared authentication deadline = %v, want zero", deadlines[1])
	}
}
