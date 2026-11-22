package datastore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestInvitePagesAndRevocation(t *testing.T) {
	f, err := NewProviderFactory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st := f.NonTx()
	if err := st.CreateBootstrapToken("bootstrap"); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{"first", "second"} {
		if err := st.CreateToken(hash, model.RoleAdmin, 0, 0, 0, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, page := range []struct {
		after int64
		limit int
	}{{-1, 1}, {0, 0}, {0, 101}} {
		if _, _, err := st.ListInvites(page.after, page.limit); err == nil {
			t.Fatal("invalid page accepted")
		}
	}
	first, more, err := st.ListInvites(0, 1)
	if err != nil || len(first) != 1 || !more || first[0].Value != "" || first[0].Hash != "" || first[0].MaxUses != 0 {
		t.Fatalf("first page: count=%d more=%v err=%v", len(first), more, err)
	}
	second, more, err := st.ListInvites(first[0].ID, 1)
	if err != nil || len(second) != 1 || more {
		t.Fatalf("second page: count=%d more=%v err=%v", len(second), more, err)
	}
	var bootstrapID int64
	if err := f.DB.QueryRowContext(t.Context(), "SELECT id FROM tokens WHERE kind = ?", tokenKindBootstrap).Scan(&bootstrapID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{bootstrapID, second[0].ID + 100} {
		if deleted, err := st.RevokeInvite(id); err != nil || deleted {
			t.Fatalf("protected/missing revoke: %v %v", deleted, err)
		}
	}
	tx, err := f.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	user, err := tx.ProvisionUser("first", "existing", "personal", time.Now(), false)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		deleted, err := st.RevokeInvite(first[0].ID)
		if err != nil || deleted != (i == 0) {
			t.Fatalf("revoke iteration %d: %v %v", i, deleted, err)
		}
	}
	tx, err = f.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ProvisionUser("first", "later", "later-personal", time.Now(), false); err == nil {
		_ = tx.Rollback()
		t.Fatal("revoked invite redeemed")
	}
	_ = tx.Rollback()
	existing, err := st.GetUserByPersonalTokenHash("personal")
	if err != nil || existing == nil || existing.ID != user.ID {
		t.Fatal("personal credential changed")
	}
	state, err := st.BootstrapTokenState()
	if err != nil || state != BootstrapTokenPending {
		t.Fatal("bootstrap changed")
	}
	for i := range MaxInvitePageSize {
		if err := st.CreateToken(fmt.Sprintf("page-%d", i), model.RoleUser, 0, 0, 1, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	full, more, err := st.ListInvites(0, MaxInvitePageSize)
	if err != nil || len(full) != MaxInvitePageSize || !more {
		t.Fatal("maximum page is not bounded with continuation")
	}
	tail, more, err := st.ListInvites(full[len(full)-1].ID, MaxInvitePageSize)
	if err != nil || len(tail) != 2 || more {
		t.Fatal("page continuation lost rows")
	}
}

func TestInviteRevokeSerializesWithRedemption(t *testing.T) {
	f, err := NewProviderFactory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.NonTx().CreateToken("invite", model.RoleUser, 0, 0, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	page, _, err := f.NonTx().ListInvites(0, 1)
	if err != nil || len(page) != 1 {
		t.Fatal("missing invite")
	}
	txI, err := f.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tx := txI.(*txProvider)
	defer func() { _ = tx.Rollback() }()
	barrier := &inviteWriteBarrier{DB: tx.DB, reached: make(chan struct{}), release: make(chan struct{})}
	tx.DB = barrier
	done := make(chan error, 1)
	go func() { _, err := tx.ProvisionUser("invite", "committed", "personal", time.Now(), false); done <- err }()
	select {
	case <-barrier.reached:
	case err := <-done:
		t.Fatalf("provision stopped: %v", err)
	}
	other, err := f.DB.Conn(context.Background())
	if err != nil {
		close(barrier.release)
		provisionErr := <-done
		t.Fatalf("setup failed: %v; joined provision: %v", err, provisionErr)
	}
	defer other.Close()
	_, err = other.ExecContext(context.Background(), "PRAGMA busy_timeout=0")
	if err != nil {
		close(barrier.release)
		provisionErr := <-done
		t.Fatalf("setup failed: %v; joined provision: %v", err, provisionErr)
	}
	competing := &baseProvider{DB: other}
	deleted, revokeErr := competing.RevokeInvite(page[0].ID)
	close(barrier.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if revokeErr == nil || deleted {
		t.Fatal("revocation bypassed redemption writer lock")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if deleted, err := f.NonTx().RevokeInvite(page[0].ID); err != nil || !deleted {
		t.Fatalf("revoke after commit: %v %v", deleted, err)
	}
	existing, err := f.NonTx().GetUserByPersonalTokenHash("personal")
	if err != nil || existing == nil {
		t.Fatal("committed credential lost")
	}
}
