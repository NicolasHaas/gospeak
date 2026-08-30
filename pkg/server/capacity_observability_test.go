package server

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPreAuthCapacityMetricsTrackOccupancyAndRejections(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxPreAuthConnections = 2
	srv := New(cfg, Dependencies{})

	newConn := func(address string) remoteAddrConn {
		server, client := net.Pipe()
		t.Cleanup(func() {
			_ = server.Close()
			_ = client.Close()
		})
		return remoteAddrConn{Conn: server, remote: testAddr(address)}
	}

	first := newConn("192.0.2.10:41000")
	second := newConn("192.0.2.10:41001")
	if !srv.beginPreAuth(first, preAuthControl) || !srv.beginPreAuth(second, preAuthControl) {
		t.Fatal("connections below global pre-auth limit were rejected")
	}

	snapshot := srv.preAuthCapacitySnapshot()
	if got := snapshot.current[preAuthControl]; got != 2 {
		t.Fatalf("current control occupancy = %d, want 2", got)
	}
	if got := snapshot.maxBySource[preAuthControl]; got != 2 {
		t.Fatalf("busiest control source occupancy = %d, want 2", got)
	}
	if got := srv.metrics.PreAuthControlHighWater.Load(); got != 2 {
		t.Fatalf("control occupancy high-water mark = %d, want 2", got)
	}
	if got := srv.metrics.PreAuthControlSourceHighWater.Load(); got != 2 {
		t.Fatalf("control source high-water mark = %d, want 2", got)
	}

	globalRejected := newConn("198.51.100.20:42000")
	if srv.admitPreAuthConn(globalRejected, preAuthControl) {
		t.Fatal("connection above global pre-auth limit was accepted")
	}
	if got := srv.metrics.PreAuthControlGlobalRejections.Load(); got != 1 {
		t.Fatalf("control global pre-auth rejections = %d, want 1", got)
	}

	srv.forgetAcceptedConn(first)
	snapshot = srv.preAuthCapacitySnapshot()
	if got := snapshot.current[preAuthControl]; got != 1 {
		t.Fatalf("control pre-auth occupancy after release = %d, want 1", got)
	}
	if got := snapshot.maxBySource[preAuthControl]; got != 1 {
		t.Fatalf("control busiest-source occupancy after release = %d, want 1", got)
	}
	if got := srv.metrics.PreAuthControlHighWater.Load(); got != 2 {
		t.Fatalf("control occupancy high-water mark after release = %d, want 2", got)
	}
}

func TestPreAuthRejectionCounterMatrix(t *testing.T) {
	tests := []struct {
		name    string
		plane   preAuthPlane
		reason  string
		counter func(*Metrics) int64
	}{
		{name: "control global", plane: preAuthControl, reason: "global", counter: func(m *Metrics) int64 { return m.PreAuthControlGlobalRejections.Load() }},
		{name: "control source", plane: preAuthControl, reason: "source", counter: func(m *Metrics) int64 { return m.PreAuthControlSourceRejections.Load() }},
		{name: "screen global", plane: preAuthScreen, reason: "global", counter: func(m *Metrics) int64 { return m.PreAuthScreenGlobalRejections.Load() }},
		{name: "screen source", plane: preAuthScreen, reason: "source", counter: func(m *Metrics) int64 { return m.PreAuthScreenSourceRejections.Load() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			fill := 1
			if tt.reason == "global" {
				cfg.MaxPreAuthConnections = 1
			} else {
				cfg.MaxPreAuthConnections = maxPreAuthConnectionsPerIP + 1
				fill = maxPreAuthConnectionsPerIP
			}
			srv := New(cfg, Dependencies{})

			newConn := func(address string) remoteAddrConn {
				server, client := net.Pipe()
				t.Cleanup(func() {
					_ = server.Close()
					_ = client.Close()
				})
				return remoteAddrConn{Conn: server, remote: testAddr(address)}
			}
			for i := range fill {
				conn := newConn(fmt.Sprintf("192.0.2.10:%d", 41000+i))
				if !srv.beginPreAuth(conn, tt.plane) {
					t.Fatalf("setup connection %d rejected", i+1)
				}
			}
			rejectedAddress := "192.0.2.10:49999"
			if tt.reason == "global" {
				rejectedAddress = "198.51.100.20:49999"
			}
			if srv.admitPreAuthConn(newConn(rejectedAddress), tt.plane) {
				t.Fatalf("%s rejection was accepted", tt.reason)
			}
			if got := tt.counter(srv.metrics); got != 1 {
				t.Fatalf("%s/%s rejection counter = %d, want 1", tt.plane, tt.reason, got)
			}
		})
	}
}

