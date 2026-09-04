package server

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func newBootstrapTestServer(t *testing.T) (*Server, datastore.DataProviderFactory, string) {
	t.Helper()
	dataDir := t.TempDir()
	cfg := DefaultConfig()
	cfg.DataDir = dataDir
	cfg.DBPath = filepath.Join(dataDir, "gospeak.db")
	st, err := datastore.NewProviderFactory(cfg.DBPath)
	if err != nil {
		t.Fatalf("NewProviderFactory() error = %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return New(cfg, Dependencies{Store: st}), st, filepath.Join(dataDir, "bootstrap-admin.token")
}

func readBootstrapToken(t *testing.T, path string) string {
	t.Helper()
	credential, err := os.ReadFile(path) //nolint:gosec // path is inside t.TempDir
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return strings.TrimSpace(string(credential))
}

func authenticateBootstrap(t *testing.T, srv *Server, st datastore.DataProviderFactory, username, token string) (*pb.AuthResponse, error) {
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
		_ = clientConn.Close()
		t.Fatalf("WriteControlMessage() error = %v", err)
	}
	response, err := protocol.ReadControlMessage(clientConn)
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("control handler did not return")
	}
	if err != nil {
		return nil, err
	}
	if response.ErrorResponse != nil {
		return nil, fmt.Errorf("auth failed: %s", response.ErrorResponse.Message)
	}
	if response.AuthResponse == nil {
		return nil, fmt.Errorf("response = %#v, want AuthResponse", response)
	}
	return response.AuthResponse, nil
}

func TestEnsureAdminTokenWritesRetryableCredentialWithoutLoggingSecret(t *testing.T) {
	srv, st, credentialPath := newBootstrapTestServer(t)
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatalf("ensureAdminToken() error = %v", err)
	}
	rawToken := readBootstrapToken(t, credentialPath)
	if rawToken == "" {
		t.Fatal("bootstrap credential is empty")
	}
	if _, err := readBootstrapCredential(credentialPath); err != nil {
		t.Fatalf("read protected bootstrap credential: %v", err)
	}
	if strings.Contains(logs.String(), rawToken) {
		t.Fatalf("normal logs contain bootstrap credential: %q", logs.String())
	}
	if !strings.Contains(logs.String(), credentialPath) {
		t.Fatalf("normal logs do not identify bootstrap credential path: %q", logs.String())
	}
}

func TestBootstrapProvisioningRollsBackSessionPreparationFailure(t *testing.T) {
	srv, st, credentialPath := newBootstrapTestServer(t)
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatalf("ensureAdminToken() error = %v", err)
	}
	rawToken := readBootstrapToken(t, credentialPath)
	srv.sessions.issuedSessionIDs = usableSessionIDs

	if _, err := authenticateBootstrap(t, srv, st, "bootstrap-prepare", rawToken); err == nil {
		t.Fatal("bootstrap authentication succeeded despite session ID exhaustion")
	}
	if user, err := st.NonTx().GetUserByUsername("bootstrap-prepare"); err != nil || user != nil {
		t.Fatalf("session-preparation-rejected bootstrap user persisted: user=%#v err=%v", user, err)
	}

	srv.sessions.issuedSessionIDs = 0
	response, err := authenticateBootstrap(t, srv, st, "bootstrap-prepare", rawToken)
	if err != nil || response.AutoToken == "" {
		t.Fatalf("bootstrap retry after session preparation failure = %#v, error = %v", response, err)
	}
}

