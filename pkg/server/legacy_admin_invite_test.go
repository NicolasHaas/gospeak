package server

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
)

func TestLegacyLoggedAdminInviteUpgradeDoesNotRestartBootstrap(t *testing.T) {
	for _, populated := range []bool{false, true} {
		name := "unused"
		if populated {
			name = "redeemed"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			srv, _, _ := newTestServerWithConfig(t, func(cfg *Config) { cfg.DataDir = dir })
			path := filepath.Join(dir, "legacy.db")
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			fixture, err := os.ReadFile("../datastore/testdata/v0.3.0-admin-invite.sql")
			if err != nil {
				_ = raw.Close()
				t.Fatal(err)
			}
			_, fixtureErr := raw.ExecContext(t.Context(), string(fixture))
			wantUses := 0
			if fixtureErr == nil && populated {
				_, fixtureErr = raw.ExecContext(t.Context(), "INSERT INTO users (username, role, personal_token_hash) VALUES ('legacy-admin', 2, ?); UPDATE tokens SET use_count = 1", crypto.HashToken("synthetic-independent-personal"))
				wantUses = 1
			}
			closeErr := raw.Close()
			if fixtureErr != nil || closeErr != nil {
				t.Fatalf("legacy fixture: %v; close: %v", fixtureErr, closeErr)
			}
			for range 2 {
				f, err := datastore.NewProviderFactory(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := srv.ensureAdminToken(f); err != nil {
					_ = f.Close()
					t.Fatal(err)
				}
				if response := authenticateControl(t, srv, f, "intruder", "synthetic-v0.3.0-logged-admin-invite"); response.ErrorResponse == nil {
					_ = f.Close()
					t.Fatalf("logged invite authenticated: %+v", response)
				}
				if populated {
					response := authenticateControl(t, srv, f, "legacy-admin", "synthetic-independent-personal")
					if response.AuthResponse == nil || response.AuthResponse.Username != "legacy-admin" || response.AuthResponse.Role != "admin" {
						_ = f.Close()
						t.Fatalf("existing personal credential failed: %+v", response)
					}
				}
				state, stateErr := f.NonTx().BootstrapTokenState()
				var count, uses int
				var expiry string
				rowErr := f.DB.QueryRowContext(t.Context(), "SELECT COUNT(*), use_count, expires_at FROM tokens").Scan(&count, &uses, &expiry)
				closeErr := f.Close()
				if stateErr != nil || state != datastore.BootstrapTokenAbsent || rowErr != nil || count != 1 || uses != wantUses || expiry != "1970-01-01 00:00:01" || closeErr != nil {
					t.Fatalf("bootstrap recreated/legacy lifecycle changed: state=%v stateErr=%v count=%d uses=%d expiry=%q rowErr=%v closeErr=%v", state, stateErr, count, uses, expiry, rowErr, closeErr)
				}
				if _, err := os.Stat(filepath.Join(dir, bootstrapAdminCredentialName)); !os.IsNotExist(err) {
					t.Fatalf("upgrade generated bootstrap credential: %v", err)
				}
			}
		})
	}
}
