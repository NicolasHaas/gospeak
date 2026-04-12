package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigFilePathUsesEnvOverride(t *testing.T) {
	t.Setenv(configDirEnv, filepath.Join(t.TempDir(), "override"))

	path, err := configFilePath(settingsFileName)
	if err != nil {
		t.Fatalf("configFilePath returned error: %v", err)
	}

	if got, want := path, filepath.Join(os.Getenv(configDirEnv), settingsFileName); got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}

	legacyPath, err := legacyConfigFilePath(settingsFileName)
	if err != nil {
		t.Fatalf("legacyConfigFilePath returned error: %v", err)
	}

	if legacyPath == "" {
		t.Fatal("legacyPath should not be empty")
	}
}

func TestConfigFilePathFallsBackToLegacyPath(t *testing.T) {
	legacyBase := t.TempDir()

	restore := stubClientPathFuncs(t, func() (string, error) {
		return "", os.ErrPermission
	}, func() (string, error) {
		return filepath.Join(legacyBase, "gospeak-client"), nil
	})
	defer restore()

	path, err := configFilePath(settingsFileName)
	if err == nil {
		t.Fatal("expected configFilePath to report the user config resolution error")
	}

	want := filepath.Join(legacyBase, settingsFileName)
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestLoadSettingsMigratesLegacyFile(t *testing.T) {
	configBase := t.TempDir()
	legacyBase := t.TempDir()
	legacyPath := filepath.Join(legacyBase, settingsFileName)

	restore := stubClientPathFuncs(t, func() (string, error) {
		return configBase, nil
	}, func() (string, error) {
		return filepath.Join(legacyBase, "gospeak-client"), nil
	})
	defer restore()

	legacyData := []byte("mute_key: F10\nvad_threshold: 123\n")
	if err := os.WriteFile(legacyPath, legacyData, 0600); err != nil {
		t.Fatalf("write legacy settings: %v", err)
	}

	settings := LoadSettings()
	if settings.MuteKey != "F10" {
		t.Fatalf("MuteKey = %q, want %q", settings.MuteKey, "F10")
	}
	if settings.VADThreshold != 123 {
		t.Fatalf("VADThreshold = %v, want 123", settings.VADThreshold)
	}

	newPath := filepath.Join(configBase, configDirName, settingsFileName)
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("expected migrated settings at %q: %v", newPath, err)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("expected legacy settings to remain at %q: %v", legacyPath, err)
	}
}

func TestBookmarkStoreSaveUsesUserConfigDir(t *testing.T) {
	configBase := t.TempDir()
	legacyBase := t.TempDir()

	restore := stubClientPathFuncs(t, func() (string, error) {
		return configBase, nil
	}, func() (string, error) {
		return filepath.Join(legacyBase, "gospeak-client"), nil
	})
	defer restore()

	store := NewBookmarkStore()
	store.Bookmarks = []Bookmark{{Name: "Local", ControlAddr: "127.0.0.1:9600", Username: "nicolas"}}

	if err := store.Save(); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	newPath := filepath.Join(configBase, configDirName, bookmarksFileName)
	if store.path != newPath {
		t.Fatalf("store.path = %q, want %q", store.path, newPath)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("expected bookmarks at %q: %v", newPath, err)
	}
	if _, err := os.Stat(filepath.Join(legacyBase, bookmarksFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected no legacy bookmarks file, got err=%v", err)
	}
}

func stubClientPathFuncs(t *testing.T, userConfigDir func() (string, error), executable func() (string, error)) func() {
	t.Helper()

	prevUserConfigDir := osUserConfigDir
	prevExecutable := osExecutable
	osUserConfigDir = userConfigDir
	osExecutable = executable

	return func() {
		osUserConfigDir = prevUserConfigDir
		osExecutable = prevExecutable
	}
}