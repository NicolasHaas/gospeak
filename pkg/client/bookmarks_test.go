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
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bookmark mode = %o, want 600", info.Mode().Perm())
	}
}

func TestBookmarkStoreLoadsFileWithoutTrustPins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.yaml")
	if err := os.WriteFile(path, []byte("bookmarks:\n  - name: old\n    control_addr: old.test:9600\n"), 0o600); err != nil {
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

func TestBookmarkStoreRejectsSymlinkAndDoesNotLoadToken(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	path := filepath.Join(dir, "servers.yaml")
	data := []byte("bookmarks:\n  - name: unsafe\n    control_addr: unsafe.test:9600\n    token: secret\n")
	if err := os.WriteFile(target, data, 0o644); err != nil { //nolint:gosec // permissive symlink-target fixture
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	store := &BookmarkStore{
		path:              path,
		Bookmarks:         []Bookmark{{Name: "stale", Token: "stale-secret"}},
		TrustedServerPins: map[string]string{"stale": "stale-pin"},
	}
	if err := store.Load(); err == nil {
		t.Fatal("Load accepted symlinked token file")
	}
	if len(store.Bookmarks) != 0 || len(store.TrustedServerPins) != 0 {
		t.Fatalf("unsafe load retained credentials: bookmarks=%#v pins=%#v", store.Bookmarks, store.TrustedServerPins)
	}
	assertContentsAndMode(t, target, data, 0o644)
}

func TestBookmarkStoreRejectsSymlinkedConfigDirectory(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "target")
	configDir := filepath.Join(root, "gospeak")
	data := []byte("bookmarks:\n  - name: unsafe\n    token: secret\n")
	if err := os.Mkdir(targetDir, 0o755); err != nil { //nolint:gosec // permissive symlink-target fixture
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "servers.yaml"), data, 0o644); err != nil { //nolint:gosec // permissive symlink-target fixture
		t.Fatal(err)
	}
	if err := os.Symlink(targetDir, configDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	store := &BookmarkStore{path: filepath.Join(configDir, "servers.yaml")}
	if err := store.Load(); err == nil {
		t.Fatal("Load accepted symlinked config directory")
	}
	if len(store.Bookmarks) != 0 {
		t.Fatalf("bookmarks loaded through symlinked directory: %#v", store.Bookmarks)
	}
	assertContentsAndMode(t, filepath.Join(targetDir, "servers.yaml"), data, 0o644)
	info, err := os.Stat(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("symlink target mode = %o, want 755", info.Mode().Perm())
	}
}

func TestBookmarkStoreSecuresOwnedTokenFileBeforeLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.yaml")
	data := []byte("bookmarks:\n  - name: private\n    control_addr: private.test:9600\n    token: secret\n")
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // verifies secure-on-load compatibility
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // verifies secure-on-load compatibility
		t.Fatal(err)
	}

	store := &BookmarkStore{path: path}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if len(store.Bookmarks) != 1 || store.Bookmarks[0].Token != "secret" {
		t.Fatalf("loaded bookmarks = %#v", store.Bookmarks)
	}
	assertPrivateFile(t, path)
}

func TestBookmarkStoreRejectsSymlinkedLegacyTokenFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	legacyPath := filepath.Join(dir, "legacy", "servers.yaml")
	configPath := filepath.Join(dir, "config", "servers.yaml")
	data := []byte("bookmarks:\n  - name: unsafe\n    control_addr: unsafe.test:9600\n    token: secret\n")
	if err := os.WriteFile(target, data, 0o644); err != nil { //nolint:gosec // permissive symlink-target fixture
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, legacyPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	store := &BookmarkStore{path: configPath, legacyPath: legacyPath}
	if err := store.Load(); err == nil {
		t.Fatal("Load accepted symlinked legacy token file")
	}
	if len(store.Bookmarks) != 0 {
		t.Fatalf("bookmarks loaded from unsafe legacy path: %#v", store.Bookmarks)
	}
	if _, err := os.Lstat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config file created from unsafe legacy path: %v", err)
	}
	assertContentsAndMode(t, target, data, 0o644)
}

