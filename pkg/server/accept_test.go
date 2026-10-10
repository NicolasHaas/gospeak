package server

import (
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

type acceptTestListener struct {
	accept func() (net.Conn, error)
}

func (l acceptTestListener) Accept() (net.Conn, error) { return l.accept() }
func (acceptTestListener) Close() error                { return nil }
func (acceptTestListener) Addr() net.Addr              { return &net.TCPAddr{} }

func TestAcceptConnBackoffAndReset(t *testing.T) {
	s := New(Config{}, Dependencies{})
	defer s.Shutdown()
	server, peer := net.Pipe()
	defer server.Close()
	defer peer.Close()
	var calls []time.Time
	ln := acceptTestListener{accept: func() (net.Conn, error) {
		calls = append(calls, time.Now())
		if len(calls)%3 == 0 {
			return server, nil
		}
		return nil, syscall.EMFILE
	}}
	for range 2 {
		if conn, err := s.acceptConn(ln, preAuthControl); err != nil || conn != server {
			t.Fatalf("accept = %v, %v", conn, err)
		}
	}
	for _, start := range []int{0, 3} {
		for i, minimum := range []time.Duration{5 * time.Millisecond, 10 * time.Millisecond} {
			if elapsed := calls[start+i+1].Sub(calls[start+i]); elapsed < minimum {
				t.Fatalf("retry %d elapsed=%v want>=%v", i, elapsed, minimum)
			}
		}
	}
}

func TestAcceptConnClosedAndCancellation(t *testing.T) {
	s := New(Config{}, Dependencies{})
	defer s.Shutdown()
	calls := 0
	ln := acceptTestListener{accept: func() (net.Conn, error) {
		calls++
		return nil, &net.OpError{Op: "accept", Err: net.ErrClosed}
	}}
	if _, err := s.acceptConn(ln, preAuthScreen); !errors.Is(err, net.ErrClosed) || calls != 1 {
		t.Fatalf("closed accept: calls=%d err=%v", calls, err)
	}
	entered := make(chan struct{})
	ln.accept = func() (net.Conn, error) {
		close(entered)
		return nil, syscall.EMFILE
	}
	done := make(chan error, 1)
	go func() { _, err := s.acceptConn(ln, preAuthScreen); done <- err }()
	<-entered
	s.cancel()
	select {
	case err := <-done:
		if !errors.Is(err, s.ctx.Err()) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt retry")
	}
}
