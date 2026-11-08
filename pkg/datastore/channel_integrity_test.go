package datastore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestChannelReferenceAdmission(t *testing.T) {
	for _, transactional := range []bool{false, true} {
		t.Run(fmt.Sprint(transactional), func(t *testing.T) {
			f := newChannelUniquenessStore(t)
			st := f.NonTx()
			if transactional {
				tx, err := f.Tx(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				st = tx
			}
			root := &model.Channel{Name: "root"}
			if err := st.CreateChannel(root); err != nil {
				t.Fatal(err)
			}
			for _, id := range []int64{-1, root.ID + 999, 0, root.ID} {
				valid := id == 0 || id == root.ID
				t.Run(fmt.Sprint(id), func(t *testing.T) {
					ch := &model.Channel{Name: fmt.Sprintf("child-%d", id), ParentID: id}
					if err := st.CreateChannel(ch); (err == nil) != valid {
						t.Errorf("parent %d: %v", id, err)
					}
					if _, err := st.CreateUserWithChannelScope(fmt.Sprintf("user-%d", id), model.RoleUser, id); (err == nil) != valid {
						t.Errorf("user scope %d: %v", id, err)
					}
					if err := st.CreateToken(fmt.Sprintf("token-%d", id), model.RoleUser, id, 0, 0, time.Time{}); (err == nil) != valid {
						t.Errorf("token scope %d: %v", id, err)
					}
				})
			}
			temporary := &model.Channel{Name: "temporary", IsTemp: true}
			if err := st.CreateChannel(temporary); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateToken("temporary-invite", model.RoleUser, temporary.ID, 0, 0, time.Time{}); !errors.Is(err, ErrChannelReference) {
				t.Fatalf("temporary invite accepted: %v", err)
			}
			if transactional {
				if err := st.(DataStoreTx).Commit(); err != nil {
					t.Fatal(err)
				}
			}
			var channels, users, tokens int
			for query, dest := range map[string]*int{"SELECT COUNT(*) FROM channels": &channels, "SELECT COUNT(*) FROM users": &users, "SELECT COUNT(*) FROM tokens": &tokens} {
				if err := f.DB.QueryRowContext(context.Background(), query).Scan(dest); err != nil {
					t.Fatal(err)
				}
			}
			if channels != 4 || users != 2 || tokens != 2 {
				t.Fatalf("persisted channels/users/tokens = %d/%d/%d", channels, users, tokens)
			}
		})
	}
}

func TestChannelDeletionRefusesDurableReferences(t *testing.T) {
	for _, reference := range []string{"child", "temporary child", "token", "user", "missing", "last"} {
		t.Run(reference, func(t *testing.T) {
			f := newChannelUniquenessStore(t)
			st := f.NonTx()
			ch := &model.Channel{Name: "target"}
			if err := st.CreateChannel(ch); err != nil {
				t.Fatal(err)
			}
			if reference != "last" {
				if err := st.CreateChannel(&model.Channel{Name: "other"}); err != nil {
					t.Fatal(err)
				}
			}
			switch reference {
			case "child", "temporary child":
				if err := st.CreateChannel(&model.Channel{Name: "child", ParentID: ch.ID, IsTemp: reference == "temporary child"}); err != nil {
					t.Fatal(err)
				}
			case "token":
				if err := st.CreateToken("invite", model.RoleUser, ch.ID, 0, 1, time.Time{}); err != nil {
					t.Fatal(err)
				}
			case "user":
				if _, err := st.CreateUserWithChannelScope("scoped", model.RoleUser, ch.ID); err != nil {
					t.Fatal(err)
				}
			}
			id := ch.ID
			if reference == "missing" {
				id += 999
			}
			if err := st.DeleteChannel(id); !errors.Is(err, ErrChannelDeleteRefused) {
				t.Fatalf("unsafe/nonexistent deletion: %v", err)
			}
			if got, err := st.GetChannel(ch.ID); err != nil || got == nil {
				t.Fatalf("target not preserved: %v %v", got, err)
			}
		})
	}
}

func TestInviteChannelReferenceRetirement(t *testing.T) {
	for _, tc := range []struct {
		name            string
		expired         bool
		maxUses, uses   int
		account, retire bool
		malformedExpiry bool
	}{
		{name: "expired", expired: true, maxUses: 1, retire: true},
		{name: "expired unlimited", expired: true, retire: true},
		{name: "exhausted", maxUses: 1, uses: 1, retire: true},
		{name: "remaining use", maxUses: 2, uses: 1},
		{name: "unlimited", maxUses: 0, uses: 2},
		{name: "account", maxUses: 1, uses: 1, account: true},
		{name: "malformed expiry", maxUses: 1, malformedExpiry: true},
	} {
		for _, transactional := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tx=%t", tc.name, transactional), func(t *testing.T) {
				f := newChannelUniquenessStore(t)
				ch := &model.Channel{Name: "target"}
				for _, c := range []*model.Channel{ch, {Name: "other"}} {
					if err := f.NonTx().CreateChannel(c); err != nil {
						t.Fatal(err)
					}
				}
				expiry := time.Now().Add(time.Hour)
				if tc.expired {
					expiry = time.Now().Add(-time.Hour)
				}
				if err := f.NonTx().CreateToken("invite", model.RoleUser, ch.ID, 0, tc.maxUses, expiry); err != nil {
					t.Fatal(err)
				}
				if err := f.NonTx().CreateBootstrapToken("bootstrap"); err != nil {
					t.Fatal(err)
				}
				if tc.malformedExpiry {
					if _, err := f.DB.ExecContext(context.Background(), "UPDATE tokens SET expires_at = ? WHERE hash = ?", "0000-invalid", "invite"); err != nil {
						t.Fatal(err)
					}
				}
				for range tc.uses {
					tx, err := f.Tx(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					if tc.account {
						if _, err := tx.ProvisionUser("invite", "scoped", "personal", time.Now(), false); err != nil {
							t.Fatal(err)
						}
					} else if _, err := tx.ValidateToken("invite"); err != nil {
						t.Fatal(err)
					}
					if err := tx.Commit(); err != nil {
						t.Fatal(err)
					}
				}
				st := f.NonTx()
				if transactional {
					tx, err := f.Tx(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					st = tx
				}
				err := st.DeleteChannel(ch.ID)
				if tc.retire && err != nil {
					t.Fatalf("inactive invite pinned channel: %v", err)
				}
				if !tc.retire && !errors.Is(err, ErrChannelDeleteRefused) {
					t.Fatalf("live reference not protected: %v", err)
				}
				if transactional {
					if err := st.(DataStoreTx).Commit(); err != nil {
						t.Fatal(err)
					}
				}
				got, err := f.NonTx().GetChannel(ch.ID)
				if err != nil || (got == nil) != tc.retire {
					t.Fatalf("channel after retirement: %v %v", got, err)
				}
				var count int
				if err := f.DB.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM tokens WHERE hash IN (?, ?)", "invite", "bootstrap").Scan(&count); err != nil || count != 2 {
					t.Fatalf("token history changed: count=%d err=%v", count, err)
				}
				if state, err := f.NonTx().BootstrapTokenState(); err != nil || state != BootstrapTokenPending {
					t.Fatalf("bootstrap marker changed: %v %v", state, err)
				}
				if tc.account {
					user, err := f.NonTx().GetUserByPersonalTokenHash("personal")
					if err != nil || user == nil || user.ChannelScope != ch.ID {
						t.Fatalf("scoped account changed: %v %v", user, err)
					}
				}
			})
		}
	}
}

