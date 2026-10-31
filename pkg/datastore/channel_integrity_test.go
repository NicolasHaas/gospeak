package datastore

import (
	"context"
	"errors"
	"fmt"
	"testing"
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
			if channels != 3 || users != 2 || tokens != 2 {
				t.Fatalf("persisted channels/users/tokens = %d/%d/%d", channels, users, tokens)
			}
		})
	}
}

func TestChannelDeletionRefusesDurableReferences(t *testing.T) {
	for _, reference := range []string{"child", "temporary child", "token", "expired token", "exhausted token", "user", "missing", "last"} {
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
			case "token", "expired token", "exhausted token":
				var expiry time.Time
				if reference == "expired token" {
					expiry = time.Now().Add(-time.Hour)
				}
				if err := st.CreateToken("invite", model.RoleUser, ch.ID, 0, 1, expiry); err != nil {
					t.Fatal(err)
				}
				if reference == "exhausted token" {
					tx, err := f.Tx(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					if _, err := tx.ValidateToken("invite"); err != nil {
						t.Fatal(err)
					}
					if err := tx.Commit(); err != nil {
						t.Fatal(err)
					}
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
