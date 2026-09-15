package client

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConfirmedMigrationScopeCallsOnlyApprovedFiles(t *testing.T) {
	settingsCalls := 0
	bookmarkCalls := 0
	err := migrateConfirmedLegacyConfig(
		LegacyConfigStatus{Settings: true},
		func() error { settingsCalls++; return nil },
		func() error { bookmarkCalls++; return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if settingsCalls != 1 || bookmarkCalls != 0 {
		t.Fatalf("migration calls = settings:%d bookmarks:%d", settingsCalls, bookmarkCalls)
	}
}

func TestConfirmedLegacyMigrationPublishesExactBytesThenDeletesOriginal(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "settings.yaml")
	currentPath := filepath.Join(dir, "config", "settings.yaml")
	if err := os.Mkdir(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyData := []byte("# preserved comment\naudio_input: Studio Mic\nfuture_setting: keep-me\n")
	if err := os.WriteFile(legacyPath, legacyData, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyFilePaths("settings.yaml", legacyPath, currentPath, validateSettings); err != nil {
		t.Fatal(err)
	}
	settings := loadSettings(currentPath, "")
	if settings.AudioInput != "Studio Mic" {
		t.Fatalf("migrated settings = %#v", settings)
	}
	assertContentsAndMode(t, currentPath, legacyData, 0o600)
	if _, err := os.Lstat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy settings were not deleted: %v", err)
	}
}

func TestConfirmedBookmarkMigrationPreservesCredentialsAndUnknownFields(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "servers.yaml")
	currentPath := filepath.Join(dir, "config", "servers.yaml")
	if err := os.Mkdir(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyData := []byte("bookmarks:\n  - name: private\n    control_addr: private.test:9600\n    token: secret-token\ntrusted_server_pins:\n  private.test:9600: SHA256:pin\nfuture_bookmark_field: keep-me\n")
	if err := os.WriteFile(legacyPath, legacyData, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyFilePaths("servers.yaml", legacyPath, currentPath, validateBookmarks); err != nil {
		t.Fatal(err)
	}
	assertContentsAndMode(t, currentPath, legacyData, 0o600)
	store := &BookmarkStore{path: currentPath}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if len(store.Bookmarks) != 1 || store.Bookmarks[0].Token != "secret-token" || store.PinForAddr("private.test:9600") != "SHA256:pin" {
		t.Fatalf("migrated credentials were not preserved: bookmarks=%#v pins=%#v", store.Bookmarks, store.TrustedServerPins)
	}
	if _, err := os.Lstat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy bookmarks were not deleted: %v", err)
	}
}

func TestConfirmedLegacyMigrationKeepsDifferentCurrentAndLegacyFiles(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "settings.yaml")
	currentPath := filepath.Join(dir, "config", "settings.yaml")
	if err := os.Mkdir(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyData := []byte("audio_input: Legacy Mic\n")
	if err := os.WriteFile(legacyPath, legacyData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(currentPath, []byte("audio_input: Current Mic\n")); err != nil {
		t.Fatal(err)
	}

	err := migrateLegacyFilePaths("settings.yaml", legacyPath, currentPath, validateSettings)
	if err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("migration error = %v, want differing-file error", err)
	}
	assertContentsAndMode(t, legacyPath, legacyData, 0o600)
	settings := loadSettings(currentPath, "")
	if settings.AudioInput != "Current Mic" {
		t.Fatalf("current settings changed: %#v", settings)
	}
}

func TestConfirmedLegacyMigrationKeepsSemanticallyEqualButByteDifferentFiles(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "settings.yaml")
	currentPath := filepath.Join(dir, "config", "settings.yaml")
	if err := os.Mkdir(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyData := []byte("audio_input: Studio Mic\n")
	currentData := []byte("# separately written\naudio_input: Studio Mic\n")
	if err := os.WriteFile(legacyPath, legacyData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(currentPath, currentData); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyFilePaths("settings.yaml", legacyPath, currentPath, validateSettings); err == nil {
		t.Fatal("migration deleted a bytewise-different legacy file")
	}
	assertContentsAndMode(t, legacyPath, legacyData, 0o600)
	assertContentsAndMode(t, currentPath, currentData, 0o600)
}

func TestConfirmedLegacyMigrationPreservesSourceChangedAfterPublication(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows denies concurrent writers while a migration snapshot is open")
	}
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "settings.yaml")
	currentPath := filepath.Join(dir, "config", "settings.yaml")
	if err := os.Mkdir(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("audio_input: Original\n")
	mutated := []byte("audio_input: Mutated!\n")
	if len(original) != len(mutated) {
		t.Fatal("fixture sizes differ")
	}
	if err := os.WriteFile(legacyPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	err := migrateLegacyFilePathsAfterPublish("settings.yaml", legacyPath, currentPath, validateSettings, func() {
		if writeErr := os.WriteFile(legacyPath, mutated, 0o600); writeErr != nil {
			t.Fatalf("mutate legacy after publication: %v", writeErr)
		}
	})
	if err == nil {
		t.Fatal("migration removed a source changed after publication")
	}
	assertContentsAndMode(t, currentPath, original, 0o600)
	assertContentsAndMode(t, legacyPath, mutated, 0o600)
}

func TestConfirmedLegacyMigrationPreservesLegacyWhenCurrentChangesAfterPublication(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows denies concurrent writers while a migration snapshot is open")
	}
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "settings.yaml")
	currentPath := filepath.Join(dir, "config", "settings.yaml")
	if err := os.Mkdir(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("audio_input: Original\n")
	mutated := []byte("audio_input: Mutated!\n")
	if len(original) != len(mutated) {
		t.Fatal("fixture sizes differ")
	}
	if err := os.WriteFile(legacyPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	err := migrateLegacyFilePathsAfterPublish("settings.yaml", legacyPath, currentPath, validateSettings, func() {
		if writeErr := os.WriteFile(currentPath, mutated, 0o600); writeErr != nil {
			t.Fatalf("mutate current after publication: %v", writeErr)
		}
	})
	if err == nil {
		t.Fatal("migration removed legacy after current changed")
	}
	assertContentsAndMode(t, currentPath, mutated, 0o600)
	assertContentsAndMode(t, legacyPath, original, 0o600)
}

func TestConfirmedLegacyMigrationRejectsInvalidDataWithoutPublishing(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "servers.yaml")
	currentPath := filepath.Join(dir, "config", "servers.yaml")
	if err := os.Mkdir(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyData := []byte("bookmarks: [\n")
	if err := os.WriteFile(legacyPath, legacyData, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyFilePaths("servers.yaml", legacyPath, currentPath, validateBookmarks); err == nil {
		t.Fatal("migration accepted invalid bookmark data")
	}
	assertContentsAndMode(t, legacyPath, legacyData, 0o600)
	if _, err := os.Lstat(currentPath); !os.IsNotExist(err) {
		t.Fatalf("invalid migration published current file: %v", err)
	}
}

func TestDetectLegacyConfigReturnsValidStatusAlongsideOtherFileError(t *testing.T) {
	dir := t.TempDir()
	settingsLegacy := filepath.Join(dir, "legacy", "settings.yaml")
	settingsCurrent := filepath.Join(dir, "config", "settings.yaml")
	bookmarksLegacy := filepath.Join(dir, "legacy", "servers.yaml")
	bookmarksCurrent := filepath.Join(dir, "config", "servers.yaml")
	if err := os.Mkdir(filepath.Dir(settingsLegacy), 0o700); err != nil {
		t.Fatal(err)
	}
	settingsData := []byte("audio_input: Studio Mic\n")
	if err := os.WriteFile(settingsLegacy, settingsData, 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "unsafe.yaml")
	if err := os.WriteFile(target, []byte("bookmarks: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, bookmarksLegacy); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	status, err := detectLegacyConfigPaths(settingsLegacy, settingsCurrent, bookmarksLegacy, bookmarksCurrent)
	if err == nil {
		t.Fatal("mixed detection did not report unsafe bookmarks")
	}
	if !status.Settings || status.Bookmarks {
		t.Fatalf("mixed detection status = %#v", status)
	}
	assertContentsAndMode(t, settingsLegacy, settingsData, 0o600)
}

func TestMixedLegacyMigrationCommitsOnlySuccessfulFile(t *testing.T) {
	dir := t.TempDir()
	settingsLegacy := filepath.Join(dir, "legacy", "settings.yaml")
	settingsCurrent := filepath.Join(dir, "config", "settings.yaml")
	bookmarksLegacy := filepath.Join(dir, "legacy", "servers.yaml")
	bookmarksCurrent := filepath.Join(dir, "config", "servers.yaml")
	if err := os.Mkdir(filepath.Dir(settingsLegacy), 0o700); err != nil {
		t.Fatal(err)
	}
	settingsData := []byte("audio_input: Studio Mic\n")
	invalidBookmarks := []byte("bookmarks: [\n")
	if err := os.WriteFile(settingsLegacy, settingsData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bookmarksLegacy, invalidBookmarks, 0o600); err != nil {
		t.Fatal(err)
	}

	settingsErr := migrateLegacyFilePaths("settings.yaml", settingsLegacy, settingsCurrent, validateSettings)
	bookmarksErr := migrateLegacyFilePaths("servers.yaml", bookmarksLegacy, bookmarksCurrent, validateBookmarks)
	if settingsErr != nil || bookmarksErr == nil {
		t.Fatalf("mixed migration errors: settings=%v bookmarks=%v", settingsErr, bookmarksErr)
	}
	assertContentsAndMode(t, settingsCurrent, settingsData, 0o600)
	if _, err := os.Lstat(settingsLegacy); !os.IsNotExist(err) {
		t.Fatalf("successfully migrated settings were not deleted: %v", err)
	}
	assertContentsAndMode(t, bookmarksLegacy, invalidBookmarks, 0o600)
	if _, err := os.Lstat(bookmarksCurrent); !os.IsNotExist(err) {
		t.Fatalf("invalid bookmarks were published: %v", err)
	}
}
