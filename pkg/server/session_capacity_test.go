package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestSessionManagerEnforcesPerUserLimitAtomically(t *testing.T) {
	const attempts = 32
	manager := NewSessionManagerWithLimits(64, 2)
	start := make(chan struct{})
	var admitted atomic.Int64
	var wg sync.WaitGroup

	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := manager.Create(42, "alice", model.RoleUser)
			switch {
			case err == nil:
				admitted.Add(1)
			case errors.Is(err, ErrUserSessionLimitReached):
			default:
				t.Errorf("Create() error = %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := admitted.Load(); got != 2 {
		t.Fatalf("admitted sessions = %d, want 2", got)
	}
	if got := manager.Count(); got != 2 {
		t.Fatalf("active sessions = %d, want 2", got)
	}
}

func TestSessionManagerEnforcesGlobalLimitAndReleasesCapacity(t *testing.T) {
	manager := NewSessionManagerWithLimits(2, 2)
	first, err := manager.Create(1, "alice", model.RoleUser)
	if err != nil {
		t.Fatalf("Create(first): %v", err)
	}
	if _, err := manager.Create(2, "bob", model.RoleUser); err != nil {
		t.Fatalf("Create(second): %v", err)
	}
	if _, err := manager.Create(3, "carol", model.RoleUser); !errors.Is(err, ErrGlobalSessionLimitReached) {
		t.Fatalf("Create(over global limit) error = %v, want %v", err, ErrGlobalSessionLimitReached)
	}

	manager.Remove(first.ID)
	if _, err := manager.Create(3, "carol", model.RoleUser); err != nil {
		t.Fatalf("Create(after release): %v", err)
	}
	snapshot := manager.CapacitySnapshot()
	if snapshot.Active != 2 || snapshot.CapacityUsed != 2 || snapshot.CapacityHighWater != 2 || snapshot.MaxUserCapacity != 1 || snapshot.UserCapacityHighWater != 1 {
		t.Fatalf("capacity snapshot = %#v", snapshot)
	}
}

func TestServerWiresConfiguredSessionLimits(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxSessions = 1
	cfg.MaxSessionsPerUser = 1
	srv := New(cfg, Dependencies{})

	if _, err := srv.sessions.Create(1, "alice", model.RoleUser); err != nil {
		t.Fatalf("Create(first): %v", err)
	}
	if _, err := srv.sessions.Create(2, "bob", model.RoleUser); !errors.Is(err, ErrGlobalSessionLimitReached) {
		t.Fatalf("Create(second) error = %v, want %v", err, ErrGlobalSessionLimitReached)
	}
	if srv.controlUserBudgets.maxEntries < cfg.MaxSessions {
		t.Fatalf("control user tracker limit = %d, want at least session limit %d", srv.controlUserBudgets.maxEntries, cfg.MaxSessions)
	}
}

func TestSessionCapacityRejectionsAreClassifiedExactlyOnce(t *testing.T) {
	tests := []struct {
		name         string
		maxSessions  int
		maxPerUser   int
		secondUserID int64
		wantGlobal   int64
		wantUser     int64
	}{
		{name: "global", maxSessions: 1, maxPerUser: 2, secondUserID: 2, wantGlobal: 1},
		{name: "user", maxSessions: 2, maxPerUser: 1, secondUserID: 1, wantUser: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.MaxSessions = tc.maxSessions
			cfg.MaxSessionsPerUser = tc.maxPerUser
			srv := New(cfg, Dependencies{})
			if _, err := srv.sessions.Create(1, "alice", model.RoleUser); err != nil {
				t.Fatalf("Create first session: %v", err)
			}
			_, err := srv.sessions.Create(tc.secondUserID, "second", model.RoleUser)
			if err == nil {
				t.Fatal("Create above limit unexpectedly succeeded")
			}
			srv.recordSessionRejection("192.0.2.1:1234", err)
			if got := srv.metrics.SessionGlobalRejections.Load(); got != tc.wantGlobal {
				t.Fatalf("global rejections = %d, want %d", got, tc.wantGlobal)
			}
			if got := srv.metrics.SessionUserRejections.Load(); got != tc.wantUser {
				t.Fatalf("user rejections = %d, want %d", got, tc.wantUser)
			}
		})
	}
}

