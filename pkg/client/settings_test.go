package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSettingsMigratesLegacyFileToUserConfig(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "settings.yaml")
	configPath := filepath.Join(dir, "config", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("audio_input: Studio Mic\naudio_output: USB Headset\n"), 0600); err != nil {
		t.Fatal(err)
	}

	settings := loadSettings(configPath, legacyPath)
	if settings.AudioInput != "Studio Mic" || settings.AudioOutput != "USB Headset" {
		t.Fatalf("migrated settings = %#v", settings)
	}
	assertPrivateFile(t, configPath)
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy settings still exist: %v", err)
	}
}

func TestConfigFilePathUsesUserConfigDirectory(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
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

func assertPrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("%s mode = %o, want 600", path, info.Mode().Perm())
	}
}
