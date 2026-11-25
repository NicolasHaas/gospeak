package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
)

// This deliberately implements the old derivation independently of production.
func oldBootstrapPersonalToken(setup, username string) string {
	mac := hmac.New(sha256.New, []byte(setup))
	_, _ = mac.Write([]byte("gospeak bootstrap personal token\x00"))
	_, _ = mac.Write([]byte(username))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestFinalizedOwnerIndependentOfOldSetupFile(t *testing.T) {
	srv, st, path := newBootstrapTestServer(t)
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatal(err)
	}
	setup := readBootstrapToken(t, path)
	response, err := authenticateBootstrap(t, srv, st, "independent-owner", setup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authenticateBootstrap(t, srv, st, "independent-owner", response.AutoToken); err != nil {
		t.Fatal(err)
	}
	derived := oldBootstrapPersonalToken(setup, "independent-owner")
	if derived == response.AutoToken {
		t.Fatal("old setup file and public username still derive finalized personal token")
	}
	if user, err := st.NonTx().GetUserByPersonalTokenHash(crypto.HashToken(derived)); err != nil || user != nil {
		t.Fatalf("old derivation resolves persisted owner: found=%t err=%v", user != nil, err)
	}
	if _, err := authenticateBootstrap(t, srv, st, "independent-owner", derived); err == nil {
		t.Fatal("old setup-derived credential authenticated")
	}
}
