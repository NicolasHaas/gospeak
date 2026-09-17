package server

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func registerRemoteTestConn(handler *ControlHandler, sessionID uint32, ip string) *closeTrackingConn {
	conn := newCloseTrackingConn()
	remote := remoteAddrConn{Conn: conn, remote: testAddr(net.JoinHostPort(ip, "41000"))}
	handler.setConn(sessionID, remote)
	return conn
}

func TestExplicitIPBanUsesSelectedSessionAndProtectsBootstrapOwner(t *testing.T) {
	srv, st, handler := newTestServer(t)
	if err := st.NonTx().CreateBootstrapToken("bootstrap-ip-policy"); err != nil {
		t.Fatalf("CreateBootstrapToken: %v", err)
	}
	tx, err := st.Tx(context.Background())
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	owner, err := tx.ProvisionBootstrapUser("bootstrap-ip-policy", "bootstrap-owner", "bootstrap-personal-ip", time.Now().UTC())
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("ProvisionBootstrapUser: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	adminUser, err := st.NonTx().CreateUser("ip-admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(admin): %v", err)
	}
	targetUser, err := st.NonTx().CreateUser("ip-target", model.RoleUser)
	if err != nil {
		t.Fatalf("CreateUser(target): %v", err)
	}
	bystanderUser, err := st.NonTx().CreateUser("ip-bystander", model.RoleUser)
	if err != nil {
		t.Fatalf("CreateUser(bystander): %v", err)
	}

	admin := mustCreateSession(t, srv.sessions, adminUser.ID, adminUser.Username, adminUser.Role)
	selected := mustCreateSession(t, srv.sessions, targetUser.ID, targetUser.Username, targetUser.Role)
	otherTarget := mustCreateSession(t, srv.sessions, targetUser.ID, targetUser.Username, targetUser.Role)
	bystander := mustCreateSession(t, srv.sessions, bystanderUser.ID, bystanderUser.Username, bystanderUser.Role)
	ownerSession := mustCreateSession(t, srv.sessions, owner.ID, owner.Username, owner.Role)

	selectedConn := registerRemoteTestConn(handler, selected.ID, "::ffff:192.0.2.44")
	otherTargetConn := registerRemoteTestConn(handler, otherTarget.ID, "198.51.100.9")
	bystanderConn := registerRemoteTestConn(handler, bystander.ID, "192.0.2.44")
	ownerConn := registerRemoteTestConn(handler, ownerSession.ID, "192.0.2.44")

	srv.handleBanUser(handler, admin.ID, &pb.BanUserRequest{
		UserID:         targetUser.ID,
		Reason:         "policy-test-192.0.2.44-must-not-be-stored",
		IPBanSessionID: selected.ID,
	}, st, &nopConn{})

	requireClosed(t, selectedConn)
	requireClosed(t, otherTargetConn)
	requireClosed(t, bystanderConn)
	requireOpen(t, ownerConn)

	bans, _, err := st.NonTx().ListActiveBans(0, datastore.MaxBanPageSize)
	if err != nil {
		t.Fatalf("ListActiveBans: %v", err)
	}
	if len(bans) != 2 {
		t.Fatalf("active bans = %#v, want one account and one IP ban", bans)
	}
	var gotIP string
	for _, ban := range bans {
		if ban.IP != "" {
			gotIP = ban.IP
		}
	}
	if gotIP != "192.0.2.44" {
		t.Fatalf("stored IP = %q, want canonical exact address", gotIP)
	}
}

func TestIPBanSynchronizesWithBootstrapOwnerProvisioning(t *testing.T) {
	srv, st, handler := newTestServer(t)
	adminUser, err := st.NonTx().CreateUser("bootstrap-race-admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(admin): %v", err)
	}
	targetUser, err := st.NonTx().CreateUser("bootstrap-race-target", model.RoleUser)
	if err != nil {
		t.Fatalf("CreateUser(target): %v", err)
	}
	bystanderUser, err := st.NonTx().CreateUser("bootstrap-race-bystander", model.RoleUser)
	if err != nil {
		t.Fatalf("CreateUser(bystander): %v", err)
	}
	admin := mustCreateSession(t, srv.sessions, adminUser.ID, adminUser.Username, adminUser.Role)
	target := mustCreateSession(t, srv.sessions, targetUser.ID, targetUser.Username, targetUser.Role)
	bystander := mustCreateSession(t, srv.sessions, bystanderUser.ID, bystanderUser.Username, bystanderUser.Role)
	targetConn := registerRemoteTestConn(handler, target.ID, "198.51.100.70")
	bystanderConn := registerRemoteTestConn(handler, bystander.ID, "198.51.100.70")
	if err := st.NonTx().CreateBootstrapToken("bootstrap-race-hash"); err != nil {
		t.Fatalf("CreateBootstrapToken: %v", err)
	}

	srv.remoteModerationMu.Lock()
	srv.bootstrapMu.Lock()
	done := make(chan struct{})
	go func() {
		srv.handleBanUser(handler, admin.ID, &pb.BanUserRequest{
			UserID:         targetUser.ID,
			IPBanSessionID: target.ID,
		}, st, &nopConn{})
		close(done)
	}()
	srv.remoteModerationMu.Unlock()

	deadline := time.Now().Add(time.Second)
	for srv.remoteModerationMu.TryLock() {
		srv.remoteModerationMu.Unlock()
		if time.Now().After(deadline) {
			srv.bootstrapMu.Unlock()
			t.Fatal("ban handler did not reach moderation policy section")
		}
		runtime.Gosched()
	}

	tx, err := st.Tx(context.Background())
	if err != nil {
		srv.bootstrapMu.Unlock()
		t.Fatalf("Tx: %v", err)
	}
	owner, err := tx.ProvisionBootstrapUser("bootstrap-race-hash", "bootstrap-race-owner", "bootstrap-race-personal", time.Now().UTC())
	if err != nil {
		_ = tx.Rollback()
		srv.bootstrapMu.Unlock()
		t.Fatalf("ProvisionBootstrapUser: %v", err)
	}
	if err := tx.Commit(); err != nil {
		srv.bootstrapMu.Unlock()
		t.Fatalf("Commit: %v", err)
	}
	ownerSession := mustCreateSession(t, srv.sessions, owner.ID, owner.Username, owner.Role)
	ownerConn := registerRemoteTestConn(handler, ownerSession.ID, "198.51.100.70")
	srv.bootstrapMu.Unlock()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("IP ban did not complete after bootstrap provisioning")
	}
	requireClosed(t, targetConn)
	requireClosed(t, bystanderConn)
	requireOpen(t, ownerConn)
}

