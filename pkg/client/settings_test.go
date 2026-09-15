package client

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadSettingsRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	path := filepath.Join(dir, "settings.yaml")
	data := []byte("audio_input: Symlink Mic\n")
	if err := os.WriteFile(target, data, 0o644); err != nil { //nolint:gosec // permissive symlink-target fixture
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	settings := loadSettings(path, "")
	if settings.AudioInput != "" {
		t.Fatalf("loaded settings through symlink: %#v", settings)
	}
	assertContentsAndMode(t, target, data, 0o644)
}

func TestLoadSettingsRejectsSymlinkedLegacyFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	legacyPath := filepath.Join(dir, "legacy", "settings.yaml")
	configPath := filepath.Join(dir, "config", "settings.yaml")
	data := []byte("audio_input: Legacy Symlink Mic\n")
	if err := os.WriteFile(target, data, 0o644); err != nil { //nolint:gosec // permissive symlink-target fixture
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, legacyPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	settings := loadSettings(configPath, legacyPath)
	if settings.AudioInput != "" {
		t.Fatalf("migrated settings through symlink: %#v", settings)
	}
	if _, err := os.Lstat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config file created from unsafe legacy path: %v", err)
	}
	assertContentsAndMode(t, target, data, 0o644)
}

func TestLoadSettingsDoesNotAutomaticallyMigrateLegacyFile(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "settings.yaml")
	configPath := filepath.Join(dir, "config", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("audio_input: Studio Mic\naudio_output: USB Headset\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	settings := loadSettings(configPath, legacyPath)
	if settings.AudioInput != "Studio Mic" || settings.AudioOutput != "USB Headset" {
		t.Fatalf("migrated settings = %#v", settings)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("automatic migration created current settings: %v", err)
	}
	assertContentsAndMode(t, legacyPath, []byte("audio_input: Studio Mic\naudio_output: USB Headset\n"), 0o600)
	settings.AudioInput = "Changed Legacy Mic"
	if err := settings.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("saving declined migration created current settings: %v", err)
	}
	reloaded := loadSettings(configPath, legacyPath)
	if reloaded.AudioInput != "Changed Legacy Mic" {
		t.Fatalf("legacy save was not retained: %#v", reloaded)
	}
}

func TestConfigFilePathUsesUserConfigDirectory(t *testing.T) {
	root := t.TempDir()
	var configHome string
	switch runtime.GOOS {
	case "windows":
		t.Setenv("AppData", root)
		configHome = root
	case "darwin", "ios":
		t.Setenv("HOME", root)
		configHome = filepath.Join(root, "Library", "Application Support")
	case "plan9":
		t.Setenv("home", root)
		configHome = filepath.Join(root, "lib")
	default:
		t.Setenv("XDG_CONFIG_HOME", root)
		configHome = root
	}

	got, err := configFilePath("settings.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(configHome, "gospeak", "settings.yaml")
	if got != want {
		t.Fatalf("config path = %q, want %q", got, want)
	}
}

func TestWritePrivateFileReplacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gospeak", "settings.yaml")
	if err := writePrivateFile(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is inside the test's temporary directory
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("settings contents = %q, want new", data)
	}
	assertPrivateFile(t, path)
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "settings.yaml" {
		t.Fatalf("config directory entries = %v", entries)
	}
}

func TestWritePrivateFileRejectsSymlinkedConfigDirectory(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "target")
	configDir := filepath.Join(root, "gospeak")
	if err := os.Mkdir(targetDir, 0o755); err != nil { //nolint:gosec // permissive symlink-target fixture
		t.Fatal(err)
	}
	if err := os.Symlink(targetDir, configDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := writePrivateFile(filepath.Join(configDir, "settings.yaml"), []byte("secret")); err == nil {
		t.Fatal("writePrivateFile accepted symlinked config directory")
	}
	if _, err := os.Lstat(filepath.Join(targetDir, "settings.yaml")); !os.IsNotExist(err) {
		t.Fatalf("write escaped into symlink target: %v", err)
	}
	info, err := os.Stat(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("symlink target mode = %o, want 755", info.Mode().Perm())
	}
}