func TestBootstrapProvisioningRetriesAfterAuthResponseWriteFailure(t *testing.T) {
	srv, st, credentialPath := newBootstrapTestServer(t)
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatalf("ensureAdminToken() error = %v", err)
	}
	rawToken := readBootstrapToken(t, credentialPath)

	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		srv.handleControlConn(newControlHandler(srv, st), serverConn, st)
		close(done)
	}()
	if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{
		AuthRequest: &pb.AuthRequest{Username: "bootstrap-admin", Token: rawToken},
	}); err != nil {
		t.Fatalf("WriteControlMessage() error = %v", err)
	}
	if err := clientConn.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("control handler did not return after failed AuthResponse write")
	}

	firstRetry, err := authenticateBootstrap(t, srv, st, "bootstrap-admin", rawToken)
	if err != nil {
		t.Fatalf("first retry error = %v", err)
	}
	if firstRetry.Role != "admin" || firstRetry.AutoToken == "" {
		t.Fatalf("first retry = %#v, want admin with personal token", firstRetry)
	}
	secondRetry, err := authenticateBootstrap(t, srv, st, "bootstrap-admin", rawToken)
	if err != nil {
		t.Fatalf("second retry error = %v", err)
	}
	if secondRetry.AutoToken != firstRetry.AutoToken {
		t.Fatal("bootstrap retry rotated the personal token")
	}
	if _, err := authenticateBootstrap(t, srv, st, "other-admin", rawToken); err == nil {
		t.Fatal("bootstrap credential provisioned a second admin")
	}
	users, err := st.NonTx().ListUsers()
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	if len(users) != 1 || users[0].Username != "bootstrap-admin" {
		t.Fatalf("users = %#v, want one bootstrap admin", users)
	}
}

func TestEnsureAdminTokenRetiresStaleCredentialForExistingStore(t *testing.T) {
	srv, st, credentialPath := newBootstrapTestServer(t)
	if err := st.NonTx().CreateToken(crypto.HashToken("existing-invite"), model.RoleUser, 0, 1, 1, time.Time{}); err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	rawToken, err := crypto.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	if err := os.WriteFile(credentialPath, []byte(rawToken+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatalf("ensureAdminToken() error = %v", err)
	}
	if _, err := os.Stat(credentialPath); !os.IsNotExist(err) {
		t.Fatalf("stale credential error = %v, want not exist", err)
	}
}

func TestEnsureAdminTokenRecoversPublishedCredential(t *testing.T) {
	srv, st, credentialPath := newBootstrapTestServer(t)
	rawToken, err := crypto.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	if err := os.WriteFile(credentialPath, []byte(rawToken+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatalf("ensureAdminToken() error = %v", err)
	}
	got, err := os.ReadFile(credentialPath) //nolint:gosec // path is inside t.TempDir
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != rawToken+"\n" {
		t.Fatalf("credential was rotated during recovery")
	}
	if _, err := authenticateBootstrap(t, srv, st, "recovered-admin", rawToken); err != nil {
		t.Fatalf("recovered bootstrap authentication error = %v", err)
	}
}

func TestEnsureAdminTokenRejectsUnsafePublishedCredential(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission fixture")
	}
	srv, st, credentialPath := newBootstrapTestServer(t)
	rawToken, err := crypto.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	if err := os.WriteFile(credentialPath, []byte(rawToken+"\n"), 0o644); err != nil { //nolint:gosec // insecure mode is the regression fixture
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := srv.ensureAdminToken(st); err == nil {
		t.Fatal("ensureAdminToken() accepted a group/world-readable credential")
	}
	hasTokens, err := st.NonTx().HasTokens()
	if err != nil {
		t.Fatalf("HasTokens() error = %v", err)
	}
	if hasTokens {
		t.Fatal("unsafe bootstrap credential was installed in the datastore")
	}
}

func TestEnsureAdminTokenRejectsCredentialMadeUnsafeAfterInitialization(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission fixture")
	}
	srv, st, credentialPath := newBootstrapTestServer(t)
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatalf("first ensureAdminToken() error = %v", err)
	}
	if err := os.Chmod(credentialPath, 0o644); err != nil { //nolint:gosec // insecure mode is the regression fixture
		t.Fatalf("Chmod() error = %v", err)
	}
	if err := srv.ensureAdminToken(st); err == nil {
		t.Fatal("ensureAdminToken() accepted a credential made group/world-readable after initialization")
	}
}

func TestEnsureAdminTokenRejectsCredentialWithSpecialModeBits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission fixture")
	}
	srv, st, credentialPath := newBootstrapTestServer(t)
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatalf("first ensureAdminToken() error = %v", err)
	}
	if err := os.Chmod(credentialPath, 0o600|os.ModeSticky); err != nil { //nolint:gosec // special mode is the regression fixture
		t.Fatalf("Chmod() error = %v", err)
	}
	if err := srv.ensureAdminToken(st); err == nil {
		t.Fatal("ensureAdminToken() accepted a credential with a sticky bit")
	}
}