func TestInviteReferenceExpiryMatchesRedemptionBoundary(t *testing.T) {
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(offset.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newChannelUniquenessStore(t)
				ch := &model.Channel{Name: "target"}
				for _, c := range []*model.Channel{ch, {Name: "other"}} {
					if err := f.NonTx().CreateChannel(c); err != nil {
						t.Fatal(err)
					}
				}
				if err := f.NonTx().CreateToken("invite", model.RoleUser, ch.ID, 0, 0, time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Second + offset)
				tx, err := f.Tx(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				_, validationErr := tx.ValidateToken("invite")
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				if (validationErr != nil) != (offset > 0) {
					t.Fatalf("redemption at boundary: %v", validationErr)
				}
				err = f.NonTx().DeleteChannel(ch.ID)
				if offset > 0 && err != nil {
					t.Fatalf("expired invite pinned channel: %v", err)
				}
				if offset <= 0 && !errors.Is(err, ErrChannelDeleteRefused) {
					t.Fatalf("unexpired invite not protected: %v", err)
				}
				if offset > 0 {
					tx, err := f.Tx(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					if _, err := tx.ProvisionUser("invite", "late", "personal", time.Now(), false); err == nil {
						t.Fatal("retired invite created an account")
					}
				}
			})
		})
	}
}