func TestWritePrivateFileRejectsSymlinkDestination(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	path := filepath.Join(dir, "settings.yaml")
	data := []byte("target")
	if err := os.WriteFile(target, data, 0o644); err != nil { //nolint:gosec // permissive symlink-target fixture
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := writePrivateFile(path, []byte("replacement")); err == nil {
		t.Fatal("writePrivateFile accepted symlink destination")
	}
	assertContentsAndMode(t, target, data, 0o644)
}

func assertPrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %o, want 600", path, info.Mode().Perm())
	}
}

func assertContentsAndMode(t *testing.T, path string, want []byte, mode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path) //nolint:gosec // test helper receives temporary paths only
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s contents = %q, want %q", path, got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), mode)
	}
}

func TestWritePrivateFileRejectsSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	ancestor := filepath.Join(root, "config-home")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, ancestor); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path := filepath.Join(ancestor, "gospeak", "settings.yaml")
	if err := writePrivateFile(path, []byte("secret")); err == nil {
		t.Fatal("writePrivateFile accepted a symlinked ancestor")
	}
	if _, err := os.Lstat(filepath.Join(target, "gospeak")); !os.IsNotExist(err) {
		t.Fatalf("write created content beneath the symlink target: %v", err)
	}
}

func TestWritePrivateFileIfAbsentDoesNotClobber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gospeak", "settings.yaml")
	if err := writePrivateFile(path, []byte("current")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileIfAbsent(path, []byte("legacy")); !os.IsExist(err) {
		t.Fatalf("writePrivateFileIfAbsent error = %v, want exists", err)
	}
	data, err := readPrivateFile(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "current" {
		t.Fatalf("current config = %q, want current", data)
	}
}

func TestLoadSettingsRejectsHardLinkedFileWithoutChangingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	path := filepath.Join(dir, "settings.yaml")
	data := []byte("audio_input: Hard Link Mic\n")
	if err := os.WriteFile(target, data, 0o644); err != nil { //nolint:gosec // permissive hard-link target fixture
		t.Fatal(err)
	}
	if err := os.Link(target, path); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	settings := loadSettings(path, "")
	if settings.AudioInput != "" {
		t.Fatalf("loaded settings through hard link: %#v", settings)
	}
	assertContentsAndMode(t, target, data, 0o644)
}

func TestLoadSettingsLeavesLegacyFileWhenCurrentExists(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "settings.yaml")
	configPath := filepath.Join(dir, "config", "settings.yaml")
	if err := os.Mkdir(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("audio_input: Legacy Mic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(configPath, []byte("audio_input: Current Mic\n")); err != nil {
		t.Fatal(err)
	}

	settings := loadSettings(configPath, legacyPath)
	if settings.AudioInput != "Current Mic" {
		t.Fatalf("loaded settings = %#v", settings)
	}
	assertContentsAndMode(t, legacyPath, []byte("audio_input: Legacy Mic\n"), 0o600)
}

func TestDeclinedSettingsMigrationUsesLegacyWhenCurrentAlsoExists(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "settings.yaml")
	currentPath := filepath.Join(dir, "config", "settings.yaml")
	if err := os.Mkdir(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("audio_input: Legacy Mic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	currentData := []byte("audio_input: Current Mic\n")
	if err := writePrivateFile(currentPath, currentData); err != nil {
		t.Fatal(err)
	}
	settings := loadSettings(legacyPath, "")
	settings.AudioInput = "Updated Legacy Mic"
	if err := settings.Save(); err != nil {
		t.Fatal(err)
	}
	assertContentsAndMode(t, currentPath, currentData, 0o600)
	reloaded := loadSettings(legacyPath, "")
	if reloaded.AudioInput != "Updated Legacy Mic" {
		t.Fatalf("declined migration did not retain legacy destination: %#v", reloaded)
	}
}
