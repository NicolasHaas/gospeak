package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
)

func recoveryFixture(t *testing.T, owner, finalized bool) (string, *model.User) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server.db")
	st, err := datastore.NewProviderFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	var user *model.User
	if owner {
		if err := st.NonTx().CreateBootstrapToken("setup"); err != nil {
			t.Fatal(err)
		}
		tx, err := st.Tx(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.BootstrapPersonalTokenKey("setup"); err != nil {
			t.Fatal(err)
		}
		user, err = tx.ProvisionBootstrapUser("setup", "owner", crypto.HashToken("old"), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if finalized {
			if _, err := st.NonTx().FinalizeBootstrapToken(user.ID); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		channel := &model.Channel{Name: "local-admin-scope"}
		if err := st.NonTx().CreateChannel(channel); err != nil {
			t.Fatal(err)
		}
		user, err = st.NonTx().CreateUserWithChannelScope("admin", model.RoleAdmin, channel.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.NonTx().UpdateUserPersonalToken(user.ID, crypto.HashToken("old"), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return path, user
}
func recoveryArgs(path string, u *model.User) []string {
	return []string{"-db", path, "-user-id", fmt.Sprint(u.ID), "-username", u.Username, "-expected-version", "1", "-stopped-service"}
}

func TestLocalRecoveryPreservesOwnerAndExhaustsPendingSetup(t *testing.T) {
	for _, state := range []string{"pending-owner", "final-owner", "admin"} {
		t.Run(state, func(t *testing.T) {
			path, u := recoveryFixture(t, state != "admin", state == "final-owner")
			candidate, err := crypto.GenerateToken()
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := run(recoveryArgs(path, u), strings.NewReader(candidate+"\n"), &out); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), candidate) {
				t.Fatal("recovery printed secret")
			}
			st, err := datastore.NewProviderFactory(path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			got, err := st.NonTx().GetUserByPersonalTokenHash(crypto.HashToken(candidate))
			if err != nil || got == nil || got.ID != u.ID || got.Username != u.Username || got.Role != u.Role || got.ChannelScope != u.ChannelScope {
				t.Fatal("recovery changed account identity or rights")
			}
			if old, err := st.NonTx().GetUserByPersonalTokenHash(crypto.HashToken("old")); err != nil || old != nil {
				t.Fatal("old credential survived local recovery")
			}
			if state != "admin" {
				owner, err := st.NonTx().IsBootstrapUser(u.ID)
				if err != nil || !owner {
					t.Fatal("owner marker lost")
				}
				status, err := st.NonTx().BootstrapTokenState()
				if err != nil || status != datastore.BootstrapTokenFinalized {
					t.Fatal("setup remained pending")
				}
				var key string
				if err := st.DB.QueryRow("SELECT bootstrap_key FROM tokens WHERE kind = 1").Scan(&key); err != nil || key != "" {
					t.Fatal("pending retry key survived")
				}
			}
			out.Reset()
			if err := run(recoveryArgs(path, u), strings.NewReader(candidate), &out); err == nil {
				t.Fatal("stale local retry changed credential")
			}
		})
	}
}

func TestLocalRecoveryRefusesWrongTargetInvalidInputAndUnstoppedService(t *testing.T) {
	path, u := recoveryFixture(t, true, false)
	candidate, err := crypto.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, which := range []string{"not-stopped", "wrong-name", "stale-version", "malformed", "oversized", "shared-input"} {
		t.Run(which, func(t *testing.T) {
			args := recoveryArgs(path, u)
			input := candidate
			var out bytes.Buffer
			switch which {
			case "not-stopped":
				args = args[:len(args)-1]
			case "wrong-name":
				args[5] = "different"
			case "stale-version":
				args[7] = "0"
			case "malformed":
				input = "not-a-generated-token"
			case "oversized":
				input = strings.Repeat("a", 10000)
			case "shared-input":
				secretPath := filepath.Join(t.TempDir(), "shared")
				if err := os.WriteFile(secretPath, []byte(candidate), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(secretPath, 0644); err != nil { // #nosec G302 -- Negative fixture deliberately makes synthetic input world-readable.
					t.Fatal(err)
				}
				f, err := os.Open(secretPath) // #nosec G304 -- Path is the synthetic shared-input fixture under t.TempDir.
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if err := run(args, f, &out); err == nil {
					t.Fatal("shared secret input accepted")
				}
				return
			}
			err := run(args, strings.NewReader(input), &out)
			if err == nil {
				t.Fatal("invalid recovery accepted")
			}
			if strings.Contains(out.String(), candidate) || strings.Contains(err.Error(), candidate) {
				t.Fatal("error leaked secret")
			}
		})
	}
	st, err := datastore.NewProviderFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got, err := st.NonTx().GetUserByPersonalTokenHash(crypto.HashToken("old")); err != nil || got == nil {
		t.Fatal("refused recovery changed hash")
	}
	status, err := st.NonTx().BootstrapTokenState()
	if err != nil || status != datastore.BootstrapTokenPending {
		t.Fatal("refused recovery finalized setup")
	}
}

func TestLocalRecoveryFinalizationFailureRollsBackReplacement(t *testing.T) {
	path, u := recoveryFixture(t, true, false)
	st, err := datastore.NewProviderFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec("CREATE TRIGGER reject_finalization BEFORE UPDATE ON tokens BEGIN SELECT RAISE(ABORT,'synthetic failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	candidate, err := crypto.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(recoveryArgs(path, u), strings.NewReader(candidate), &out); err == nil {
		t.Fatal("failed finalization accepted")
	}
	st, err = datastore.NewProviderFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got, err := st.NonTx().GetUserByPersonalTokenHash(crypto.HashToken("old")); err != nil || got == nil {
		t.Fatal("failed finalization changed credential")
	}
	if got, err := st.NonTx().GetUserByPersonalTokenHash(crypto.HashToken(candidate)); err != nil || got != nil {
		t.Fatal("failed finalization installed replacement")
	}
}
