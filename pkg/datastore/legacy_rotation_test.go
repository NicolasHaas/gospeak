package datastore_test

import (
	"context"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
)

func TestLegacyOwnerExplicitReplacementClearsWarningAtomically(t *testing.T) {
	st, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.NonTx().CreateBootstrapToken("legacy-setup"); err != nil {
		t.Fatal(err)
	}
	tx, err := st.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := tx.ProvisionBootstrapUser("legacy-setup", "legacy-owner", "old", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		legacy, err := st.NonTx().HasLegacyBootstrapOwner()
		if err != nil || legacy != want {
			t.Fatalf("legacy warning=%t want=%t err=%v", legacy, want, err)
		}
		marked, err := st.NonTx().IsBootstrapUser(owner.ID)
		if err != nil || !marked {
			t.Fatal("owner marker lost")
		}
	}
	check(true)
	if _, err := st.DB.ExecContext(context.Background(), `CREATE TRIGGER reject_replaced_key BEFORE UPDATE OF bootstrap_key ON tokens BEGIN SELECT RAISE(ABORT, 'injected key retirement failure'); END`); err != nil {
		t.Fatal(err)
	}
	tx, err = st.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.ReplacePersonalToken(owner.ID, 1, "old", "replacement", time.Now()); err == nil || changed {
		t.Fatal("key retirement failure accepted")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	got, err := st.NonTx().GetUserByID(owner.ID)
	if err != nil || got.PersonalTokenHash != "old" {
		t.Fatal("key failure did not roll back replacement")
	}
	check(true)
	if _, err := st.DB.ExecContext(context.Background(), "DROP TRIGGER reject_replaced_key"); err != nil {
		t.Fatal(err)
	}
	tx, err = st.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	changed, err := tx.ReplacePersonalToken(owner.ID, 1, "old", "replacement", time.Now())
	if err != nil || !changed {
		t.Fatal("replacement failed")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	check(true)
	if !credentialTx(t, st, func(tx datastore.DataStoreTx) (bool, error) {
		return tx.ReplacePersonalToken(owner.ID, 1, "old", "replacement", time.Now())
	}) {
		t.Fatal("replacement failed")
	}
	check(false)
	if _, err := st.NonTx().FinalizeBootstrapToken(owner.ID); err != nil {
		t.Fatal(err)
	}
	check(false)
	state, err := st.NonTx().BootstrapTokenState()
	if err != nil || state != datastore.BootstrapTokenFinalized {
		t.Fatal("bootstrap lifecycle changed")
	}
}