func TestEnsureAdminTokenRejectsCredentialSymlink(t *testing.T) {
	srv, st, credentialPath := newBootstrapTestServer(t)
	rawToken, err := crypto.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	target := filepath.Join(filepath.Dir(credentialPath), "redirected-token")
	if err := os.WriteFile(target, []byte(rawToken+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink(target, credentialPath); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		t.Fatalf("Symlink() error = %v", err)
	}

	if err := srv.ensureAdminToken(st); err == nil {
		t.Fatal("ensureAdminToken() accepted a credential symlink")
	}
}

func TestBootstrapProvisioningRollsBackControlBudgetTrackerFailure(t *testing.T) {
	srv, st, credentialPath := newBootstrapTestServer(t)
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatalf("ensureAdminToken() error = %v", err)
	}
	bootstrapToken := readBootstrapToken(t, credentialPath)

	now := time.Unix(1_700_000_000, 0)
	srv.controlUserBudgets = newControlUserBudgetManager(1, 1, func() time.Time { return now })
	srv.controlUserBudgets.maxEntries = 1
	occupied, _, _, ok := srv.controlUserBudgets.Acquire(999)
	if !ok || !occupied.Allow(&pb.ControlMessage{Ping: &pb.Ping{}}).Allowed {
		t.Fatal("could not occupy control user budget tracker")
	}
	srv.controlUserBudgets.Release(999)

	if _, err := authenticateBootstrap(t, srv, st, "bootstrap-budget-retry", bootstrapToken); err == nil || !strings.Contains(err.Error(), "server control capacity reached") {
		t.Fatalf("bootstrap tracker-capacity error = %v", err)
	}
	if user, err := st.NonTx().GetUserByUsername("bootstrap-budget-retry"); err != nil || user != nil {
		t.Fatalf("tracker-rejected bootstrap user persisted: user=%#v err=%v", user, err)
	}
	if got := srv.metrics.ControlUserTrackerRejections.Load(); got != 1 {
		t.Fatalf("control user tracker rejections = %d, want 1", got)
	}

	now = now.Add(2 * time.Second)
	if _, err := authenticateBootstrap(t, srv, st, "bootstrap-budget-retry", bootstrapToken); err != nil {
		t.Fatalf("bootstrap retry after tracker refill: %v", err)
	}
	if user, err := st.NonTx().GetUserByUsername("bootstrap-budget-retry"); err != nil || user == nil {
		t.Fatalf("bootstrap retry user = %#v, err = %v", user, err)
	}
}