func TestAccountBanNeverPersistsIPOrReason(t *testing.T) {
	srv, st, handler := newTestServer(t)
	adminUser, err := st.NonTx().CreateUser("privacy-admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(admin): %v", err)
	}
	targetUser, err := st.NonTx().CreateUser("privacy-target", model.RoleUser)
	if err != nil {
		t.Fatalf("CreateUser(target): %v", err)
	}
	admin := mustCreateSession(t, srv.sessions, adminUser.ID, adminUser.Username, adminUser.Role)
	target := mustCreateSession(t, srv.sessions, targetUser.ID, targetUser.Username, targetUser.Role)
	registerRemoteTestConn(handler, target.ID, "203.0.113.77")
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	srv.handleBanUser(handler, admin.ID, &pb.BanUserRequest{
		UserID: targetUser.ID,
		Reason: "reason contains 203.0.113.77",
	}, st, &nopConn{})

	bans, _, err := st.NonTx().ListActiveBans(0, datastore.MaxBanPageSize)
	if err != nil {
		t.Fatalf("ListActiveBans: %v", err)
	}
	if len(bans) != 1 || bans[0].UserID != targetUser.ID || bans[0].IP != "" {
		t.Fatalf("account ban persisted unexpected identity data: %#v", bans)
	}
	if strings.Contains(logs.String(), "203.0.113.77") || strings.Contains(logs.String(), "reason contains") {
		t.Fatalf("moderation log leaked IP or reason: %q", logs.String())
	}
}

func TestIPBanRejectsAuthenticationBeforeSessionPublication(t *testing.T) {
	srv, st, handler := newTestServerWithConfig(t, func(cfg *Config) { cfg.AllowNoToken = true })
	if err := st.NonTx().CreateIPBan("192.0.2.88", 1, time.Time{}); err != nil {
		t.Fatalf("CreateIPBan: %v", err)
	}
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	wrapped := remoteAddrConn{Conn: serverConn, remote: testAddr("[::ffff:192.0.2.88]:41000")}
	done := make(chan struct{})
	go func() {
		srv.handleControlConn(handler, wrapped, st)
		close(done)
	}()
	if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{Username: "blocked-ip-user"}}); err != nil {
		t.Fatalf("WriteControlMessage: %v", err)
	}
	response, err := protocol.ReadControlMessage(clientConn)
	if err != nil {
		t.Fatalf("ReadControlMessage: %v", err)
	}
	if response.ErrorResponse == nil || response.ErrorResponse.Code != 4 {
		t.Fatalf("response = %#v, want banned error", response)
	}
	_ = clientConn.Close()
	<-done
	if got := srv.sessions.Count(); got != 0 {
		t.Fatalf("published sessions = %d, want 0", got)
	}
	user, err := st.NonTx().GetUserByUsername("blocked-ip-user")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if user != nil {
		t.Fatal("IP-banned unauthenticated connection provisioned an account")
	}
}

func TestPendingIPBanWinsActivationRaceExceptBootstrapBypass(t *testing.T) {
	srv, _, _ := newTestServer(t)
	const ip = "192.0.2.99"

	reservation, err := srv.sessions.Reserve(10)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	defer reservation.Release()
	if _, err := reservation.Prepare("ordinary", model.RoleUser, 0); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	srv.beginSessionBanCheck(10)
	srv.beginIPBanCheck(ip)
	srv.sessionBanMu.Lock()
	srv.ipBanPending[ip] = true
	srv.sessionBanMu.Unlock()
	session, banned := srv.activateAfterBanChecks(10, ip, reservation, false, false, false, nil)
	if !banned || session != nil {
		t.Fatalf("activation = (%#v, %t), want blocked", session, banned)
	}

	bootstrapReservation, err := srv.sessions.Reserve(11)
	if err != nil {
		t.Fatalf("Reserve bootstrap: %v", err)
	}
	defer bootstrapReservation.Release()
	if _, err := bootstrapReservation.Prepare("bootstrap", model.RoleAdmin, 0); err != nil {
		t.Fatalf("Prepare bootstrap: %v", err)
	}
	srv.beginSessionBanCheck(11)
	srv.beginIPBanCheck(ip)
	srv.sessionBanMu.Lock()
	srv.ipBanPending[ip] = true
	srv.sessionBanMu.Unlock()
	session, banned = srv.activateAfterBanChecks(11, ip, bootstrapReservation, false, true, true, nil)
	if banned || session == nil {
		t.Fatalf("bootstrap activation = (%#v, %t), want allowed", session, banned)
	}
}
