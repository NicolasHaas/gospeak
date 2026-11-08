package client

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSettingsFailedLoadBlocksSave(t *testing.T) {
	for _, location := range []string{"current", "legacy", "declined migration"} {
		for _, malformed := range []string{"mute_key: [\n# original preferences\n", "mute_key: F9\naudio_input: Studio Mic\ninput_volume: broken\noutput_volume: 25\n", "mute_key: F9\nvad_threshold: broken\naudio_output: Studio Speakers\n"} {
			t.Run(location+"/"+malformed, func(t *testing.T) {
				current, legacy := settingsPreservationPaths(t)
				path := current
				if location != "current" {
					path = legacy
				}
				if err := writePrivateFile(path, []byte(malformed)); err != nil {
					t.Fatal(err)
				}
				load := func() *Settings { return loadSettings(current, legacy) }
				if location == "declined migration" {
					load = func() *Settings { return loadSettings(legacy, "") }
				}
				s := load()
				defaults := DefaultSettings()
				defaults.path = s.path
				// Compare only persisted preferences, not the failed-load guard.
				if s.MuteKey != defaults.MuteKey || s.DeafenKey != defaults.DeafenKey || s.VADThreshold != defaults.VADThreshold || s.AudioInput != defaults.AudioInput || s.AudioOutput != defaults.AudioOutput || s.InputVolume != defaults.InputVolume || s.OutputVolume != defaults.OutputVolume {
					t.Error("partially decoded malformed settings were adopted")
				}
				s.MuteKey, s.AudioInput, s.InputVolume = "F8", "Changed Mic", 50
				if err := s.Save(); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
					t.Errorf("save after failed load = %v; want preservation error", err)
				}
				assertContentsAndMode(t, path, []byte(malformed), 0o600)
				if location != "current" {
					if _, err := os.Stat(current); !os.IsNotExist(err) {
						t.Fatalf("failed legacy load created current settings: %v", err)
					}
				}
				// Explicit repair followed by reload restores normal persistence.
				if err := writePrivateFile(path, []byte("mute_key: F9\n")); err != nil {
					t.Fatal(err)
				}
				s = load()
				if s.MuteKey != "F9" {
					t.Fatal("repaired preferences were not loaded")
				}
				if err := s.Save(); err != nil {
					t.Fatalf("successful reload did not unblock save: %v", err)
				}
			})
		}
	}
}

func TestSettingsReadFailureBlocksSave(t *testing.T) {
	for _, legacyFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "legacy"}[legacyFailure], func(t *testing.T) {
			current, legacy := settingsPreservationPaths(t)
			path := current
			if legacyFailure {
				path = legacy
			}
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			s := loadSettings(current, legacy)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			original := []byte("mute_key: F9\n# appeared after failed read\n")
			if err := writePrivateFile(path, original); err != nil {
				t.Fatal(err)
			}
			if err := s.Save(); err == nil {
				t.Error("read failure did not block save")
			}
			assertContentsAndMode(t, path, original, 0o600)
			if legacyFailure {
				if _, err := os.Stat(current); !os.IsNotExist(err) {
					t.Fatalf("failed legacy read created current settings: %v", err)
				}
			}
		})
	}
}

func TestSettingsMissingLoadAllowsFirstSave(t *testing.T) {
	current, legacy := settingsPreservationPaths(t)
	s := loadSettings(current, legacy)
	s.MuteKey = "F9"
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if loadSettings(current, legacy).MuteKey != "F9" {
		t.Fatal("first save did not retain preferences")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("first save created legacy settings: %v", err)
	}
	// Moving the file aside explicitly resets preferences without deleting it.
	if err := os.Rename(current, current+".backup"); err != nil {
		t.Fatal(err)
	}
	reset := loadSettings(current, legacy)
	defaults := DefaultSettings()
	defaults.path = reset.path
	if !reflect.DeepEqual(reset, defaults) {
		t.Fatal("missing file did not reset to defaults")
	}
	if err := reset.Save(); err != nil {
		t.Fatal(err)
	}
	if loadSettings(current+".backup", "").MuteKey != "F9" {
		t.Fatal("reset changed original preferences")
	}
}

func settingsPreservationPaths(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"HOME", "home", "AppData", "XDG_CONFIG_HOME"} {
		t.Setenv(name, root)
	}
	current, err := configFilePath("settings.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return current, filepath.Join(root, "legacy", "settings.yaml")
}
