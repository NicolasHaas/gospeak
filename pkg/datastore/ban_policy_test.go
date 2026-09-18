package datastore_test

import (
	"context"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestBanPersistenceSeparatesAccountAndExactAddressIdentity(t *testing.T) {
	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatalf("NewTestSqlConn: %v", err)
	}
	admin, err := store.NonTx().CreateUser("admin-ban-policy", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(admin): %v", err)
	}
	target, err := store.NonTx().CreateUser("target-ban-policy", model.RoleUser)
	if err != nil {
		t.Fatalf("CreateUser(target): %v", err)
	}

	if err := store.NonTx().CreateUserBan(target.ID, admin.ID, time.Time{}); err != nil {
		t.Fatalf("CreateUserBan: %v", err)
	}
	if err := store.NonTx().CreateIPBan("::ffff:192.0.2.10", admin.ID, time.Time{}); err != nil {
		t.Fatalf("CreateIPBan: %v", err)
	}

	var accountIP, addressIP string
	var accountUserID, addressUserID int64
	if err := store.DB.QueryRowContext(context.Background(), "SELECT user_id, ip FROM bans WHERE user_id = ?", target.ID).Scan(&accountUserID, &accountIP); err != nil {
		t.Fatalf("read account ban: %v", err)
	}
	if err := store.DB.QueryRowContext(context.Background(), "SELECT user_id, ip FROM bans WHERE ip <> ''").Scan(&addressUserID, &addressIP); err != nil {
		t.Fatalf("read IP ban: %v", err)
	}
	if accountUserID != target.ID || accountIP != "" {
		t.Fatalf("account ban identity = (%d, %q), want (%d, empty)", accountUserID, accountIP, target.ID)
	}
	if addressUserID != 0 || addressIP != "192.0.2.10" {
		t.Fatalf("IP ban identity = (%d, %q), want (0, 192.0.2.10)", addressUserID, addressIP)
	}

	for _, address := range []string{"192.0.2.10", "::ffff:192.0.2.10"} {
		banned, err := store.NonTx().IsIPBanned(address)
		if err != nil {
			t.Fatalf("IsIPBanned(%q): %v", address, err)
		}
		if !banned {
			t.Fatalf("IsIPBanned(%q) = false, want true", address)
		}
	}
	banned, err := store.NonTx().IsIPBanned("192.0.2.11")
	if err != nil {
		t.Fatalf("IsIPBanned(neighbor): %v", err)
	}
	if banned {
		t.Fatal("neighbor address matched exact-address ban")
	}
}

func TestCreateIPBanRejectsNonExactAddresses(t *testing.T) {
	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatalf("NewTestSqlConn: %v", err)
	}
	for _, address := range []string{"192.0.2.0/24", "example.invalid", "192.0.2.10:9600", "fe80::1%eth0", ""} {
		if err := store.NonTx().CreateIPBan(address, 1, time.Time{}); err == nil {
			t.Errorf("CreateIPBan(%q) succeeded, want error", address)
		}
	}
}

func TestListAndDeleteActiveBansByID(t *testing.T) {
	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatalf("NewTestSqlConn: %v", err)
	}
	admin, err := store.NonTx().CreateUser("ban-list-admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(admin): %v", err)
	}
	target, err := store.NonTx().CreateUser("named-banned-user", model.RoleUser)
	if err != nil {
		t.Fatalf("CreateUser(target): %v", err)
	}
	if err := store.NonTx().CreateUserBan(target.ID, admin.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateUserBan(active): %v", err)
	}
	if err := store.NonTx().CreateIPBan("2001:db8::1", admin.ID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("CreateIPBan(expired): %v", err)
	}

	bans, _, err := store.NonTx().ListActiveBans(0, datastore.MaxBanPageSize)
	if err != nil {
		t.Fatalf("ListActiveBans: %v", err)
	}
	if len(bans) != 1 || bans[0].UserID != target.ID || bans[0].Username != target.Username || bans[0].IP != "" {
		t.Fatalf("active bans = %#v, want named account ban", bans)
	}
	deleted, err := store.NonTx().DeleteBan(bans[0].ID)
	if err != nil {
		t.Fatalf("DeleteBan: %v", err)
	}
	if !deleted {
		t.Fatal("DeleteBan reported no deletion")
	}
	bans, _, err = store.NonTx().ListActiveBans(0, datastore.MaxBanPageSize)
	if err != nil {
		t.Fatalf("ListActiveBans after delete: %v", err)
	}
	if len(bans) != 0 {
		t.Fatalf("active bans after delete = %#v, want none", bans)
	}
}

func TestBootstrapUserIdentitySurvivesFinalization(t *testing.T) {
	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatalf("NewTestSqlConn: %v", err)
	}
	if err := store.NonTx().CreateBootstrapToken("bootstrap-policy-hash"); err != nil {
		t.Fatalf("CreateBootstrapToken: %v", err)
	}
	tx, err := store.Tx(context.Background())
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	owner, err := tx.ProvisionBootstrapUser("bootstrap-policy-hash", "bootstrap-policy-owner", "personal-policy-hash", time.Now().UTC())
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("ProvisionBootstrapUser: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if finalized, err := store.NonTx().FinalizeBootstrapToken(owner.ID); err != nil || !finalized {
		t.Fatalf("FinalizeBootstrapToken = (%t, %v), want (true, nil)", finalized, err)
	}

	protected, err := store.NonTx().IsBootstrapUser(owner.ID)
	if err != nil {
		t.Fatalf("IsBootstrapUser(owner): %v", err)
	}
	if !protected {
		t.Fatal("finalized bootstrap owner was not recognized")
	}
	normal, err := store.NonTx().CreateUser("ordinary-admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(normal): %v", err)
	}
	protected, err = store.NonTx().IsBootstrapUser(normal.ID)
	if err != nil {
		t.Fatalf("IsBootstrapUser(normal): %v", err)
	}
	if protected {
		t.Fatal("ordinary admin was classified as bootstrap owner")
	}
}

func TestActiveBanListingIsBoundedAndCursorPaged(t *testing.T) {
	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatalf("NewTestSqlConn: %v", err)
	}
	admin, err := store.NonTx().CreateUser("paged-ban-admin", model.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(admin): %v", err)
	}
	for _, ip := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		if err := store.NonTx().CreateIPBan(ip, admin.ID, time.Time{}); err != nil {
			t.Fatalf("CreateIPBan(%s): %v", ip, err)
		}
	}

	first, more, err := store.NonTx().ListActiveBans(0, 2)
	if err != nil || len(first) != 2 || !more {
		t.Fatalf("first page = %#v, more=%t, err=%v", first, more, err)
	}
	second, more, err := store.NonTx().ListActiveBans(first[len(first)-1].ID, 2)
	if err != nil || len(second) != 1 || more {
		t.Fatalf("second page = %#v, more=%t, err=%v", second, more, err)
	}
	if second[0].ID <= first[len(first)-1].ID {
		t.Fatalf("cursor order did not advance: first=%#v second=%#v", first, second)
	}
	if _, _, err := store.NonTx().ListActiveBans(0, datastore.MaxBanPageSize+1); err == nil {
		t.Fatal("oversized ban page was accepted")
	}
}