func TestBootstrapProvisioningRollsBackPersonalTokenStoreFailure(t *testing.T) {
	srv, st, credentialPath := newBootstrapTestServer(t)
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatalf("ensureAdminToken() error = %v", err)
	}
	rawToken := readBootstrapToken(t, credentialPath)
	provider := st.(*datastore.ProviderFactory)
	if _, err := provider.DB.Exec(`CREATE TRIGGER fail_bootstrap_personal_token
		BEFORE UPDATE OF personal_token_hash ON users
		BEGIN SELECT RAISE(ABORT, 'forced personal token failure'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	if _, err := authenticateBootstrap(t, srv, st, "bootstrap-admin", rawToken); err == nil {
		t.Fatal("bootstrap authentication unexpectedly succeeded while token storage failed")
	}
	users, err := st.NonTx().ListUsers()
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("users after rolled-back provisioning = %d, want 0", len(users))
	}
	if _, err := provider.DB.Exec(`DROP TRIGGER fail_bootstrap_personal_token`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	if _, err := authenticateBootstrap(t, srv, st, "bootstrap-admin", rawToken); err != nil {
		t.Fatalf("bootstrap retry error = %v", err)
	}
}

func TestBootstrapProvisioningRetriesAfterSessionFailure(t *testing.T) {
	srv, st, credentialPath := newBootstrapTestServer(t)
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatalf("ensureAdminToken() error = %v", err)
	}
	rawToken := readBootstrapToken(t, credentialPath)
	srv.sessions.issuedSessionIDs = usableSessionIDs
	if _, err := authenticateBootstrap(t, srv, st, "bootstrap-admin", rawToken); err == nil {
		t.Fatal("bootstrap authentication unexpectedly succeeded with exhausted sessions")
	}
	srv.sessions.issuedSessionIDs = 0
	response, err := authenticateBootstrap(t, srv, st, "bootstrap-admin", rawToken)
	if err != nil {
		t.Fatalf("bootstrap retry error = %v", err)
	}
	if response.AutoToken == "" {
		t.Fatal("bootstrap retry returned an empty personal token")
	}
}

func TestBootstrapTokenFinalizesAfterPersonalAuthentication(t *testing.T) {
	srv, st, credentialPath := newBootstrapTestServer(t)
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatalf("ensureAdminToken() error = %v", err)
	}
	rawToken := readBootstrapToken(t, credentialPath)
	response, err := authenticateBootstrap(t, srv, st, "bootstrap-admin", rawToken)
	if err != nil {
		t.Fatalf("bootstrap authentication error = %v", err)
	}
	if _, err := authenticateBootstrap(t, srv, st, "bootstrap-admin", response.AutoToken); err != nil {
		t.Fatalf("personal authentication error = %v", err)
	}
	if _, err := os.Stat(credentialPath); !os.IsNotExist(err) {
		t.Fatalf("credential file after finalization error = %v, want not exist", err)
	}
	if _, err := authenticateBootstrap(t, srv, st, "bootstrap-admin", rawToken); err == nil {
		t.Fatal("finalized bootstrap credential was accepted")
	}
	if _, err := authenticateBootstrap(t, srv, st, "bootstrap-admin", response.AutoToken); err != nil {
		t.Fatalf("repeated personal authentication error = %v", err)
	}
}

func TestBootstrapCredentialRemovalRetriesAfterFinalization(t *testing.T) {
	srv, st, credentialPath := newBootstrapTestServer(t)
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatalf("ensureAdminToken() error = %v", err)
	}
	rawToken := readBootstrapToken(t, credentialPath)
	response, err := authenticateBootstrap(t, srv, st, "bootstrap-admin", rawToken)
	if err != nil {
		t.Fatalf("bootstrap authentication error = %v", err)
	}
	if err := os.Remove(credentialPath); err != nil {
		t.Fatalf("remove credential fixture: %v", err)
	}
	if err := os.Mkdir(credentialPath, 0o700); err != nil {
		t.Fatalf("create removal blocker: %v", err)
	}
	blocker := filepath.Join(credentialPath, "blocker")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatalf("write removal blocker: %v", err)
	}
	if _, err := authenticateBootstrap(t, srv, st, "bootstrap-admin", response.AutoToken); err != nil {
		t.Fatalf("first personal authentication error = %v", err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatalf("remove blocker file: %v", err)
	}
	if err := os.Remove(credentialPath); err != nil {
		t.Fatalf("remove blocker directory: %v", err)
	}
	if err := os.WriteFile(credentialPath, []byte(rawToken+"\n"), 0o600); err != nil {
		t.Fatalf("restore stale credential fixture: %v", err)
	}
	restarted := New(srv.cfg, Dependencies{Store: st})
	if err := restarted.ensureAdminToken(st); err != nil {
		t.Fatalf("restart ensureAdminToken() error = %v", err)
	}
	if _, err := os.Stat(credentialPath); !os.IsNotExist(err) {
		t.Fatalf("credential after restart removal retry error = %v, want not exist", err)
	}
}

func TestConcurrentBootstrapProvisioningCreatesOneAdmin(t *testing.T) {
	for _, usernames := range [][]string{{"same-admin", "same-admin"}, {"admin-one", "admin-two"}} {
		t.Run(strings.Join(usernames, "-"), func(t *testing.T) {
			srv, st, credentialPath := newBootstrapTestServer(t)
			if err := srv.ensureAdminToken(st); err != nil {
				t.Fatalf("ensureAdminToken() error = %v", err)
			}
			rawToken := readBootstrapToken(t, credentialPath)
			start := make(chan struct{})
			type result struct {
				response *pb.AuthResponse
				err      error
			}
			results := make(chan result, len(usernames))
			var wg sync.WaitGroup
			for _, username := range usernames {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					response, err := authenticateBootstrap(t, srv, st, username, rawToken)
					results <- result{response: response, err: err}
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			successes := 0
			var returnedToken string
			for result := range results {
				if result.err != nil {
					continue
				}
				successes++
				if returnedToken != "" && returnedToken != result.response.AutoToken {
					t.Fatal("same bootstrap identity returned different personal tokens")
				}
				returnedToken = result.response.AutoToken
			}
			wantSuccesses := 2
			if usernames[0] != usernames[1] {
				wantSuccesses = 1
			}
			if successes != wantSuccesses {
				t.Fatalf("successful authentications = %d, want %d", successes, wantSuccesses)
			}
			users, err := st.NonTx().ListUsers()
			if err != nil {
				t.Fatalf("ListUsers() error = %v", err)
			}
			if len(users) != 1 {
				t.Fatalf("users = %d, want 1", len(users))
			}
		})
	}
}

func TestBootstrapProvisioningRetrySurvivesRestart(t *testing.T) {
	srv, st, credentialPath := newBootstrapTestServer(t)
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatalf("ensureAdminToken() error = %v", err)
	}
	rawToken := readBootstrapToken(t, credentialPath)
	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		srv.handleControlConn(newControlHandler(srv, st), serverConn, st)
		close(done)
	}()
	if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{
		AuthRequest: &pb.AuthRequest{Username: "bootstrap-admin", Token: rawToken},
	}); err != nil {
		t.Fatalf("WriteControlMessage() error = %v", err)
	}
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("control handler did not return")
	}
	if err := st.(*datastore.ProviderFactory).Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := datastore.NewProviderFactory(srv.cfg.DBPath)
	if err != nil {
		t.Fatalf("NewProviderFactory() after restart error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := New(srv.cfg, Dependencies{Store: reopened})
	response, err := authenticateBootstrap(t, restarted, reopened, "bootstrap-admin", rawToken)
	if err != nil {
		t.Fatalf("bootstrap retry after restart error = %v", err)
	}
	if response.AutoToken != deriveBootstrapPersonalToken(rawToken, "bootstrap-admin") {
		t.Fatal("bootstrap retry after restart returned a different personal token")
	}
}

func TestOrdinarySingleUseInviteRemainsConsumedAfterResponseFailure(t *testing.T) {
	srv, st, _ := newBootstrapTestServer(t)
	creator, err := st.NonTx().CreateUser("token-creator", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	rawToken, err := crypto.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	if err := st.NonTx().CreateToken(crypto.HashToken(rawToken), model.RoleUser, 0, creator.ID, 1, st.NonTx().ZeroTime()); err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		srv.handleControlConn(newControlHandler(srv, st), serverConn, st)
		close(done)
	}()
	if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{
		AuthRequest: &pb.AuthRequest{Username: "first-invite-user", Token: rawToken},
	}); err != nil {
		t.Fatalf("WriteControlMessage() error = %v", err)
	}
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("control handler did not return")
	}
	if _, err := authenticateBootstrap(t, srv, st, "second-invite-user", rawToken); err == nil {
		t.Fatal("ordinary single-use invite was reusable after response failure")
	}
}

func TestHandleCreateTokenRejectsNegativeMaxUses(t *testing.T) {
	srv, st, _ := newBootstrapTestServer(t)
	admin, err := st.NonTx().CreateUser("admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	session, err := srv.sessions.Create(admin.ID, admin.Username, admin.Role)
	if err != nil {
		t.Fatalf("Create() session error = %v", err)
	}
	responseConn := &bufferConn{}
	srv.handleCreateToken(session.ID, &pb.CreateTokenRequest{Role: model.RoleAdmin.String(), MaxUses: -1}, st, responseConn)
	response, err := protocol.ReadControlMessage(responseConn)
	if err != nil {
		t.Fatalf("ReadControlMessage() error = %v", err)
	}
	if response.ErrorResponse == nil || response.ErrorResponse.Code != 31 {
		t.Fatalf("response = %#v, want max-uses error", response)
	}
	hasTokens, err := st.NonTx().HasTokens()
	if err != nil {
		t.Fatalf("HasTokens() error = %v", err)
	}
	if hasTokens {
		t.Fatal("negative max-uses request created a token")
	}
}
