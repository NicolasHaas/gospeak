package server

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func TestAuthSourceGroups(t *testing.T) {
	for _, tc := range []struct{ addr, want string }{
		{"[2001:db8:1:2::1]:1", "2001:db8:1:2::/64"},
		{"[2001:db8:1:2:ffff::2]:2", "2001:db8:1:2::/64"},
		{"[2001:db8:1:3::1]:1", "2001:db8:1:3::/64"},
		{"[::ffff:192.0.2.1]:1", "192.0.2.1"},
		{"192.0.2.1:2", "192.0.2.1"},
	} {
		tcp, err := net.ResolveTCPAddr("tcp", tc.addr)
		if err != nil {
			t.Fatal(err)
		}
		for _, addr := range []net.Addr{tcp, testAddr(tc.addr)} {
			if got := authRateLimitKey(addr); got != tc.want {
				t.Errorf("%s: %s, want %s", addr, got, tc.want)
			}
		}
	}
	limiter := newAuthRateLimiter(1, time.Minute)
	first := authRateLimitKey(testAddr("[2001:db8:1:2::1]:1"))
	second := authRateLimitKey(testAddr("[2001:db8:1:2::2]:2"))
	if !limiter.Allow(first) {
		t.Fatal("first denied")
	}
	limiter.RecordFailure(first)
	if limiter.Allow(second) {
		t.Fatal("rotated IPv6 address reset failure budget")
	}
	provision := newAccountProvisionLimiter(1, time.Hour)
	if !provision.Reserve(first) {
		t.Fatal("initial provision denied")
	}
	provision.Commit(first)
	if provision.Reserve(second) {
		t.Fatal("rotated IPv6 address reset provisioning budget")
	}
	srv := New(DefaultConfig(), Dependencies{})
	for i := range maxPreAuthConnectionsPerIP + 1 {
		conn := remoteAddrConn{Conn: newCloseTrackingConn(), remote: testAddr(fmt.Sprintf("[2001:db8:1:2::%x]:1", i+1))}
		allowed := srv.beginPreAuth(conn, preAuthControl)
		if allowed {
			defer srv.forgetAcceptedConn(conn)
		}
		if allowed != (i < maxPreAuthConnectionsPerIP) {
			t.Fatalf("rotated address admission %d = %t", i, allowed)
		}
	}
	neighbor := remoteAddrConn{Conn: newCloseTrackingConn(), remote: testAddr("[2001:db8:1:3::1]:1")}
	if !srv.beginPreAuth(neighbor, preAuthControl) {
		t.Fatal("neighbor /64 lost its admission budget")
	}
	defer srv.forgetAcceptedConn(neighbor)
}
