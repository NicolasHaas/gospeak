package datastore_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
)

func TestSQLiteConnectionPragmasApplyToEveryPoolConnection(t *testing.T) {
	tests := map[string]string{
		"plain path": filepath.Join(t.TempDir(), "plain.db"),
		"URI pragma overrides": "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "uri.db")) +
			"?mode=rwc&_pragma=foreign_keys(OFF)&_pragma=busy_timeout(1)" +
			"&_pragma=foreign_keys/**/(OFF)&_pragma=main.foreign_keys(OFF)" +
			"&_pragma=busy_timeout/**/(1)&_pragma=main.busy_timeout(1)",
	}
	for name, dbPath := range tests {
		t.Run(name, func(t *testing.T) {
			assertSQLiteConnectionPragmas(t, dbPath)
		})
	}
}

func TestSQLiteConnectionPragmasRejectEmptyPath(t *testing.T) {
	if _, err := datastore.NewProviderFactory(""); err == nil {
		t.Fatal("NewProviderFactory accepted an empty database path")
	}
}

func assertSQLiteConnectionPragmas(t *testing.T, dbPath string) {
	t.Helper()
	store, err := datastore.NewProviderFactory(dbPath)
	if err != nil {
		t.Fatalf("NewProviderFactory: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close datastore: %v", err)
		}
	})

	const connectionCount = 4
	store.DB.SetMaxOpenConns(connectionCount)
	ctx := context.Background()
	for i := 0; i < connectionCount; i++ {
		conn, err := store.DB.Conn(ctx)
		if err != nil {
			t.Fatalf("acquire connection %d: %v", i, err)
		}
		t.Cleanup(func() {
			if err := conn.Close(); err != nil {
				t.Errorf("release connection %d: %v", i, err)
			}
		})

		var foreignKeys, busyTimeout int
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", i, err)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", i, err)
		}
		if foreignKeys != 1 || busyTimeout != 5000 {
			t.Fatalf("connection %d pragmas = foreign_keys %d, busy_timeout %d; want 1, 5000", i, foreignKeys, busyTimeout)
		}
	}
}