func TestInviteRedemptionPinsChannelDuringConcurrentDeletion(t *testing.T) {
	f := newChannelUniquenessStore(t)
	ch := &model.Channel{Name: "target"}
	for _, c := range []*model.Channel{ch, {Name: "other"}} {
		if err := f.NonTx().CreateChannel(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.NonTx().CreateToken("invite", model.RoleUser, ch.ID, 0, 1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	tx, err := f.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ProvisionUser("invite", "scoped", "personal", time.Now(), false); err != nil {
		t.Fatal(err)
	}
	started, deleted := make(chan struct{}), make(chan error, 1)
	go func() { close(started); deleted <- f.NonTx().DeleteChannel(ch.ID) }()
	<-started
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-deleted:
		if !errors.Is(err, ErrChannelDeleteRefused) {
			t.Fatalf("final invite use lost its account scope: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("competing deletion did not exit")
	}
	user, err := f.NonTx().GetUserByPersonalTokenHash("personal")
	if err != nil || user == nil || user.ChannelScope != ch.ID {
		t.Fatalf("provisioned scope changed: %v %v", user, err)
	}
}

func TestChannelReferenceWriterOrdering(t *testing.T) {
	for _, ref := range []string{"child", "token", "user", "last"} {
		for _, deletionFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/delete-first=%t", ref, deletionFirst), func(t *testing.T) {
				f := newChannelUniquenessStore(t)
				ch, other := &model.Channel{Name: "target"}, &model.Channel{Name: "other"}
				for _, c := range []*model.Channel{ch, other} {
					if err := f.NonTx().CreateChannel(c); err != nil {
						t.Fatal(err)
					}
				}
				if err := f.NonTx().CreateToken("inactive", model.RoleUser, ch.ID, 0, 1, time.Now().Add(-time.Hour)); err != nil {
					t.Fatal(err)
				}
				write := func(st DataStore) error {
					switch ref {
					case "child":
						return st.CreateChannel(&model.Channel{Name: "child", ParentID: ch.ID})
					case "token":
						return st.CreateToken("invite", model.RoleUser, ch.ID, 0, 0, time.Time{})
					case "user":
						_, err := st.CreateUserWithChannelScope("scoped", model.RoleUser, ch.ID)
						return err
					default:
						return st.DeleteChannel(other.ID)
					}
				}
				tx, err := f.Tx(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				if deletionFirst {
					err = tx.DeleteChannel(ch.ID)
				} else {
					err = write(tx)
				}
				if err != nil {
					t.Fatal(err)
				}
				// Force both commit orders; the first transaction owns SQLite's writer lock.
				started, result := make(chan struct{}), make(chan error, 1)
				go func() {
					close(started)
					if deletionFirst {
						result <- write(f.NonTx())
					} else {
						result <- f.NonTx().DeleteChannel(ch.ID)
					}
				}()
				<-started
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-result:
					want := ErrChannelDeleteRefused
					if deletionFirst && ref != "last" {
						want = ErrChannelReference
					}
					if !errors.Is(err, want) {
						t.Fatalf("competing writer: %v, want %v", err, want)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("competing writer did not finish")
				}
			})
		}
	}
}
