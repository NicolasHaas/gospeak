package server

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
)

func TestLegacyBootstrapOwnerPreservedAcrossUpgrade(t *testing.T) {
	for _, finalized := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "finalized"}[finalized], func(t *testing.T) {
			srv, st, path := newBootstrapTestServer(t)
			if err := srv.ensureAdminToken(st); err != nil {
				t.Fatal(err)
			}
			setup := readBootstrapToken(t, path)
			personal := oldBootstrapPersonalToken(setup, "legacy-owner")
			tx, err := st.Tx(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			owner, err := tx.ProvisionBootstrapUser(crypto.HashToken(setup), "legacy-owner", crypto.HashToken(personal), time.Now().UTC())
			if err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if finalized {
				if _, err := st.NonTx().FinalizeBootstrapToken(owner.ID); err != nil {
					t.Fatal(err)
				}
			}
			// Reopen an actual v12 schema instead of mocking a legacy flag.
			provider := st.(*datastore.ProviderFactory)
			if _, err := provider.DB.ExecContext(context.Background(), "ALTER TABLE tokens DROP COLUMN bootstrap_key; UPDATE schema_migrations SET version = 12"); err != nil {
				t.Fatal(err)
			}
			if err := provider.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := datastore.NewProviderFactory(srv.cfg.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			restarted := New(srv.cfg, Dependencies{Store: reopened})
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			err = restarted.ensureAdminToken(reopened)
			slog.SetDefault(previous)
			if err != nil || !strings.Contains(logs.String(), "legacy bootstrap owner credential preserved") {
				t.Fatalf("legacy startup warning missing: err=%v logs=%s", err, logs.String())
			}
			if strings.Contains(logs.String(), setup) || strings.Contains(logs.String(), personal) {
				t.Fatal("startup warning exposed a credential")
			}
			if !finalized {
				retry, err := authenticateBootstrap(t, restarted, reopened, "legacy-owner", setup)
				if err != nil || retry.AutoToken != personal {
					t.Fatalf("legacy retry changed credential: %v", err)
				}
			}
			if _, err := authenticateBootstrap(t, restarted, reopened, "legacy-owner", personal); err != nil {
				t.Fatal(err)
			}
			if protected, err := reopened.NonTx().IsBootstrapUser(owner.ID); err != nil || !protected {
				t.Fatalf("owner marker lost: %t %v", protected, err)
			}
			if legacy, err := reopened.NonTx().HasLegacyBootstrapOwner(); err != nil || !legacy {
				t.Fatalf("legacy risk silently marked remediated: %t %v", legacy, err)
			}
		})
	}
}
