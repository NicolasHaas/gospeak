package server

import (
	"net"
	"testing"
	"time"
)

type testAddr string

func (a testAddr) Network() string { return "tcp" }
func (a testAddr) String() string  { return string(a) }

func TestAuthRateLimiterNormalizesSourcePorts(t *testing.T) {
	limiter := newAuthRateLimiter(2, time.Minute)
	first := authRateLimitKey(testAddr("192.0.2.10:41000"))
	second := authRateLimitKey(testAddr("192.0.2.10:41001"))

	if first != second {
		t.Fatalf("rate limit keys differ by source port: %q != %q", first, second)
	}
	for i := 0; i < 2; i++ {
		if !limiter.Allow(first) {
			t.Fatalf("Allow attempt %d = false, want true", i+1)
		}
		limiter.RecordFailure(first)
	}
	if limiter.Allow(second) {
		t.Fatal("Allow after two failures from the same IP = true, want false")
	}
}

func TestAuthRateLimiterPrunesExpiredEntriesAndStaysBounded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newAuthRateLimiter(3, time.Minute)
	limiter.maxEntries = 2
	limiter.now = func() time.Time { return now }

	for _, key := range []string{"192.0.2.1", "192.0.2.2"} {
		if !limiter.Allow(key) {
			t.Fatalf("Allow(%q) = false, want true", key)
		}
		limiter.RecordFailure(key)
	}
	if limiter.Allow("192.0.2.3") {
		t.Fatal("Allow at entry capacity = true, want false")
	}
	if got := len(limiter.entries); got != 2 {
		t.Fatalf("entry count = %d, want 2", got)
	}

	now = now.Add(time.Minute + time.Nanosecond)
	if !limiter.Allow("192.0.2.3") {
		t.Fatal("Allow after entries expired = false, want true")
	}
	if got := len(limiter.entries); got != 1 {
		t.Fatalf("entry count after prune = %d, want 1", got)
	}
}

func TestAuthRateLimiterClearsSuccessfulAuthentication(t *testing.T) {
	limiter := newAuthRateLimiter(1, time.Minute)
	const key = "192.0.2.10"

	if !limiter.Allow(key) {
		t.Fatal("initial Allow = false, want true")
	}
	limiter.RecordFailure(key)
	if limiter.Allow(key) {
		t.Fatal("Allow after failure = true, want false")
	}

	limiter.Reset(key)
	if !limiter.Allow(key) {
		t.Fatal("Allow after Reset = false, want true")
	}
}

func TestSilentScreenConnectionTimesOut(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PreAuthTimeout = 20 * time.Millisecond
	srv := New(cfg, Dependencies{})
	serverConn, clientConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()

	done := make(chan struct{})
	go func() {
		srv.handleScreenConn(serverConn)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		_ = clientConn.Close()
		<-done
		t.Fatal("silent screen connection did not time out")
	}
}

func TestPreAuthConnectionsAreBoundedAndClosedOnShutdown(t *testing.T) {
	for _, plane := range []preAuthPlane{preAuthControl, preAuthScreen} {
		t.Run(string(plane), func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.MaxPreAuthConnections = 1
			srv := New(cfg, Dependencies{})
			firstServer, firstClient := net.Pipe()
			defer func() { _ = firstClient.Close() }()
			secondServer, secondClient := net.Pipe()
			defer func() { _ = secondServer.Close() }()
			defer func() { _ = secondClient.Close() }()

			if !srv.beginPreAuth(firstServer, plane) {
				t.Fatal("first pre-auth connection rejected")
			}
			if srv.beginPreAuth(secondServer, plane) {
				t.Fatal("second pre-auth connection accepted above configured limit")
			}

			srv.Shutdown()
			_ = firstClient.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := firstClient.Read(make([]byte, 1)); err == nil {
				t.Fatal("tracked pre-auth connection remained open after Shutdown")
			}
		})
	}
}

func TestAuthenticatedConnectionReleasesPreAuthSlotAndClosesOnShutdown(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxPreAuthConnections = 1
	srv := New(cfg, Dependencies{})
	firstServer, firstClient := net.Pipe()
	defer func() { _ = firstClient.Close() }()
	secondServer, secondClient := net.Pipe()
	defer func() { _ = secondClient.Close() }()

	if !srv.beginPreAuth(firstServer, preAuthControl) {
		t.Fatal("first pre-auth connection rejected")
	}
	srv.finishPreAuth(firstServer)
	if !srv.beginPreAuth(secondServer, preAuthControl) {
		t.Fatal("authenticated connection did not release its pre-auth slot")
	}

	srv.Shutdown()
	for i, client := range []net.Conn{firstClient, secondClient} {
		_ = client.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := client.Read(make([]byte, 1)); err == nil {
			t.Fatalf("tracked connection %d remained open after Shutdown", i+1)
		}
	}
}
