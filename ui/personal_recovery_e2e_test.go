package ui

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
	"github.com/NicolasHaas/gospeak/pkg/server"
)

type credentialReadyLog struct {
	slog.Handler
	ready chan struct{}
}

func (h credentialReadyLog) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "GoSpeak server running" {
		close(h.ready)
	}
	return h.Handler.Handle(ctx, r)
}

func startCredentialUIRealServer(t *testing.T, cfg server.Config, st datastore.DataProviderFactory) (string, func()) {
	t.Helper()
	logger := slog.Default()
	ready := make(chan struct{})
	slog.SetDefault(slog.New(credentialReadyLog{Handler: slog.NewTextHandler(io.Discard, nil), ready: ready}))
	srv := server.New(cfg, server.Dependencies{Store: st})
	done := make(chan error, 1)
	go func() { done <- srv.Run() }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			srv.Shutdown()
			if err := <-done; err != nil {
				t.Error(err)
			}
			slog.SetDefault(logger)
		})
	}
	select {
	case <-ready:
	case err := <-done:
		done <- err
		stop()
		t.Fatalf("server startup failed: %v", err)
	case <-time.After(5 * time.Second):
		stop()
		t.Fatal("server startup did not complete")
	}
	data, err := os.ReadFile(filepath.Join(cfg.DataDir, "server.crt"))
	if err != nil {
		stop()
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		stop()
		t.Fatal("missing fixture certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	return client.SPKIFingerprint(cert), stop
}

// No fake credential endpoint: the production dialog talks to Server.Run and
// its real SQLite writer. UI completion loss is separate from the wire-ACK loss
// exercised by the server package's CAS-loss/restart regression.
func TestPersonalCredentialPendingRecoveryRealServer(t *testing.T) {
	isolateAudioSettingsConfig(t)
	f := test.NewApp()
	defer f.Quit()
	completions := make(chan func(), 32)
	fyne.SetCurrentApp(&audioSettingsApp{App: f, driver: &audioSettingsDriver{Driver: f.Driver(), completion: completions}})
	defer fyne.SetCurrentApp(f)
	cfg := server.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.DBPath = filepath.Join(cfg.DataDir, "test.db")
	cfg.VoiceAddr = "127.0.0.1:0"
	cfg.EnableScreenShare = false
	// Reserve a loopback port; StartControl owns it after this listener closes.
	reservation, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ControlAddr = reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := datastore.NewProviderFactory(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }() // st is replaced at each genuine service restart.
	if err := st.NonTx().CreateBootstrapToken("synthetic-setup"); err != nil {
		t.Fatal(err)
	}
	tx, err := st.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := tx.ProvisionBootstrapUser("synthetic-setup", "owner", crypto.HashToken("owner-personal"), time.Now())
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.FinalizeBootstrapToken(owner.ID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	target, err := st.NonTx().CreateUser("person", model.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.NonTx().UpdateUserPersonalToken(target.ID, crypto.HashToken("old-personal"), time.Now()); err != nil {
		t.Fatal(err)
	}
	pin, stop := startCredentialUIRealServer(t, cfg, st)
	defer stop()
	ctx := context.Background()
	c, auth, err := client.OpenPersonalTokenControl(ctx, cfg.ControlAddr, pin, target.Username, "old-personal")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	candidate, _ := crypto.GenerateToken()
	attempt, _ := crypto.GenerateToken()
	p := client.PendingPersonalCredential{ControlAddr: cfg.ControlAddr, ServerIdentity: pin, Username: target.Username, AccountID: target.ID, OldToken: "old-personal", Candidate: candidate, Attempt: attempt, Version: auth.CredentialVersion}
	bs := client.NewBookmarkStore()
	if err := bs.Save(); err != nil {
		t.Fatal(err)
	}
	if err := bs.StagePersonalCredential(p, true); err != nil {
		t.Fatal(err)
	}
	admin, _, err := client.OpenPersonalTokenControl(ctx, cfg.ControlAddr, pin, owner.Username, "owner-personal")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.PersonalTokenExchange(ctx, &pb.ControlMessage{RevokePersonalTokenReq: &pb.RevokePersonalTokenRequest{TargetID: target.ID, Version: p.Version}}); err != nil {
		t.Fatal(err)
	}
	if err := bs.SubmitPersonalCredential(ctx, p); !client.PersonalCredentialRejected(err) {
		t.Fatalf("CAS loser old token accepted: %v", err)
	}
	if err := client.VerifyPersonalCredential(ctx, p); !client.PersonalCredentialRejected(err) {
		t.Fatalf("CAS loser candidate accepted: %v", err)
	}
	status, err := admin.PersonalTokenExchange(ctx, &pb.ControlMessage{PersonalTokenStatusReq: &pb.PersonalTokenStatusRequest{TargetID: target.ID}})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := admin.PersonalTokenExchange(ctx, &pb.ControlMessage{IssuePersonalRecoveryReq: &pb.IssuePersonalRecoveryRequest{TargetID: target.ID, Version: status.PersonalTokenStatusResp.Version}})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{fyneApp: f, window: f.NewWindow("Real recovery"), engine: client.NewEngine(), bookmarks: bs}
	defer a.window.Close()
	a.showPersonalCredentialDialog()
	buttons, entries := credentialDialogControls(a.window)
	entries[4].SetText(issued.IssuePersonalRecoveryResp.Grant)
	test.Tap(buttons["Redeem recovery grant"])
	if !a.credentialBusy {
		t.Fatal("pending CAS loser blocks the real fresh-grant recovery action")
	}
	next := func() func() {
		t.Helper()
		select {
		case fn := <-completions:
			return fn
		case <-time.After(5 * time.Second):
			t.Fatal("credential worker did not complete")
			return nil
		}
	}
	next()()             // private recovery stage, then actual production redemption
	completion := next() // network request finished; simulate UI process loss before callback
	restarted := client.NewBookmarkStore()
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	q, found, err := restarted.PendingPersonalCredential(p.ControlAddr, p.Username, p.AccountID)
	if err != nil || !found || q.Candidate != p.Candidate || q.Attempt == p.Attempt {
		t.Fatal("UI recovery did not retain candidate/new attempt")
	}
	if err := restarted.ReconcilePersonalCredential(ctx, q); err != nil {
		t.Fatal(err)
	}
	completion() // old callback cannot overwrite the restarted finalized bookmark
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.PendingCredentials) != 0 || restarted.Bookmarks[0].Token != p.Candidate {
		t.Fatal("restart promotion failed")
	}
	user, err := st.NonTx().GetUserByPersonalTokenHash(crypto.HashToken(p.Candidate))
	if err != nil || user == nil || user.ID != target.ID || user.Role != target.Role || user.ChannelScope != target.ChannelScope {
		t.Fatal("identity/authority changed")
	}
	stop() // Join the service before invoking the real operator-local tool.
	binary := filepath.Join(t.TempDir(), "recover-token")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../cmd/recover-token") // #nosec G204 -- Build the fixed local recovery command into a test-owned temporary directory.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build local recovery: %v: %s", err, output)
	}
	peer, err := st.NonTx().CreateUser("local-admin", model.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.NonTx().UpdateUserPersonalToken(peer.ID, crypto.HashToken("old-local-admin"), time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, local := range []*model.User{owner, peer} {
		old := "owner-personal"
		if local.ID == peer.ID {
			old = "old-local-admin"
		}
		version, err := st.NonTx().PersonalTokenVersion(local.ID)
		if err != nil {
			t.Fatal(err)
		}
		candidate, _ := crypto.GenerateToken()
		attempt, _ := crypto.GenerateToken()
		recovered, _ := crypto.GenerateToken()
		p := client.PendingPersonalCredential{ControlAddr: cfg.ControlAddr, ServerIdentity: pin, Username: local.Username, AccountID: local.ID, OldToken: old, Candidate: candidate, Attempt: attempt, Version: version}
		if err := restarted.StagePersonalCredential(p, true); err != nil {
			t.Fatal(err)
		}
		// A stopped process has no surviving SQLite pool/WAL mappings. Keeping
		// this pool across the CLI can read a stale pre-recovery credential.
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := st.NonTx().GetUserByID(local.ID); err == nil {
			t.Fatal("stopped service database is still usable during operator recovery")
		}
		command := exec.CommandContext(t.Context(), binary, "-db", cfg.DBPath, "-user-id", strconv.FormatInt(local.ID, 10), "-username", local.Username, "-expected-version", strconv.FormatInt(version, 10), "-stopped-service") // #nosec G204 -- Execute only the local binary just built; arguments are synthetic fixture identities.
		command.Stdin = strings.NewReader(recovered + "\n")                                                                                                                                                                       // private pipe, never argv/environment
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("local recovery: %v: %s", err, output)
		}
		st, err = datastore.NewProviderFactory(cfg.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		u, err := st.NonTx().GetUserByPersonalTokenHash(crypto.HashToken(recovered))
		if err != nil || u == nil || u.ID != local.ID {
			t.Fatal("restarted database did not observe the committed operator credential")
		}
		if current, err := st.NonTx().PersonalTokenVersion(local.ID); err != nil || current != version+1 {
			t.Fatal("operator recovery did not durably advance the expected credential version")
		}
		newPin, stopLocal := startCredentialUIRealServer(t, cfg, st)
		func() {
			defer stopLocal()
			if newPin != pin {
				t.Fatal("restart changed trust identity")
			}
			if err := client.VerifyPersonalCredential(ctx, p); !client.PersonalCredentialRejected(err) {
				t.Fatalf("old pending candidate was not rejected: %v", err)
			}
			window := f.NewWindow("Operator recovery")
			defer window.Close()
			localApp := &App{fyneApp: f, window: window, engine: client.NewEngine(), bookmarks: restarted}
			localApp.showPersonalCredentialDialog()
			controls, fields := credentialDialogControls(window)
			fields[2].SetText(recovered)
			test.Tap(controls["Verify operator-recovered credential"])
			if !localApp.credentialBusy {
				t.Fatal("pending state blocked local recovery")
			}
			next()()
			if localApp.credentialBusy {
				t.Fatal("local recovery worker not released")
			}
			if err := restarted.Load(); err != nil {
				t.Fatal(err)
			}
			if len(restarted.PendingCredentials) != 0 {
				t.Fatal("verified local credential did not resolve pending")
			}
			found := false
			for _, b := range restarted.Bookmarks {
				if b.Username == local.Username && b.Token == recovered {
					found = true
				}
			}
			if !found {
				t.Fatal("operator-recovered credential not privately saved")
			}
			u, err := st.NonTx().GetUserByPersonalTokenHash(crypto.HashToken(recovered))
			if err != nil || u == nil || u.ID != local.ID || u.Role != local.Role || u.ChannelScope != local.ChannelScope {
				t.Fatal("local recovery changed identity")
			}
			marked, err := st.NonTx().IsBootstrapUser(local.ID)
			if err != nil || marked != (local.ID == owner.ID) {
				t.Fatal("local recovery changed owner marker")
			}
		}()
	}
}
