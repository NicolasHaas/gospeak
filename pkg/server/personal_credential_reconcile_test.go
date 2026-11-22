package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
)

// Real control authentication over verified loopback TLS; no lifecycle mutation
// endpoint is implied by this fixture's direct writer transaction.
func personalCredentialListener(t *testing.T, srv *Server, handler *ControlHandler, st datastore.DataProviderFactory, loseACK ...bool) (string, string) {
	t.Helper()
	key, err := crypto.GenerateMediaKey(srv.cfg.MediaCipher)
	if err != nil {
		t.Fatal(err)
	}
	srv.voiceKey = key
	certificate := httptest.NewTLSServer(nil)
	t.Cleanup(certificate.Close)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", certificate.TLS)
	if err != nil {
		t.Fatal(err)
	}
	var clients sync.WaitGroup
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if len(loseACK) > 0 && loseACK[0] {
				conn = personalACKLossConn{conn}
			}
			clients.Add(1)
			go func() { defer clients.Done(); srv.handleControlConn(handler, conn, st) }()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-accepted
		handler.mu.RLock()
		conns := make([]*controlClient, 0, len(handler.connMap))
		for _, c := range handler.connMap {
			conns = append(conns, c)
		}
		handler.mu.RUnlock()
		for _, c := range conns {
			_ = c.Close()
		}
		joined := make(chan struct{})
		go func() { clients.Wait(); close(joined) }()
		select {
		case <-joined:
		case <-time.After(4 * time.Second):
			t.Error("credential fixture handlers did not join")
		}
	})
	return listener.Addr().String(), client.SPKIFingerprint(certificate.Certificate())
}

type personalACKLossConn struct{ net.Conn }

func (c personalACKLossConn) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte(`"rotate_personal_token_response"`)) || bytes.Contains(data, []byte(`"redeem_personal_recovery_response"`)) {
		_ = c.Close()
		return 0, io.ErrClosedPipe
	}
	return c.Conn.Write(data)
}

func TestPersonalCredentialReconciliationBeforeAndAfterCommit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, st, handler := newTestServer(t)
	u, err := st.NonTx().CreateUser("reconcile-person", model.RoleModerator)
	if err != nil {
		t.Fatal(err)
	}
	old, err := crypto.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := crypto.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := crypto.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.NonTx().UpdateUserPersonalToken(u.ID, crypto.HashToken(old), time.Now()); err != nil {
		t.Fatal(err)
	}
	addr, pin := personalCredentialListener(t, srv, handler, st)
	bs := client.NewBookmarkStore()
	// Seed only the isolated XDG path so Load never consults real legacy files.
	if err := bs.Save(); err != nil {
		t.Fatal(err)
	}
	p := client.PendingPersonalCredential{ControlAddr: addr, Username: u.Username, AccountID: u.ID, ServerIdentity: pin, Attempt: attempt, OldToken: old, Candidate: candidate, Version: 1}
	if err := bs.StagePersonalCredential(p, true); err != nil {
		t.Fatal(err)
	}
	if err := bs.ReconcilePersonalCredential(context.Background(), p); err == nil {
		t.Fatal("uncommitted candidate authenticated")
	}
	pending, found, err := bs.PendingPersonalCredential(addr, u.Username, u.ID)
	if err != nil || !found || pending != p {
		t.Fatal("candidate rejection discarded pending state")
	}
	// Model commit followed by a lost response/crash. No acknowledgement is used.
	srv.remoteModerationMu.Lock()
	tx, err := st.Tx(context.Background())
	if err != nil {
		srv.remoteModerationMu.Unlock()
		t.Fatal(err)
	}
	changed, err := tx.ReplacePersonalToken(u.ID, 1, crypto.HashToken(old), crypto.HashToken(candidate), time.Now())
	if err == nil && changed {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	srv.remoteModerationMu.Unlock()
	if err != nil || !changed {
		t.Fatal("replacement did not commit")
	}
	restarted := client.NewBookmarkStore()
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	saved, found, err := restarted.PendingPersonalCredential(addr, u.Username, u.ID)
	if err != nil || !found || saved != p {
		t.Fatal("restart lost original candidate")
	}
	if err := restarted.ReconcilePersonalCredential(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.PendingCredentials) != 0 || len(restarted.Bookmarks) != 1 || restarted.Bookmarks[0].Token != candidate {
		t.Fatal("verified candidate was not promoted")
	}
	ctrl, err := client.NewControlClient(addr, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	if _, err := ctrl.Authenticate(old, u.Username); err == nil {
		t.Fatal("old personal credential still authenticates")
	}
}

func TestPersonalCredentialReconciliationTrustFailurePreservesCandidate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, st, handler := newTestServer(t)
	u, err := st.NonTx().CreateUser("reconcile-trust", model.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := crypto.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := crypto.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.NonTx().UpdateUserPersonalToken(u.ID, crypto.HashToken(candidate), time.Now()); err != nil {
		t.Fatal(err)
	}
	addr, _ := personalCredentialListener(t, srv, handler, st)
	// Use a syntactically valid different pin, not disabled verification.
	wrongPin := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	p := client.PendingPersonalCredential{ControlAddr: addr, Username: u.Username, AccountID: u.ID, ServerIdentity: wrongPin, Attempt: attempt, OldToken: "historical-old", Candidate: candidate, Version: 0}
	bs := client.NewBookmarkStore()
	if err := bs.Save(); err != nil {
		t.Fatal(err)
	}
	if err := bs.StagePersonalCredential(p, true); err != nil {
		t.Fatal(err)
	}
	if err := bs.ReconcilePersonalCredential(context.Background(), p); err == nil {
		t.Fatal("wrong TLS identity authenticated candidate")
	}
	pending, found, err := bs.PendingPersonalCredential(addr, u.Username, u.ID)
	if err != nil || !found || pending != p {
		t.Fatal("trust error lost staged candidate")
	}
}
