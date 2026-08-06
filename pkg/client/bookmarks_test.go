package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBookmarkStorePersistsTrustedServerPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.yaml")
	store := &BookmarkStore{path: path}
	store.TrustServer("example.test:9600", "SHA256:abc")
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	loaded := &BookmarkStore{path: path}
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	if got := loaded.PinForAddr("example.test:9600"); got != "SHA256:abc" {
		t.Fatalf("PinForAddr() = %q, want %q", got, "SHA256:abc")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("bookmark mode = %o, want 600", info.Mode().Perm())
	}
}

func TestBookmarkStoreLoadsFileWithoutTrustPins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.yaml")
	if err := os.WriteFile(path, []byte("bookmarks:\n  - name: old\n    control_addr: old.test:9600\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store := &BookmarkStore{path: path}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if got := store.PinForAddr("old.test:9600"); got != "" {
		t.Fatalf("PinForAddr() = %q, want empty", got)
	}
}

func TestBookmarkStoreMigratesLegacyFileToUserConfig(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "servers.yaml")
	configPath := filepath.Join(dir, "config", "servers.yaml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("bookmarks:\n  - name: old\n    control_addr: old.test:9600\n    token: secret\n"), 0600); err != nil {
		t.Fatal(err)
	}

	store := &BookmarkStore{path: configPath, legacyPath: legacyPath}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if len(store.Bookmarks) != 1 || store.Bookmarks[0].Token != "secret" {
		t.Fatalf("migrated bookmarks = %#v", store.Bookmarks)
	}
	assertPrivateFile(t, configPath)
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy bookmarks still exist: %v", err)
	}
}
