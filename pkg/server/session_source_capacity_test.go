package server

import (
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func authenticateSource(t *testing.T, srv *Server, st datastore.DataProviderFactory, username, token, source string) *pb.ControlMessage {
	t.Helper()
	before := srv.sessions.CapacitySnapshot()
	serverConn, clientConn := net.Pipe()
	wrapped := remoteAddrConn{Conn: serverConn, remote: testAddr(net.JoinHostPort(source, "1234"))}
	done := make(chan struct{})
	go func() { srv.handleControlConn(newControlHandler(srv, st), wrapped, st); close(done) }()
	t.Cleanup(func() { _ = clientConn.Close() })
	if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{Username: username, Token: token, MediaCiphers: []string{"aes128"}}}); err != nil {
		t.Fatal(err)
	}
	response, err := protocol.ReadControlMessage(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not exit")
	}
	requireSourceCapacity(t, srv.sessions, before.CapacityUsed, before.ActiveSources, before.MaxSourceCapacity)
	return response
}

func requireSourceCapacity(t *testing.T, manager *SessionManager, used, sources, maximum int) {
	t.Helper()
	snapshot := manager.CapacitySnapshot()
	if snapshot.CapacityUsed != used || snapshot.ActiveSources != sources || snapshot.MaxSourceCapacity != maximum {
		t.Fatalf("source capacity = %#v, want used=%d sources=%d max=%d", snapshot, used, sources, maximum)
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if len(manager.sessionSources) != len(manager.sessions) {
		t.Fatal("active source ownership does not match sessions")
	}
	total := 0
	for source, count := range manager.bySource {
		if !source.IsValid() || count <= 0 {
			t.Fatalf("invalid or empty source entry: %v=%d", source, count)
		}
		total += count
	}
	if total != used {
		t.Fatalf("source claims = %d, want %d", total, used)
	}
}

func TestSourceSessionCapacityIncludesConcurrentPending(t *testing.T) {
	srv := New(DefaultConfig(), Dependencies{})
	start := make(chan struct{})
	reservations := make(chan *SessionReservation, 65)
	var wg sync.WaitGroup
	for i := range 65 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			reservation, err := srv.sessions.Reserve(int64(i+1), "127.0.0.1")
			if err == nil {
				reservations <- reservation
			} else if !errors.Is(err, ErrSourceSessionLimitReached) {
				t.Errorf("Reserve: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(reservations)
	var admitted []*SessionReservation
	for reservation := range reservations {
		admitted = append(admitted, reservation)
	}
	defer func() {
		for _, reservation := range admitted {
			reservation.Release()
		}
	}()
	if len(admitted) != 64 {
		t.Fatalf("same-source pending reservations = %d, want 64", len(admitted))
	}
	requireSourceCapacity(t, srv.sessions, 64, 1, 64)
	neighbor, err := srv.sessions.Reserve(100, "127.0.0.2")
	if err != nil {
		t.Fatalf("unrelated source denied: %v", err)
	}
	neighbor.Release()
	activeIDs := make([]uint32, 0, 32)
	for _, reservation := range admitted[:32] {
		session, err := reservation.Create("mixed-occupancy", model.RoleUser, 0)
		if err != nil {
			t.Fatal(err)
		}
		activeIDs = append(activeIDs, session.ID)
		defer srv.sessions.Remove(session.ID)
	}
	if _, err := srv.sessions.Reserve(101, "127.0.0.1"); !errors.Is(err, ErrSourceSessionLimitReached) {
		t.Fatalf("mixed pending/active occupancy did not retain the cap: %v", err)
	}
	requireSourceCapacity(t, srv.sessions, 64, 1, 64)
	for _, reservation := range admitted {
		reservation.Release()
		reservation.Release()
	}
	requireSourceCapacity(t, srv.sessions, 32, 1, 32)
	for _, id := range activeIDs {
		srv.sessions.Remove(id)
		srv.sessions.Remove(id)
	}
	requireSourceCapacity(t, srv.sessions, 0, 0, 0)
	if got := srv.sessions.CapacitySnapshot().SourceCapacityHighWater; got != 64 {
		t.Fatalf("source high water = %d", got)
	}
}

func TestSourceSessionGroupsAndInvalidAddresses(t *testing.T) {
	for _, tc := range []struct{ first, same, different string }{
		{"127.0.0.1", "::ffff:127.0.0.1", "127.0.0.2"},
		{"2001:db8:1:2::1", "2001:db8:1:2:ffff::2", "2001:db8:1:3::1"},
	} {
		t.Run(tc.first, func(t *testing.T) {
			manager := NewSessionManagerWithLimits(8, 8, 1)
			active, err := manager.Create(1, "first", model.RoleUser, tc.first)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Remove(active.ID)
			if _, err := manager.Reserve(2, tc.same); !errors.Is(err, ErrSourceSessionLimitReached) {
				t.Fatalf("equivalent source admitted: %v", err)
			}
			neighbor, err := manager.Reserve(2, tc.different)
			if err != nil {
				t.Fatalf("different source denied: %v", err)
			}
			neighbor.Release()
			requireSourceCapacity(t, manager, 1, 1, 1)
		})
	}
	manager := NewSessionManager()
	for _, address := range []string{"", "pipe", "localhost", "127.0.0.1:1234", "2001:db8::/64", "fe80::1%eth0"} {
		if _, err := manager.Reserve(1, address); err == nil {
			t.Errorf("invalid source %q admitted", address)
		}
	}
	if _, err := canonicalControlPeerIP(&net.TCPAddr{IP: net.ParseIP("fe80::1"), Zone: "eth0"}); err == nil {
		t.Fatal("scoped TCP peer accepted")
	}
	requireSourceCapacity(t, manager, 0, 0, 0)
}

func TestSourceSessionFailureRelease(t *testing.T) {
	for _, failure := range []string{"bind", "unbound prepare", "ID exhaustion", "double prepare", "unprepared activation", "released activation"} {
		t.Run(failure, func(t *testing.T) {
			manager := NewSessionManagerWithLimits(8, 1, 1)
			reservation, err := manager.Reserve(0, "127.0.0.1")
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "bind":
				other, err := manager.Reserve(1, "127.0.0.2")
				if err != nil {
					t.Fatal(err)
				}
				if err := reservation.BindUser(1); !errors.Is(err, ErrUserSessionLimitReached) {
					t.Fatalf("BindUser: %v", err)
				}
				other.Release()
			case "unbound prepare":
				if _, err := reservation.Prepare("failed", model.RoleUser, 0); err == nil {
					t.Fatal("unbound preparation accepted")
				}
			case "unprepared activation":
				if reservation.Activate() != nil {
					t.Fatal("unprepared activation accepted")
				}
			default:
				if err := reservation.BindUser(1); err != nil {
					t.Fatal(err)
				}
				if failure == "ID exhaustion" {
					manager.issuedSessionIDs = usableSessionIDs
				}
				_, err := reservation.Prepare("failed", model.RoleUser, 0)
				if failure == "ID exhaustion" {
					if !errors.Is(err, ErrSessionIDExhausted) {
						t.Fatalf("Prepare: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if failure == "double prepare" {
					if _, err := reservation.Prepare("again", model.RoleUser, 0); err == nil {
						t.Fatal("double preparation accepted")
					}
				}
				if failure == "released activation" {
					reservation.Release()
					if reservation.Activate() != nil {
						t.Fatal("released activation accepted")
					}
				}
			}
			reservation.Release()
			reservation.Release()
			requireSourceCapacity(t, manager, 0, 0, 0)
			replacement, err := manager.Reserve(2, "127.0.0.1")
			if err != nil {
				t.Fatal(err)
			}
			replacement.Release()
		})
	}
}

func TestSourceClaimSurvivesActivationPublicationAndReconnect(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxSessionsPerSource = 1
	srv := New(cfg, Dependencies{})
	reservation, err := srv.sessions.Reserve(1, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := reservation.Prepare("racing", model.RoleUser, 0)
	if err != nil {
		t.Fatal(err)
	}
	registered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	srv.beginSessionBanCheck(1)
	go func() {
		defer close(done)
		srv.activateAfterBanCheck(1, reservation, false, func(*model.Session) { close(registered); <-release })
	}()
	<-registered // activation is visible, connection publication has not returned
	reservation.Release()
	if _, err := srv.sessions.Reserve(2, "127.0.0.1"); !errors.Is(err, ErrSourceSessionLimitReached) {
		t.Errorf("publication gap lost source claim: %v", err)
	}
	requireSourceCapacity(t, srv.sessions, 1, 1, 1)
	close(release)
	<-done
	for range 32 {
		srv.sessions.Remove(prepared.ID)
		srv.sessions.Remove(prepared.ID)
		reservation.Release()
		requireSourceCapacity(t, srv.sessions, 0, 0, 0)
		reservation, err = srv.sessions.Reserve(1, "::ffff:127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		prepared, err = reservation.Create("reconnected", model.RoleUser, 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	srv.sessions.Remove(prepared.ID)
	reservation.Release()
	requireSourceCapacity(t, srv.sessions, 0, 0, 0)
}

func TestSourceSessionCapacityPreventsDurableAuthenticationChanges(t *testing.T) {
	for _, path := range []string{"personal", "invite", "open", "bootstrap"} {
		t.Run(path, func(t *testing.T) {
			srv, st, credentialPath := newBootstrapTestServer(t)
			srv.sessions = NewSessionManagerWithLimits(8, 8, 1)
			token := ""
			switch path {
			case "personal":
				user, err := st.NonTx().CreateUser("source-denied", model.RoleUser)
				if err != nil {
					t.Fatal(err)
				}
				token = "source-personal"
				if err := st.NonTx().UpdateUserPersonalToken(user.ID, crypto.HashToken(token), time.Now()); err != nil {
					t.Fatal(err)
				}
			case "invite":
				token = "source-invite"
				if err := st.NonTx().CreateToken(crypto.HashToken(token), model.RoleUser, 0, 1, 1, time.Time{}); err != nil {
					t.Fatal(err)
				}
			case "open":
				srv.cfg.AllowNoToken = true
			case "bootstrap":
				if err := srv.ensureAdminToken(st); err != nil {
					t.Fatal(err)
				}
				token = readBootstrapToken(t, credentialPath)
			}
			before, err := st.NonTx().ListUsers()
			if err != nil {
				t.Fatal(err)
			}
			occupant, err := srv.sessions.Reserve(0, "127.0.0.1")
			if err != nil {
				t.Fatal(err)
			}
			defer occupant.Release()
			response := authenticateSource(t, srv, st, "source-denied", token, "::ffff:127.0.0.1")
			if response.ErrorResponse == nil || response.ErrorResponse.Code != 3 {
				t.Fatalf("source capacity response = %#v", response)
			}
			after, err := st.NonTx().ListUsers()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("capacity denial changed users: before=%v after=%v err=%v", before, after, err)
			}
			requireSourceCapacity(t, srv.sessions, 1, 1, 1)
			if srv.metrics.SessionSourceRejections.Load() != 1 || srv.metrics.SessionGlobalRejections.Load() != 0 || srv.metrics.SessionUserRejections.Load() != 0 {
				t.Fatal("capacity rejection misclassified")
			}
			if path == "bootstrap" {
				if state, err := st.NonTx().BootstrapTokenState(); err != nil || state != datastore.BootstrapTokenPending {
					t.Fatalf("bootstrap state = %v, %v", state, err)
				}
				if readBootstrapToken(t, credentialPath) != token {
					t.Fatal("capacity denial retired bootstrap credential")
				}
			}
			occupant.Release()
			response = authenticateSource(t, srv, st, "source-denied", token, "127.0.0.1")
			if response.AuthResponse == nil {
				t.Fatalf("capacity denial consumed credential: %#v", response)
			}
			requireSourceCapacity(t, srv.sessions, 0, 0, 0)
		})
	}
}

func TestSourceCapacityDenialDoesNotFinalizeBootstrapPersonalToken(t *testing.T) {
	srv, st, credentialPath := newBootstrapTestServer(t)
	srv.sessions = NewSessionManagerWithLimits(8, 8, 1)
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatal(err)
	}
	bootstrap := readBootstrapToken(t, credentialPath)
	first := authenticateSource(t, srv, st, "bootstrap-owner", bootstrap, "127.0.0.1")
	if first.AuthResponse == nil {
		t.Fatal("bootstrap failed")
	}
	occupant, err := srv.sessions.Reserve(0, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer occupant.Release()
	denied := authenticateSource(t, srv, st, "bootstrap-owner", first.AuthResponse.AutoToken, "127.0.0.1")
	if denied.ErrorResponse == nil || denied.ErrorResponse.Code != 3 {
		t.Fatal("personal login was not capacity denied")
	}
	if state, err := st.NonTx().BootstrapTokenState(); err != nil || state != datastore.BootstrapTokenPending {
		t.Fatalf("capacity denial finalized bootstrap: %v, %v", state, err)
	}
	if readBootstrapToken(t, credentialPath) != bootstrap {
		t.Fatal("capacity denial removed bootstrap file")
	}
	occupant.Release()
	if response := authenticateSource(t, srv, st, "bootstrap-owner", first.AuthResponse.AutoToken, "127.0.0.1"); response.AuthResponse == nil {
		t.Fatal("personal retry failed")
	}
	if state, err := st.NonTx().BootstrapTokenState(); err != nil || state != datastore.BootstrapTokenFinalized {
		t.Fatalf("personal retry did not finalize: %v, %v", state, err)
	}
	requireSourceCapacity(t, srv.sessions, 0, 0, 0)
}

func TestInvalidSourceCannotProvisionOpenAccount(t *testing.T) {
	srv, st, _ := newTestServer(t)
	srv.cfg.AllowNoToken = true
	for _, source := range []string{"not-an-IP", "fe80::1%eth0"} {
		response := authenticateSource(t, srv, st, "invalid-source", "", source)
		if response.ErrorResponse == nil || response.ErrorResponse.Code != 2 {
			t.Fatalf("invalid peer accepted: %#v", response)
		}
	}
	if user, err := st.NonTx().GetUserByUsername("invalid-source"); err != nil || user != nil {
		t.Fatalf("invalid peer persisted account: %v, %v", user, err)
	}
	requireSourceCapacity(t, srv.sessions, 0, 0, 0)
}

func TestSourceLimitConfigurationNormalization(t *testing.T) {
	for _, limit := range []int{-1, 0, 3} {
		cfg := DefaultConfig()
		cfg.MaxSessionsPerSource = limit
		srv := New(cfg, Dependencies{})
		want := limit
		if want <= 0 {
			want = 64
		}
		snapshot := srv.sessions.CapacitySnapshot()
		if srv.cfg.MaxSessionsPerSource != want || snapshot.PerSourceLimit != want || snapshot.GlobalLimit != 1024 || snapshot.PerUserLimit != 8 {
			t.Fatalf("normalized config = %#v, snapshot = %#v", srv.cfg, snapshot)
		}
	}
}
