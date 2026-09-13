package datastore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrationFromPopulatedVersionTwoCreatesTokenColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version-two.db")
	raw := openRawSQLite(t, path)
	if _, err := raw.ExecContext(context.Background(), `
		CREATE TABLE schema_migrations (version INTEGER NOT NULL);
		INSERT INTO schema_migrations VALUES (2);
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			role INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		INSERT INTO users (username) VALUES ('legacy-user');
		CREATE TABLE channels (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			parent_id INTEGER NOT NULL DEFAULT 0,
			is_temp INTEGER NOT NULL DEFAULT 0,
			allow_sub_channels INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE tokens (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			hash TEXT NOT NULL UNIQUE,
			role INTEGER NOT NULL DEFAULT 0,
			channel_scope INTEGER NOT NULL DEFAULT 0,
			created_by INTEGER NOT NULL DEFAULT 0,
			max_uses INTEGER NOT NULL DEFAULT 0,
			use_count INTEGER NOT NULL DEFAULT 0,
			expires_at TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
	`); err != nil {
		t.Fatalf("create version-two fixture: %v", err)
	}
	closeRawSQLite(t, raw)

	store, err := NewProviderFactory(path)
	if err != nil {
		t.Fatalf("NewProviderFactory: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close datastore: %v", err)
		}
	}()

	for _, column := range []string{"personal_token_hash", "personal_token_created_at"} {
		if !sqliteColumnExists(t, store.DB, "users", column) {
			t.Errorf("users.%s is absent after migration", column)
		}
	}
	var createdAt string
	if err := store.DB.QueryRowContext(context.Background(), "SELECT personal_token_created_at FROM users WHERE username = 'legacy-user'").Scan(&createdAt); err != nil {
		t.Fatalf("read migrated token timestamp: %v", err)
	}
	if createdAt == "" {
		t.Fatal("migrated personal_token_created_at is empty")
	}
}

func TestMigrationRepairsVersionEightMissingTokenTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "damaged-version-eight.db")
	store, err := NewProviderFactory(path)
	if err != nil {
		t.Fatalf("create current datastore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close current datastore: %v", err)
	}

	raw := openRawSQLite(t, path)
	if _, err := raw.ExecContext(context.Background(), `
		ALTER TABLE users DROP COLUMN personal_token_created_at;
		UPDATE schema_migrations SET version = 8;
	`); err != nil {
		t.Fatalf("damage version-eight fixture: %v", err)
	}
	closeRawSQLite(t, raw)

	store, err = NewProviderFactory(path)
	if err != nil {
		t.Fatalf("repair datastore: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close datastore: %v", err)
		}
	}()
	if !sqliteColumnExists(t, store.DB, "users", "personal_token_created_at") {
		t.Fatal("users.personal_token_created_at was not repaired")
	}
	var version int
	if err := store.DB.QueryRowContext(context.Background(), "SELECT version FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("read repaired schema version: %v", err)
	}
	if version != 9 {
		t.Fatalf("repaired schema version = %d, want 9", version)
	}
}

func TestMigrationUnexpectedErrorRollsBackVersionBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-version-one.db")
	raw := openRawSQLite(t, path)
	if _, err := raw.ExecContext(context.Background(), `
		CREATE TABLE schema_migrations (version INTEGER NOT NULL);
		INSERT INTO schema_migrations VALUES (1);
		CREATE VIEW channels AS SELECT 1 AS id;
	`); err != nil {
		t.Fatalf("create invalid version-one fixture: %v", err)
	}
	closeRawSQLite(t, raw)

	if store, err := NewProviderFactory(path); err == nil {
		_ = store.Close()
		t.Fatal("NewProviderFactory accepted invalid migration schema")
	}
	raw = openRawSQLite(t, path)
	defer closeRawSQLite(t, raw)
	var version int
	if err := raw.QueryRowContext(context.Background(), "SELECT version FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema version after failed migration = %d, want 1", version)
	}
}

func TestMigrationRollsBackDDLWhenVersionUpdateFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version-update-failure.db")
	store, err := NewProviderFactory(path)
	if err != nil {
		t.Fatalf("create current datastore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close current datastore: %v", err)
	}

	raw := openRawSQLite(t, path)
	if _, err := raw.ExecContext(context.Background(), `
		ALTER TABLE channels DROP COLUMN created_by;
		UPDATE schema_migrations SET version = 7;
		CREATE TRIGGER reject_schema_version
		BEFORE UPDATE ON schema_migrations
		BEGIN
			SELECT RAISE(ABORT, 'reject schema version');
		END;
	`); err != nil {
		t.Fatalf("create version-update failure fixture: %v", err)
	}
	closeRawSQLite(t, raw)

	if store, err := NewProviderFactory(path); err == nil {
		_ = store.Close()
		t.Fatal("NewProviderFactory succeeded despite rejected version update")
	}
	raw = openRawSQLite(t, path)
	defer closeRawSQLite(t, raw)
	if sqliteColumnExists(t, raw, "channels", "created_by") {
		t.Fatal("channels.created_by survived failed schema-version update")
	}
	var version int
	if err := raw.QueryRowContext(context.Background(), "SELECT version FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 7 {
		t.Fatalf("schema version after rejected update = %d, want 7", version)
	}
}

func openRawSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw SQLite database: %v", err)
	}
	return db
}

func closeRawSQLite(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("close raw SQLite database: %v", err)
	}
}

func sqliteColumnExists(t *testing.T, db DB, table, column string) bool {
	t.Helper()
	var exists int
	if err := db.QueryRowContext(
		context.Background(),
		"SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE name = ?)",
		table,
		column,
	).Scan(&exists); err != nil {
		t.Fatalf("inspect %s.%s: %v", table, column, err)
	}
	return exists != 0
}