func TestPreAuthSourceLimitIsCountedAndLoggedWithoutLogFlooding(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxPreAuthConnections = maxPreAuthConnectionsPerIP + 2
	srv := New(cfg, Dependencies{})

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	now := time.Unix(1_700_000_000, 0)
	srv.capacityLogNow = func() time.Time { return now }

	for i := range maxPreAuthConnectionsPerIP {
		server, client := net.Pipe()
		t.Cleanup(func() {
			_ = server.Close()
			_ = client.Close()
		})
		conn := remoteAddrConn{Conn: server, remote: testAddr(fmt.Sprintf("192.0.2.10:%d", 41000+i))}
		if !srv.beginPreAuth(conn, preAuthScreen) {
			t.Fatalf("screen pre-auth connection %d below source limit was rejected", i+1)
		}
	}

	for range 2 {
		server, client := net.Pipe()
		t.Cleanup(func() {
			_ = server.Close()
			_ = client.Close()
		})
		rejected := remoteAddrConn{Conn: server, remote: testAddr("192.0.2.10:49999")}
		if srv.admitPreAuthConn(rejected, preAuthScreen) {
			t.Fatal("screen pre-auth connection above source limit was accepted")
		}
	}

	if got := srv.metrics.PreAuthScreenSourceRejections.Load(); got != 2 {
		t.Fatalf("screen source pre-auth rejections = %d, want 2", got)
	}
	if got := strings.Count(logs.String(), "capacity limit reached"); got != 1 {
		t.Fatalf("capacity limit log entries = %d, want 1; logs=%q", got, logs.String())
	}
	for _, field := range []string{"kind=preauth", "plane=screen", "reason=source", "current=8", "limit=8"} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("capacity log missing %q: %s", field, logs.String())
		}
	}

	now = now.Add(capacityLogInterval)
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	rejected := remoteAddrConn{Conn: server, remote: testAddr("192.0.2.10:49998")}
	if srv.admitPreAuthConn(rejected, preAuthScreen) {
		t.Fatal("screen pre-auth connection above source limit was accepted after log interval")
	}
	if !strings.Contains(logs.String(), "suppressed=1") {
		t.Fatalf("next capacity log did not report suppressed events: %s", logs.String())
	}
}

func TestLimiterUsageSnapshotsIgnoreSourceIdentity(t *testing.T) {
	auth := newAuthRateLimiter(3, time.Minute)
	if !auth.Allow("192.0.2.10") {
		t.Fatal("first auth reservation rejected")
	}
	auth.RecordFailure("192.0.2.10")
	if !auth.Allow("192.0.2.10") {
		t.Fatal("second auth reservation rejected")
	}
	if !auth.Allow("198.51.100.20") {
		t.Fatal("independent auth reservation rejected")
	}
	authSnapshot := auth.usageSnapshot()
	if authSnapshot.maxUsage != 2 || authSnapshot.activeSources != 2 {
		t.Fatalf("auth usage snapshot = %+v, want max 2 across 2 sources", authSnapshot)
	}

	provision := newAccountProvisionLimiter(4, time.Hour)
	for range 3 {
		if !provision.Reserve("192.0.2.10") {
			t.Fatal("provisioning reservation rejected")
		}
		provision.Commit("192.0.2.10")
	}
	provisionSnapshot := provision.usageSnapshot()
	if provisionSnapshot.maxUsage != 3 || provisionSnapshot.activeSources != 1 {
		t.Fatalf("provisioning usage snapshot = %+v, want max 3 across 1 source", provisionSnapshot)
	}
}

func TestAuthenticationLimitRejectionIsCounted(t *testing.T) {
	srv := New(DefaultConfig(), Dependencies{})
	srv.authLimiter = newAuthRateLimiter(1, time.Minute)
	const key = "192.0.2.10"
	allowed, _, _, _ := srv.allowAuthWithObservability(key)
	if !allowed {
		t.Fatal("first authentication reservation rejected")
	}
	srv.authLimiter.RecordFailure(key)
	allowed, reason, current, limit := srv.allowAuthWithObservability(key)
	if allowed {
		t.Fatal("authentication above source budget was accepted")
	}
	if reason != "source" || current != 1 || limit != 1 {
		t.Fatalf("authentication rejection details = (%q, %d, %d), want (source, 1, 1)", reason, current, limit)
	}
	srv.recordAuthRateLimitRejection("192.0.2.10:41000", reason, current, limit)
	if got := srv.metrics.AuthRateLimitSourceRejections.Load(); got != 1 {
		t.Fatalf("authentication rate-limit rejections = %d, want 1", got)
	}
	if got := srv.metrics.AuthRateLimitSourceHighWater.Load(); got != 1 {
		t.Fatalf("authentication source high-water mark = %d, want 1", got)
	}
}