func TestSessionCapacityPreventsIrreversibleOpenAccountProvisioning(t *testing.T) {
	srv, st, _ := newTestServer(t)
	srv.cfg.AllowNoToken = true
	srv.sessions = NewSessionManagerWithLimits(1, 1)
	if _, err := srv.sessions.Create(1, "occupant", model.RoleUser); err != nil {
		t.Fatalf("Create occupying session: %v", err)
	}

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		srv.handleControlConn(newControlHandler(srv, st), serverConn, st)
		close(done)
	}()
	if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{
		AuthRequest: &pb.AuthRequest{Username: "must-not-persist"},
	}); err != nil {
		t.Fatalf("write auth request: %v", err)
	}
	response, err := protocol.ReadControlMessage(clientConn)
	if err != nil || response.ErrorResponse == nil {
		t.Fatalf("capacity response = %#v, error = %v", response, err)
	}
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("capacity-rejected handler did not return")
	}
	user, err := st.NonTx().GetUserByUsername("must-not-persist")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if user != nil {
		t.Fatalf("capacity-rejected user persisted: %#v", user)
	}
	if usage := srv.authLimiter.usageSnapshot(); usage.activeSources != 0 || usage.maxUsage != 0 {
		t.Fatalf("auth budget after capacity rejection = %#v", usage)
	}
	if got := strings.Count(logs.String(), "capacity limit reached"); got != 1 {
		t.Fatalf("capacity log entries = %d, want 1; logs=%q", got, logs.String())
	}
	if strings.Contains(logs.String(), "create session") {
		t.Fatalf("capacity rejection emitted unthrottled create log: %s", logs.String())
	}
}

func TestInvalidCredentialDoesNotBypassAuthenticationBudgetAtSessionCapacity(t *testing.T) {
	srv, st, _ := newTestServer(t)
	srv.sessions = NewSessionManagerWithLimits(1, 1)
	if _, err := srv.sessions.Create(1, "occupant", model.RoleUser); err != nil {
		t.Fatalf("Create occupying session: %v", err)
	}

	response := authenticateControl(t, srv, st, "attacker", "invalid-token")
	if response.ErrorResponse == nil {
		t.Fatalf("invalid credential response = %#v, want authentication error", response)
	}
	usage := srv.authLimiter.usageSnapshot()
	if usage.activeSources != 1 || usage.maxUsage != 1 {
		t.Fatalf("authentication usage = %#v, want one failed attempt", usage)
	}
	if got := srv.metrics.SessionGlobalRejections.Load(); got != 0 {
		t.Fatalf("session global rejections = %d, want invalid credential rejected first", got)
	}
}

func TestSessionCapacityRollsBackInviteProvisioning(t *testing.T) {
	srv, st, _ := newTestServer(t)
	srv.sessions = NewSessionManagerWithLimits(1, 1)
	occupant, err := srv.sessions.Create(1, "occupant", model.RoleUser)
	if err != nil {
		t.Fatalf("Create occupying session: %v", err)
	}
	const invite = "capacity-invite"
	if err := st.NonTx().CreateToken(crypto.HashToken(invite), model.RoleUser, 0, 1, 1, time.Time{}); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	response := authenticateControl(t, srv, st, "invite-user", invite)
	if response.ErrorResponse == nil {
		t.Fatalf("capacity response = %#v, want error", response)
	}
	if user, err := st.NonTx().GetUserByUsername("invite-user"); err != nil || user != nil {
		t.Fatalf("capacity-rejected invite user persisted: user=%#v err=%v", user, err)
	}

	srv.sessions.Remove(occupant.ID)
	response = authenticateControl(t, srv, st, "invite-user", invite)
	if response.AuthResponse == nil || response.AuthResponse.AutoToken == "" {
		t.Fatalf("rolled-back invite was not reusable: %#v", response)
	}
}

