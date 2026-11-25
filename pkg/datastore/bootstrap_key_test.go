package datastore_test

import (
	"context"
	"testing"
	"time"
)

func TestBootstrapKeyTransactionAndFinalization(t *testing.T) {
	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.NonTx().CreateBootstrapToken("setup-hash"); err != nil {
		t.Fatal(err)
	}
	tx, err := store.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	key, err := tx.BootstrapPersonalTokenKey("setup-hash")
	if err != nil || key == "" {
		_ = tx.Rollback()
		t.Fatalf("prepare key: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	tx, err = store.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	fresh, err := tx.BootstrapPersonalTokenKey("setup-hash")
	if err != nil || fresh == "" || fresh == key {
		t.Fatalf("rolled-back key was reused: %v", err)
	}
	owner, err := tx.ProvisionBootstrapUser("setup-hash", "owner", "personal-hash", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = store.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	retry, err := tx.BootstrapPersonalTokenKey("setup-hash")
	if err != nil || retry != fresh {
		_ = tx.Rollback()
		t.Fatalf("committed key changed: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if finalized, err := store.NonTx().FinalizeBootstrapToken(owner.ID); err != nil || !finalized {
		t.Fatalf("finalize: %t %v", finalized, err)
	}
	var cleared bool
	if err := store.DB.QueryRowContext(context.Background(), "SELECT bootstrap_key = '' FROM tokens WHERE hash = ?", "setup-hash").Scan(&cleared); err != nil || !cleared {
		t.Fatalf("retry key retained after finalization: %t %v", cleared, err)
	}
	if legacy, err := store.NonTx().HasLegacyBootstrapOwner(); err != nil || legacy {
		t.Fatalf("new owner misclassified as legacy: %t %v", legacy, err)
	}
}
