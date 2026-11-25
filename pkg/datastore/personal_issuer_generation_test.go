package datastore_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestRecoveryGrantIssuerGenerationAndUpgrade(t *testing.T) {
	for _, mode := range []string{"rotate", "revoke-recover", "upgrade"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "grants.db")
			st, err := datastore.NewProviderFactory(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			issuer := personalRecoveryIssuer(t, st)
			target, err := st.NonTx().CreateUser("target", model.RoleUser)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
				return tx.IssuePersonalRecovery(target.ID, 0, "stolen-grant", now, issuer)
			}) {
				t.Fatal("grant not issued")
			}
			if mode == "upgrade" {
				if _, err := st.DB.ExecContext(context.Background(), "ALTER TABLE users DROP COLUMN recovery_issuer_version; UPDATE schema_migrations SET version = 15"); err != nil {
					t.Fatal(err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				st, err = datastore.NewProviderFactory(path)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				actor, err := st.NonTx().GetUserByID(issuer)
				if err != nil {
					t.Fatal(err)
				}
				hash := actor.PersonalTokenHash
				if mode == "revoke-recover" {
					if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) { return tx.ReplacePersonalToken(issuer, 1, hash, "", now) }) {
						t.Fatal("issuer revocation failed")
					}
					hash = ""
				}
				version, err := st.NonTx().PersonalTokenVersion(issuer)
				if err != nil {
					t.Fatal(err)
				}
				if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
					return tx.ReplacePersonalToken(issuer, version, hash, crypto.HashToken("operator-recovered"), now)
				}) {
					t.Fatal("issuer replacement failed")
				}
			}
			if credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
				return tx.RedeemPersonalRecovery(target.ID, "stolen-grant", crypto.HashToken("attacker"), now)
			}) {
				t.Fatal("stolen unversioned/old-generation grant revived")
			}
			if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
				return tx.IssuePersonalRecovery(target.ID, 1, "fresh-grant", now, issuer)
			}) {
				t.Fatal("fresh grant not issued")
			}
			if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
				return tx.RedeemPersonalRecovery(target.ID, "fresh-grant", crypto.HashToken("legitimate"), now)
			}) {
				t.Fatal("new-generation grant rejected")
			}
		})
	}
}