func TestBookmarkStoreDoesNotAutomaticallyMigrateLegacyFile(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "servers.yaml")
	configPath := filepath.Join(dir, "config", "servers.yaml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("bookmarks:\n  - name: old\n    control_addr: old.test:9600\n    token: secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := &BookmarkStore{path: configPath, legacyPath: legacyPath}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if len(store.Bookmarks) != 1 || store.Bookmarks[0].Token != "secret" {
		t.Fatalf("migrated bookmarks = %#v", store.Bookmarks)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("automatic migration created current bookmarks: %v", err)
	}
	assertContentsAndMode(t, legacyPath, []byte("bookmarks:\n  - name: old\n    control_addr: old.test:9600\n    token: secret\n"), 0o600)
	store.Bookmarks[0].Name = "changed legacy"
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("saving declined migration created current bookmarks: %v", err)
	}
	reloaded := &BookmarkStore{path: configPath, legacyPath: legacyPath}
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Bookmarks) != 1 || reloaded.Bookmarks[0].Name != "changed legacy" {
		t.Fatalf("legacy save was not retained: %#v", reloaded.Bookmarks)
	}
}

func TestBookmarkStoreRejectsHardLinkedTokenFileWithoutChangingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	path := filepath.Join(dir, "servers.yaml")
	data := []byte("bookmarks:\n  - name: unsafe\n    token: secret\n")
	if err := os.WriteFile(target, data, 0o644); err != nil { //nolint:gosec // permissive hard-link target fixture
		t.Fatal(err)
	}
	if err := os.Link(target, path); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	store := &BookmarkStore{path: path}
	if err := store.Load(); err == nil {
		t.Fatal("Load accepted hard-linked token file")
	}
	if len(store.Bookmarks) != 0 {
		t.Fatalf("bookmarks loaded through hard link: %#v", store.Bookmarks)
	}
	assertContentsAndMode(t, target, data, 0o644)
}

func TestBookmarkStoreLeavesLegacyFileWhenCurrentExists(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "servers.yaml")
	configPath := filepath.Join(dir, "config", "servers.yaml")
	if err := os.Mkdir(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("bookmarks:\n  - name: old\n    token: old-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(configPath, []byte("bookmarks:\n  - name: current\n    token: current-secret\n")); err != nil {
		t.Fatal(err)
	}

	store := &BookmarkStore{path: configPath, legacyPath: legacyPath}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if len(store.Bookmarks) != 1 || store.Bookmarks[0].Token != "current-secret" {
		t.Fatalf("loaded bookmarks = %#v", store.Bookmarks)
	}
	assertContentsAndMode(t, legacyPath, []byte("bookmarks:\n  - name: old\n    token: old-secret\n"), 0o600)
}

func TestDeclinedBookmarkMigrationUsesLegacyWhenCurrentAlsoExists(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "servers.yaml")
	currentPath := filepath.Join(dir, "config", "servers.yaml")
	if err := os.Mkdir(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("bookmarks:\n  - name: legacy\n    token: legacy-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	currentData := []byte("bookmarks:\n  - name: current\n    token: current-secret\n")
	if err := writePrivateFile(currentPath, currentData); err != nil {
		t.Fatal(err)
	}
	store := &BookmarkStore{path: legacyPath, legacyPath: legacyPath}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	store.Bookmarks[0].Name = "updated legacy"
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	assertContentsAndMode(t, currentPath, currentData, 0o600)
	reloaded := &BookmarkStore{path: legacyPath, legacyPath: legacyPath}
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Bookmarks) != 1 || reloaded.Bookmarks[0].Name != "updated legacy" {
		t.Fatalf("declined migration did not retain legacy destination: %#v", reloaded.Bookmarks)
	}
}

func TestDeclinedBookmarkMigrationKeepsLegacyDestinationAfterParseError(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "servers.yaml")
	currentPath := filepath.Join(dir, "config", "servers.yaml")
	if err := os.Mkdir(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("bookmarks: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	currentData := []byte("bookmarks:\n  - name: current\n    token: current-secret\n")
	if err := writePrivateFile(currentPath, currentData); err != nil {
		t.Fatal(err)
	}

	declined := &BookmarkStore{path: legacyPath, legacyPath: legacyPath}
	if err := declined.Load(); err == nil {
		t.Fatal("invalid legacy bookmarks unexpectedly parsed")
	}
	declined.Bookmarks = []Bookmark{{Name: "recovered legacy"}}
	if err := declined.Save(); err != nil {
		t.Fatal(err)
	}
	assertContentsAndMode(t, currentPath, currentData, 0o600)
	reloaded := &BookmarkStore{path: legacyPath, legacyPath: legacyPath}
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Bookmarks) != 1 || reloaded.Bookmarks[0].Name != "recovered legacy" {
		t.Fatalf("parse-error recovery wrote to wrong destination: %#v", reloaded.Bookmarks)
	}
}