func TestBoundedLimiterRejectionReasonsAreCounted(t *testing.T) {
	t.Run("authentication tracker capacity", func(t *testing.T) {
		srv := New(DefaultConfig(), Dependencies{})
		srv.authLimiter = newAuthRateLimiter(3, time.Minute)
		srv.authLimiter.maxEntries = 1
		if allowed, _, _, _ := srv.authLimiter.AllowWithDetails("192.0.2.10"); !allowed {
			t.Fatal("first authentication source rejected")
		}
		allowed, reason, current, limit := srv.authLimiter.AllowWithDetails("198.51.100.20")
		if allowed || reason != "tracker_capacity" || current != 1 || limit != 1 {
			t.Fatalf("tracker rejection = (%t, %q, %d, %d), want (false, tracker_capacity, 1, 1)", allowed, reason, current, limit)
		}
		srv.recordAuthRateLimitRejection("198.51.100.20:41000", reason, current, limit)
		if got := srv.metrics.AuthRateLimitTrackerRejections.Load(); got != 1 {
			t.Fatalf("authentication tracker rejections = %d, want 1", got)
		}
	})

	t.Run("provisioning window transition", func(t *testing.T) {
		srv := New(DefaultConfig(), Dependencies{})
		now := time.Unix(1_700_000_000, 0)
		srv.accountProvisionLimiter = newAccountProvisionLimiter(2, time.Hour)
		srv.accountProvisionLimiter.now = func() time.Time { return now }
		if reserved, _, _, _ := srv.accountProvisionLimiter.ReserveWithDetails("192.0.2.10"); !reserved {
			t.Fatal("first provisioning reservation rejected")
		}
		now = now.Add(time.Hour)
		reserved, reason, current, limit := srv.accountProvisionLimiter.ReserveWithDetails("192.0.2.10")
		if reserved || reason != "window_transition" || current != 1 || limit != 2 {
			t.Fatalf("window rejection = (%t, %q, %d, %d), want (false, window_transition, 1, 2)", reserved, reason, current, limit)
		}
		srv.recordAccountProvisionRejection("192.0.2.10:41000", reason, current, limit)
		if got := srv.metrics.AccountProvisionWindowRejections.Load(); got != 1 {
			t.Fatalf("account provisioning window rejections = %d, want 1", got)
		}
	})
}

func TestMetricsEndpointExposesCapacityWithoutSourceLabels(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxPreAuthConnections = 2
	srv := New(cfg, Dependencies{})
	srv.authLimiter = newAuthRateLimiter(3, time.Minute)
	srv.authLimiter.maxEntries = 7
	srv.accountProvisionLimiter = newAccountProvisionLimiter(4, time.Hour)
	srv.accountProvisionLimiter.maxEntries = 9

	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	conn := remoteAddrConn{Conn: server, remote: testAddr("192.0.2.10:41000")}
	if !srv.beginPreAuth(conn, preAuthControl) {
		t.Fatal("pre-auth connection rejected")
	}

	authAllowed, _, _, _ := srv.allowAuthWithObservability("192.0.2.10")
	if !authAllowed {
		t.Fatal("auth reservation rejected")
	}
	srv.authLimiter.RecordFailure("192.0.2.10")
	provisionReserved, _, _, _ := srv.reserveAccountWithObservability("192.0.2.10")
	if !provisionReserved {
		t.Fatal("provisioning reservation rejected")
	}
	srv.accountProvisionLimiter.Commit("192.0.2.10")

	recorder := httptest.NewRecorder()
	srv.handleMetrics(recorder, httptest.NewRequestWithContext(context.Background(), "GET", "/metrics", nil))
	body := recorder.Body.String()
	for _, line := range []string{
		`gospeak_preauth_connections{plane="control"} 1`,
		`gospeak_preauth_connections_high_water{plane="control"} 1`,
		`gospeak_preauth_connection_limit{plane="control"} 2`,
		`gospeak_preauth_source_max_connections{plane="control"} 1`,
		`gospeak_preauth_source_max_connections_high_water{plane="control"} 1`,
		`gospeak_preauth_source_connection_limit{plane="control"} 8`,
		`gospeak_preauth_rejections_total{plane="control",reason="global"} 0`,
		`gospeak_auth_rate_limit_source_max_usage 1`,
		`gospeak_auth_rate_limit_source_high_water_usage 1`,
		`gospeak_auth_rate_limit_source_limit 3`,
		`gospeak_auth_rate_limit_tracker_limit 7`,
		`gospeak_auth_rate_limit_rejections_total{reason="source"} 0`,
		`gospeak_auth_rate_limit_rejections_total{reason="tracker_capacity"} 0`,
		`gospeak_auth_rate_limit_rejections_total{reason="window_transition"} 0`,
		`gospeak_account_provisioning_source_max_usage 1`,
		`gospeak_account_provisioning_source_high_water_usage 1`,
		`gospeak_account_provisioning_source_limit 4`,
		`gospeak_account_provisioning_tracker_limit 9`,
		`gospeak_account_provisioning_rejections_total{reason="source"} 0`,
		`gospeak_account_provisioning_rejections_total{reason="tracker_capacity"} 0`,
		`gospeak_account_provisioning_rejections_total{reason="window_transition"} 0`,
	} {
		if !strings.Contains(body, line+"\n") {
			t.Fatalf("metrics output missing %q:\n%s", line, body)
		}
	}
	if strings.Contains(body, "192.0.2.10") {
		t.Fatalf("metrics output exposed source identity:\n%s", body)
	}
}
