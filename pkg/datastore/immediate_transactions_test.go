package datastore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"modernc.org/sqlite"
)

type inviteWriteBarrier struct {
	DB
	reached chan struct{}
	release chan struct{}
}

func (b *inviteWriteBarrier) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "UPDATE tokens SET use_count = use_count + 1") {
		close(b.reached)
		<-b.release
	}
	return b.DB.ExecContext(ctx, query, args...)
}

func TestInviteProvisionAcquiresWriterBeforeRead(t *testing.T) {
	for _, suffix := range []string{"", "?_txlock=deferred", "?_txlock=exclusive&_txlock=deferred"} {
		t.Run(suffix, func(t *testing.T) {
			f, err := NewProviderFactory(filepath.Join(t.TempDir(), "db") + suffix)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := f.NonTx().CreateToken("invite", model.RoleUser, 0, 0, 1, time.Time{}); err != nil {
				t.Fatal(err)
			}
			other, err := f.DB.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			if _, err := other.ExecContext(context.Background(), "PRAGMA busy_timeout=0"); err != nil {
				t.Fatal(err)
			}
			txI, err := f.Tx(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			tx := txI.(*txProvider)
			defer func() { _ = tx.Rollback() }()
			barrier := &inviteWriteBarrier{DB: tx.DB, reached: make(chan struct{}), release: make(chan struct{})}
			tx.DB = barrier
			result := make(chan error, 1)
			go func() {
				_, err := tx.ProvisionUser("invite", "new-user", "personal", time.Now().UTC(), false)
				result <- err
			}()
			select {
			case <-barrier.reached:
			case err := <-result:
				t.Fatalf("provision ended before barrier: %v", err)
			case <-time.After(5 * time.Second):
				close(barrier.release)
				t.Fatal("provision did not reach read/write barrier")
			}
			// No timing assumption: an independent writer either owns the lock or cannot commit.
			_, competingErr := other.ExecContext(context.Background(), "INSERT INTO users (username, role) VALUES ('other', 0)")
			close(barrier.release)
			provisionErr := <-result
			if provisionErr != nil {
				t.Fatalf("ProvisionUser after competing write: %v (competing: %v)", provisionErr, competingErr)
			}
			var busy *sqlite.Error
			if !errors.As(competingErr, &busy) || busy.Code() != 5 {
				t.Fatalf("competing writer = %v, want SQLITE_BUSY", competingErr)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if _, err := other.ExecContext(context.Background(), "INSERT INTO users (username, role) VALUES ('other', 0)"); err != nil {
				t.Fatal(err)
			}
			user, err := f.NonTx().GetUserByPersonalTokenHash("personal")
			if err != nil || user == nil {
				t.Fatalf("committed personal credential: user=%v err=%v", user, err)
			}
		})
	}
}

func TestTransactionCancellationRollsBackProvision(t *testing.T) {
	f, err := NewProviderFactory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.NonTx().CreateToken("invite", model.RoleUser, 0, 0, 1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx, err := f.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ProvisionUser("invite", "cancelled", "personal", time.Now().UTC(), false); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := tx.Commit(); err == nil {
		t.Fatal("cancelled transaction committed")
	}
	retry, err := f.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = retry.Rollback() }()
	if _, err := retry.ProvisionUser("invite", "retried", "personal", time.Now().UTC(), false); err != nil {
		t.Fatal(err)
	}
	if err := retry.Commit(); err != nil {
		t.Fatal(err)
	}
	if user, err := f.NonTx().GetUserByUsername("cancelled"); err != nil || user != nil {
		t.Fatalf("cancelled user persisted: %v %v", user, err)
	}
}
