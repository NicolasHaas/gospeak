package server

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func authenticateControl(t *testing.T, srv *Server, st datastore.DataProviderFactory, username, token string) *pb.ControlMessage {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		srv.handleControlConn(newControlHandler(srv, st), serverConn, st)
		close(done)
	}()

	if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{
		AuthRequest: &pb.AuthRequest{Username: username, Token: token},
	}); err != nil {
		t.Fatalf("write AuthRequest: %v", err)
	}
	response, err := protocol.ReadControlMessage(clientConn)
	if err != nil {
		t.Fatalf("read auth response: %v", err)
	}
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("control handler did not return after client close")
	}
	return response
}

func expectControlRejectedBeforeAuth(t *testing.T, srv *Server, st datastore.DataProviderFactory) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		srv.handleControlConn(newControlHandler(srv, st), serverConn, st)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		_ = clientConn.Close()
		t.Fatal("control handler did not reject before authentication")
	}
	var one [1]byte
	if _, err := clientConn.Read(one[:]); err == nil {
		_ = clientConn.Close()
		t.Fatal("rejected control connection remained readable")
	}
	_ = clientConn.Close()
}

func TestInviteProvisioningRollsBackUseWhenUsernameIsTaken(t *testing.T) {
	srv, st, _ := newTestServer(t)
	if _, err := st.NonTx().CreateUser("taken", model.RoleUser); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	const invite = "single-use-invite"
	if err := st.NonTx().CreateToken(crypto.HashToken(invite), model.RoleUser, 0, 1, 1, time.Time{}); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	first := authenticateControl(t, srv, st, "taken", invite)
	if first.ErrorResponse == nil {
		t.Fatalf("taken username response = %#v, want authentication error", first)
	}
	second := authenticateControl(t, srv, st, "fresh", invite)
	if second.AuthResponse == nil {
		t.Fatalf("invite was consumed by rolled-back provisioning: %#v", second)
	}
}

