package datastore_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
)

func TestSQLiteArtifactsUsePrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACLs are not represented by POSIX permission bits")
	}

	root := t.TempDir()
	existingPath := filepath.Join(root, "existing.db")
	missingDir := filepath.Join(root, "private", "sqlite")
	missingPath := filepath.Join(missingDir, "missing.db")
	uriPath := filepath.Join(root, "uri.db")
	plainQueryPath := filepath.Join(root, "plain-query.db")
	tests := map[string]struct {
		path       string
		dsn        string
		precreate  bool
		createdDir string
	}{
		"existing database": {
			path:      existingPath,
			dsn:       existingPath,
			precreate: true,
		},
		"missing parent": {
			path:       missingPath,
			dsn:        missingPath,
			createdDir: missingDir,
		},
		"file URI": {
			path:      uriPath,
			dsn:       "file:" + filepath.ToSlash(uriPath) + "?mode=rwc",
			precreate: true,
		},
		"plain path query": {
			path:      plainQueryPath,
			dsn:       plainQueryPath + "?mode=memory",
			precreate: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if tc.precreate {
				if err := os.WriteFile(tc.path, nil, 0o644); err != nil { //nolint:gosec // Intentionally permissive regression fixture.
					t.Fatalf("precreate database: %v", err)
				}
				if err := os.Chmod(tc.path, 0o644); err != nil { //nolint:gosec // Intentionally permissive regression fixture.
					t.Fatalf("make database permissive: %v", err)
				}
			}

			store, err := datastore.NewProviderFactory(tc.dsn)
			if err != nil {
				t.Fatalf("NewProviderFactory: %v", err)
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Errorf("Close datastore: %v", err)
				}
			}()

			if tc.createdDir != "" {
				assertPrivateMode(t, tc.createdDir, 0o700)
			}
			for _, path := range []string{tc.path, tc.path + "-wal", tc.path + "-shm"} {
				assertPrivateMode(t, path, 0o600)
			}
		})
	}
}

func TestSQLiteExistingSidecarsAreProtected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACLs are not represented by POSIX permission bits")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "existing-sidecars.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw SQLite database: %v", err)
	}
	t.Cleanup(func() {
		if err := raw.Close(); err != nil {
			t.Errorf("close raw SQLite database: %v", err)
		}
	})
	if _, err := raw.ExecContext(ctx, "PRAGMA journal_mode=WAL; CREATE TABLE seed (id INTEGER)"); err != nil {
		t.Fatalf("create WAL database: %v", err)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(candidate, 0o644); err != nil { //nolint:gosec // Intentionally permissive regression fixture.
			t.Fatalf("make %s permissive: %v", filepath.Base(candidate), err)
		}
	}

	store, err := datastore.NewProviderFactory(path)
	if err != nil {
		t.Fatalf("NewProviderFactory: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close datastore: %v", err)
		}
	})
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		assertPrivateMode(t, candidate, 0o600)
	}
}

func TestSQLiteMemoryURIDoesNotCreateFilesystemArtifacts(t *testing.T) {
	namedPath := filepath.Join(t.TempDir(), "memory.db")
	memdbPath := filepath.Join(t.TempDir(), "memdb.db")
	plainMemdbPath := filepath.Join(t.TempDir(), "plain-memdb.db")
	tests := map[string]string{
		"named":           "file:" + filepath.ToSlash(namedPath) + "?mode=memory&cache=shared",
		"special":         "file::memory:?cache=shared",
		"memdb VFS":       "file:" + filepath.ToSlash(memdbPath) + "?vfs=memdb",
		"plain memdb VFS": plainMemdbPath + "?vfs=memdb",
	}
	for name, dsn := range tests {
		t.Run(name, func(t *testing.T) {
			store, err := datastore.NewProviderFactory(dsn)
			if err != nil {
				t.Fatalf("NewProviderFactory: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("Close datastore: %v", err)
			}
		})
	}
	for _, path := range []string{":memory:", namedPath, memdbPath, plainMemdbPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("memory URI created %s (stat error %v)", path, err)
		}
	}
}

func TestSQLiteUnsupportedURIOptionsHaveNoFilesystemSideEffects(t *testing.T) {
	root := t.TempDir()
	tests := map[string]string{
		"disk VFS":             "?vfs=unix-dotfile",
		"invalid mode":         "?mode=invalid",
		"duplicate mode":       "?mode=rw&mode=rwc",
		"memory with disk VFS": "?mode=memory&vfs=unix-dotfile",
		"memdb invalid mode":   "?vfs=memdb&mode=invalid",
	}
	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name+".db")
			dsn := "file:" + filepath.ToSlash(path) + query
			if store, err := datastore.NewProviderFactory(dsn); err == nil {
				_ = store.Close()
				t.Fatal("unsupported URI option was accepted")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("unsupported URI option created filesystem artifact (stat error %v)", err)
			}
		})
	}
	plainDir := filepath.Join(root, "plain-missing")
	plainPath := filepath.Join(plainDir, "unsupported.db")
	if store, err := datastore.NewProviderFactory(plainPath + "?vfs=unknown"); err == nil {
		_ = store.Close()
		t.Fatal("unsupported plain-path VFS was accepted")
	}
	if _, err := os.Stat(plainDir); !os.IsNotExist(err) {
		t.Fatalf("unsupported plain-path VFS created parent directory (stat error %v)", err)
	}
	if store, err := datastore.NewProviderFactory("file:"); err == nil {
		_ = store.Close()
		t.Fatal("private temporary SQLite database was accepted")
	}
	if store, err := datastore.NewProviderFactory("file://server/share/gospeak.db"); err == nil {
		_ = store.Close()
		t.Fatal("non-local SQLite URI authority was accepted")
	}
}

func TestSQLiteReadWriteURIDoesNotCreateMissingDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing-parent")
	path := filepath.Join(dir, "missing.db")
	dsn := "file:" + filepath.ToSlash(path) + "?mode=rw"
	if store, err := datastore.NewProviderFactory(dsn); err == nil {
		_ = store.Close()
		t.Fatal("mode=rw created a missing database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("mode=rw created filesystem artifact (stat error %v)", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("mode=rw created parent directory (stat error %v)", err)
	}
}

func TestSQLiteExistingParentPermissionsRemainUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACLs are not represented by POSIX permission bits")
	}
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o755); err != nil { //nolint:gosec // Intentionally permissive compatibility fixture.
		t.Fatalf("create shared parent: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // Intentionally permissive compatibility fixture.
		t.Fatalf("make parent shared: %v", err)
	}
	store, err := datastore.NewProviderFactory(filepath.Join(dir, "gospeak.db"))
	if err != nil {
		t.Fatalf("NewProviderFactory: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close datastore: %v", err)
	}
	assertPrivateMode(t, dir, 0o755)
}

func assertPrivateMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", filepath.Base(path), err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s permissions = %04o, want %04o", filepath.Base(path), got, want)
	}
}
