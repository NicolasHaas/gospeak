package server

import (
	"net"
	"testing"
	"time"
)

type testAddr string

func (a testAddr) Network() string { return "tcp" }
func (a testAddr) String() string  { return string(a) }

type remoteAddrConn struct {
	net.Conn
	remote net.Addr
}

func (c remoteAddrConn) RemoteAddr() net.Addr { return c.remote }

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

func TestAuthRateLimiterPreservesFailuresAfterSuccessfulAuthentication(t *testing.T) {
	limiter := newAuthRateLimiter(2, time.Minute)
	const key = "192.0.2.10"

	if !limiter.Allow(key) {
		t.Fatal("initial Allow = false, want true")
	}
	if !limiter.Allow(key) {
		t.Fatal("concurrent Allow = false, want true")
	}
	limiter.RecordFailure(key)
	limiter.RecordSuccess(key)
	if !limiter.Allow(key) {
		t.Fatal("remaining authentication attempt was not available")
	}
	limiter.RecordFailure(key)
	if limiter.Allow(key) {
		t.Fatal("successful authentication erased the preceding failure budget")
	}
}

func TestAuthRateLimiterDoesNotMixExpiredInFlightWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newAuthRateLimiter(2, time.Minute)
	limiter.now = func() time.Time { return now }
	const key = "192.0.2.10"

	if !limiter.Allow(key) {
		t.Fatal("initial reservation rejected")
	}
	now = now.Add(time.Minute + time.Nanosecond)
	if limiter.Allow(key) {
		t.Fatal("new window replaced an expired entry with an in-flight reservation")
	}
	limiter.RecordSuccess(key)
	if !limiter.Allow(key) {
		t.Fatal("new window remained blocked after the stale reservation finished")
	}
}

func TestAuthRateLimiterDoesNotPruneExpiredInFlightEntryAtCapacity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newAuthRateLimiter(2, time.Minute)
	limiter.maxEntries = 1
	limiter.now = func() time.Time { return now }

	if !limiter.Allow("old") {
		t.Fatal("initial reservation rejected")
	}
	now = now.Add(time.Minute + time.Nanosecond)
	if limiter.Allow("new") {
		t.Fatal("capacity pruning removed an expired in-flight entry")
	}
	limiter.RecordSuccess("old")
	if !limiter.Allow("new") {
		t.Fatal("capacity remained blocked after the old reservation finished")
	}
}

func TestAccountProvisionLimiterBoundsSuccessesAndReleasesFailures(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newAccountProvisionLimiter(2, time.Hour)
	limiter.now = func() time.Time { return now }
	const key = "192.0.2.10"

	if !limiter.Reserve(key) {
		t.Fatal("first reservation rejected")
	}
	limiter.Release(key)
	for i := 0; i < 2; i++ {
		if !limiter.Reserve(key) {
			t.Fatalf("successful reservation %d rejected", i+1)
		}
		limiter.Commit(key)
	}
	if limiter.Reserve(key) {
		t.Fatal("reservation above successful provisioning budget accepted")
	}
	if !limiter.Reserve("198.51.100.20") {
		t.Fatal("one IP exhausted another IP's provisioning budget")
	}

	now = now.Add(time.Hour + time.Nanosecond)
	if !limiter.Reserve(key) {
		t.Fatal("expired provisioning budget was not reset")
	}
}

func TestAccountProvisionLimiterDoesNotMixExpiredInFlightWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newAccountProvisionLimiter(2, time.Hour)
	limiter.now = func() time.Time { return now }
	const key = "192.0.2.10"

	if !limiter.Reserve(key) {
		t.Fatal("initial reservation rejected")
	}
	now = now.Add(time.Hour + time.Nanosecond)
	if limiter.Reserve(key) {
		t.Fatal("new window replaced an expired entry with an in-flight reservation")
	}
	limiter.Release(key)
	if !limiter.Reserve(key) {
		t.Fatal("new window remained blocked after the stale reservation finished")
	}
}

func TestAccountProvisionLimiterDoesNotPruneExpiredInFlightEntryAtCapacity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newAccountProvisionLimiter(2, time.Hour)
	limiter.maxEntries = 1
	limiter.now = func() time.Time { return now }

	if !limiter.Reserve("old") {
		t.Fatal("initial reservation rejected")
	}
	now = now.Add(time.Hour + time.Nanosecond)
	if limiter.Reserve("new") {
		t.Fatal("capacity pruning removed an expired in-flight entry")
	}
	limiter.Release("old")
	if !limiter.Reserve("new") {
		t.Fatal("capacity remained blocked after the old reservation finished")
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

func TestPreAuthAdmissionPreservesCapacityAcrossClientIPs(t *testing.T) {
	for _, plane := range []preAuthPlane{preAuthControl, preAuthScreen} {
		t.Run(string(plane), func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.MaxPreAuthConnections = 10
			srv := New(cfg, Dependencies{})
			t.Cleanup(srv.Shutdown)

			connections := make([]net.Conn, 0, cfg.MaxPreAuthConnections*2)
			admit := func(ip string) bool {
				serverConn, clientConn := net.Pipe()
				connections = append(connections, clientConn)
				conn := remoteAddrConn{Conn: serverConn, remote: testAddr(net.JoinHostPort(ip, "41000"))}
				if !srv.beginPreAuth(conn, plane) {
					_ = serverConn.Close()
					return false
				}
				connections = append(connections, conn)
				return true
			}
			t.Cleanup(func() {
				for _, conn := range connections {
					_ = conn.Close()
				}
			})

			attackerAccepted := 0
			for range 9 {
				if admit("192.0.2.10") {
					attackerAccepted++
				}
			}
			if attackerAccepted != 8 {
				t.Errorf("connections admitted for one IP = %d, want 8", attackerAccepted)
			}
			if !admit("198.51.100.20") {
				t.Error("connection from a second IP rejected despite reserved global capacity")
			}
			if !admit("203.0.113.30") {
				t.Error("connection up to the global capacity rejected")
			}
			if admit("203.0.113.31") {
				t.Error("connection above the global capacity accepted")
			}
		})
	}
}

func TestPreAuthPerIPCapacityIsReleased(t *testing.T) {
	cfg := DefaultConfig()
	srv := New(cfg, Dependencies{})
	t.Cleanup(srv.Shutdown)

	connections := make([]net.Conn, 0, maxPreAuthConnectionsPerIP*2+2)
	newConn := func() remoteAddrConn {
		serverConn, clientConn := net.Pipe()
		connections = append(connections, clientConn)
		return remoteAddrConn{Conn: serverConn, remote: testAddr("192.0.2.10:41000")}
	}
	t.Cleanup(func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	})

	admitted := make([]remoteAddrConn, 0, maxPreAuthConnectionsPerIP)
	for range maxPreAuthConnectionsPerIP {
		conn := newConn()
		if !srv.beginPreAuth(conn, preAuthControl) {
			t.Fatal("connection below the per-IP capacity rejected")
		}
		connections = append(connections, conn)
		admitted = append(admitted, conn)
	}
	rejected := newConn()
	if srv.beginPreAuth(rejected, preAuthControl) {
		t.Fatal("connection above the per-IP capacity accepted")
	}
	_ = rejected.Close()

	srv.finishPreAuth(admitted[0])
	replacement := newConn()
	if !srv.beginPreAuth(replacement, preAuthControl) {
		t.Fatal("authentication did not release per-IP pre-auth capacity")
	}
	connections = append(connections, replacement)
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