func TestSessionPreparationFailureRollsBackProvisioning(t *testing.T) {
	t.Run("open account", func(t *testing.T) {
		srv, st, _ := newTestServer(t)
		srv.cfg.AllowNoToken = true
		srv.sessions.issuedSessionIDs = usableSessionIDs

		response := authenticateControl(t, srv, st, "prepare-open", "")
		if response.ErrorResponse == nil {
			t.Fatalf("session preparation response = %#v, want error", response)
		}
		if user, err := st.NonTx().GetUserByUsername("prepare-open"); err != nil || user != nil {
			t.Fatalf("session-preparation-rejected open user persisted: user=%#v err=%v", user, err)
		}
	})

	t.Run("invite", func(t *testing.T) {
		srv, st, _ := newTestServer(t)
		const invite = "prepare-failure-invite"
		if err := st.NonTx().CreateToken(crypto.HashToken(invite), model.RoleUser, 0, 1, 1, time.Time{}); err != nil {
			t.Fatalf("CreateToken: %v", err)
		}
		srv.sessions.issuedSessionIDs = usableSessionIDs

		response := authenticateControl(t, srv, st, "prepare-invite", invite)
		if response.ErrorResponse == nil {
			t.Fatalf("session preparation response = %#v, want error", response)
		}
		if user, err := st.NonTx().GetUserByUsername("prepare-invite"); err != nil || user != nil {
			t.Fatalf("session-preparation-rejected invite user persisted: user=%#v err=%v", user, err)
		}

		srv.sessions.issuedSessionIDs = 0
		response = authenticateControl(t, srv, st, "prepare-invite", invite)
		if response.AuthResponse == nil || response.AuthResponse.AutoToken == "" {
			t.Fatalf("rolled-back invite was not reusable: %#v", response)
		}
	})
}

func TestPreparedSessionRetainsReservationUntilActivation(t *testing.T) {
	manager := NewSessionManagerWithLimits(1, 1)
	reservation, err := manager.Reserve(7)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	prepared, err := reservation.Prepare("prepared", model.RoleUser, 0)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if snapshot := manager.CapacitySnapshot(); snapshot.Active != 0 || snapshot.CapacityUsed != 1 {
		t.Fatalf("capacity after Prepare = %#v, want one pending claim", snapshot)
	}
	if _, ok := manager.GetSnapshot(prepared.ID); ok {
		t.Fatal("prepared session became active before activation")
	}
	activated := reservation.Activate()
	if activated == nil || activated.ID != prepared.ID {
		t.Fatalf("Activate = %#v, want prepared session %#v", activated, prepared)
	}
	if snapshot := manager.CapacitySnapshot(); snapshot.Active != 1 || snapshot.CapacityUsed != 1 {
		t.Fatalf("capacity after Activate = %#v, want one active session", snapshot)
	}
}

func TestSessionReservationReleaseRestoresCapacity(t *testing.T) {
	manager := NewSessionManagerWithLimits(1, 1)
	reservation, err := manager.Reserve(0)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := reservation.BindUser(1); err != nil {
		t.Fatalf("BindUser: %v", err)
	}
	if snapshot := manager.CapacitySnapshot(); snapshot.Active != 0 || snapshot.CapacityUsed != 1 || snapshot.MaxUserCapacity != 1 {
		t.Fatalf("pending reservation snapshot = %#v", snapshot)
	}
	reservation.Release()
	if _, err := manager.Create(2, "replacement", model.RoleUser); err != nil {
		t.Fatalf("Create after reservation release: %v", err)
	}
}

func TestSessionCapacityMetricsAreExported(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxSessions = 7
	cfg.MaxSessionsPerUser = 2
	srv := New(cfg, Dependencies{})
	if _, err := srv.sessions.Create(1, "alice", model.RoleUser); err != nil {
		t.Fatalf("Create session: %v", err)
	}
	srv.metrics.SessionUserRejections.Add(1)

	recorder := httptest.NewRecorder()
	srv.handleMetrics(recorder, httptest.NewRequestWithContext(context.Background(), "GET", "/metrics", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		"gospeak_sessions 1",
		"gospeak_session_capacity_used 1",
		"gospeak_session_capacity_high_water 1",
		"gospeak_session_limit 7",
		"gospeak_session_user_limit 2",
		`gospeak_session_rejections_total{reason="user"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}
