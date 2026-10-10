package datastore

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

type loginQueryRecorder struct {
	DB
	query string
	args  []any
}

func (r *loginQueryRecorder) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	r.query, r.args = query, args
	return r.DB.QueryRowContext(ctx, query, args...)
}

func TestLoginQueriesUseIndexesAfterMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "login.db")
	store, err := NewProviderFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.ExecContext(context.Background(), `
		DROP INDEX IF EXISTS idx_tokens_created_by_kind;
		UPDATE schema_migrations SET version = 11;
		INSERT INTO tokens(hash, kind, created_by, use_count) VALUES ('synthetic-marker', 1, 1, 1);
		INSERT INTO tokens(hash, kind, created_by, use_count, max_uses) VALUES ('synthetic-invite', 0, 1, 1, 1);
		INSERT INTO bans(user_id) VALUES (1);
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewProviderFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recorder := &loginQueryRecorder{DB: store.DB}
	provider := &baseProvider{DB: recorder}
	for _, tc := range []struct {
		name, index string
		check       func(int64) (bool, error)
	}{
		{"ban", "idx_bans_user_id", provider.IsUserBanned},
		{"bootstrap", "idx_tokens_created_by_kind", provider.IsBootstrapUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if yes, err := tc.check(1); err != nil || !yes {
				t.Fatalf("lookup=%v err=%v", yes, err)
			}
			rows, err := store.DB.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+recorder.query, recorder.args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			indexed := false
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				t.Log(detail)
				indexed = indexed || (strings.Contains(detail, "SEARCH") && strings.Contains(detail, tc.index))
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !indexed {
				t.Errorf("actual lookup did not search %s", tc.index)
			}
		})
	}
	var tokens int
	if err := store.DB.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM tokens").Scan(&tokens); err != nil || tokens != 2 {
		t.Fatalf("migration lost marker/invite: tokens=%d err=%v", tokens, err)
	}
}

func TestLoginIndexMigrationRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollback.db")
	store, err := NewProviderFactory(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.ExecContext(context.Background(), `
		DROP INDEX IF EXISTS idx_tokens_created_by_kind;
		UPDATE schema_migrations SET version = 11;
		CREATE TRIGGER reject_login_migration BEFORE UPDATE ON schema_migrations
		BEGIN SELECT RAISE(ABORT, 'reject migration'); END;
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := NewProviderFactory(path); err == nil {
		_ = reopened.Close()
		t.Fatal("migration ignored version update failure")
	}
	raw := openRawSQLite(t, path)
	defer closeRawSQLite(t, raw)
	var version, indexes int
	if err := raw.QueryRowContext(context.Background(), "SELECT version FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM sqlite_master WHERE name='idx_tokens_created_by_kind'").Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if version != 11 || indexes != 0 {
		t.Fatalf("failed migration version=%d indexes=%d", version, indexes)
	}
}