func TestInviteProvisioningRollsBackPersonalTokenFailure(t *testing.T) {
	srv, st, _ := newTestServer(t)
	factory, ok := st.(*datastore.ProviderFactory)
	if !ok {
		t.Fatalf("test store type = %T, want *datastore.ProviderFactory", st)
	}
	if _, err := factory.DB.Exec(`
		CREATE TRIGGER fail_personal_token_update
		BEFORE UPDATE OF personal_token_hash ON users
		BEGIN
			SELECT RAISE(FAIL, 'forced personal token failure');
		END;
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	const invite = "rollback-invite"
	if err := st.NonTx().CreateToken(crypto.HashToken(invite), model.RoleUser, 0, 1, 1, time.Time{}); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	response := authenticateControl(t, srv, st, "rolled-back", invite)
	if response.ErrorResponse == nil {
		t.Fatalf("personal token failure response = %#v, want error", response)
	}
	if user, err := st.NonTx().GetUserByUsername("rolled-back"); err != nil || user != nil {
		t.Fatalf("failed provisioning persisted user: user=%#v err=%v", user, err)
	}
	if _, err := factory.DB.Exec("DROP TRIGGER fail_personal_token_update"); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	if retry := authenticateControl(t, srv, st, "retry", invite); retry.AuthResponse == nil {
		t.Fatalf("failed provisioning consumed invite: %#v", retry)
	}
}

func TestAuthenticationFailuresDoNotRevealUsernameState(t *testing.T) {
	srv, st, _ := newTestServer(t)
	if _, err := st.NonTx().CreateUser("taken", model.RoleUser); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	srv.cfg.AllowNoToken = true
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	requests := []struct {
		name     string
		username string
		token    string
	}{
		{name: "invalid username", username: "not valid"},
		{name: "taken username", username: "taken"},
		{name: "invalid invite", username: "fresh", token: "not-an-invite"},
	}
	var want string
	for i, request := range requests {
		t.Run(request.name, func(t *testing.T) {
			response := authenticateControl(t, srv, st, request.username, request.token)
			if response.ErrorResponse == nil {
				t.Fatalf("response = %#v, want authentication error", response)
			}
			got := fmt.Sprintf("%d:%s", response.ErrorResponse.Code, response.ErrorResponse.Message)
			if i == 0 {
				want = got
			} else if got != want {
				t.Fatalf("externally distinguishable auth error = %q, want %q", got, want)
			}
		})
	}
	if got := strings.Count(logs.String(), "authentication failed"); got != 1 {
		t.Fatalf("account provisioning failure log count = %d, want 1; logs=%q", got, logs.String())
	}
	if strings.Contains(logs.String(), "invalid token") || strings.Contains(logs.String(), "username already taken") {
		t.Fatalf("account provisioning log revealed authentication state: %s", logs.String())
	}
}

func TestAuthenticationNonSourceRejectionsAreCountedThroughControlPath(t *testing.T) {
	t.Run("tracker capacity", func(t *testing.T) {
		srv, st, _ := newTestServer(t)
		srv.authLimiter = newAuthRateLimiter(3, time.Minute)
		srv.authLimiter.maxEntries = 1
		if !srv.authLimiter.Allow("occupied") {
			t.Fatal("failed to occupy authentication tracker")
		}

		expectControlRejectedBeforeAuth(t, srv, st)
		if got := srv.metrics.AuthRateLimitTrackerRejections.Load(); got != 1 {
			t.Fatalf("authentication tracker rejections = %d, want 1", got)
		}
		if got := srv.metrics.AuthRateLimitSourceRejections.Load() + srv.metrics.AuthRateLimitWindowRejections.Load(); got != 0 {
			t.Fatalf("other authentication rejection counters = %d, want 0", got)
		}
	})

	t.Run("window transition", func(t *testing.T) {
		srv, st, _ := newTestServer(t)
		now := time.Unix(1_700_000_000, 0)
		srv.authLimiter = newAuthRateLimiter(3, time.Minute)
		srv.authLimiter.now = func() time.Time { return now }
		if !srv.authLimiter.Allow("pipe") {
			t.Fatal("failed to reserve authentication window")
		}
		now = now.Add(time.Minute)

		expectControlRejectedBeforeAuth(t, srv, st)
		if got := srv.metrics.AuthRateLimitWindowRejections.Load(); got != 1 {
			t.Fatalf("authentication window rejections = %d, want 1", got)
		}
		if got := srv.metrics.AuthRateLimitSourceRejections.Load() + srv.metrics.AuthRateLimitTrackerRejections.Load(); got != 0 {
			t.Fatalf("other authentication rejection counters = %d, want 0", got)
		}
	})
}

func TestOpenModeProvisioningTrackerRejectionIsCountedThroughControlPath(t *testing.T) {
	srv, st, _ := newTestServer(t)
	srv.cfg.AllowNoToken = true
	srv.accountProvisionLimiter = newAccountProvisionLimiter(2, time.Hour)
	srv.accountProvisionLimiter.maxEntries = 1
	if !srv.accountProvisionLimiter.Reserve("occupied") {
		t.Fatal("failed to occupy provisioning tracker")
	}

	response := authenticateControl(t, srv, st, "blocked", "")
	if response.ErrorResponse == nil || response.ErrorResponse.Message != "authentication failed" {
		t.Fatalf("provisioning tracker rejection response = %#v", response)
	}
	if got := srv.metrics.AccountProvisionTrackerRejections.Load(); got != 1 {
		t.Fatalf("account provisioning tracker rejections = %d, want 1", got)
	}
	if got := srv.metrics.AccountProvisionSourceRejections.Load() + srv.metrics.AccountProvisionWindowRejections.Load(); got != 0 {
		t.Fatalf("other account provisioning rejection counters = %d, want 0", got)
	}
	if user, err := st.NonTx().GetUserByUsername("blocked"); err != nil || user != nil {
		t.Fatalf("tracker-rejected account persisted: user=%#v err=%v", user, err)
	}
}

func TestOpenModeAccountProvisioningBudgetIsEnforced(t *testing.T) {
	srv, st, _ := newTestServer(t)
	srv.cfg.AllowNoToken = true
	srv.accountProvisionLimiter = newAccountProvisionLimiter(1, time.Hour)

	first := authenticateControl(t, srv, st, "first", "")
	if first.AuthResponse == nil {
		t.Fatalf("first open-mode provisioning response = %#v", first)
	}
	second := authenticateControl(t, srv, st, "second", "")
	if second.ErrorResponse == nil || second.ErrorResponse.Message != "authentication failed" {
		t.Fatalf("provisioning above per-IP budget response = %#v", second)
	}
	if user, err := st.NonTx().GetUserByUsername("second"); err != nil || user != nil {
		t.Fatalf("rate-limited account persisted: user=%#v err=%v", user, err)
	}
	if got := srv.metrics.AccountProvisionSourceRejections.Load(); got != 1 {
		t.Fatalf("account provisioning rejection metric = %d, want 1", got)
	}
	if got := srv.metrics.AccountProvisionSourceHighWater.Load(); got != 1 {
		t.Fatalf("account provisioning source high-water mark = %d, want 1", got)
	}
}
