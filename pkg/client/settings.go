package client

import (
	"log/slog"
	"os"

	"gopkg.in/yaml.v3"
)

// Settings stores user preferences persisted as YAML in the user config directory.
type Settings struct {
	MuteKey      string  `yaml:"mute_key"`
	DeafenKey    string  `yaml:"deafen_key"`
	VADThreshold float64 `yaml:"vad_threshold"`
	AudioInput   string  `yaml:"audio_input,omitempty"`
	AudioOutput  string  `yaml:"audio_output,omitempty"`
}

// DefaultSettings returns default settings.
func DefaultSettings() *Settings {
	return &Settings{
		MuteKey:      "F11",
		DeafenKey:    "F12",
		VADThreshold: 200,
	}
}

// LoadSettings loads settings from YAML or returns defaults.
func LoadSettings() *Settings {
	path, err := configFilePath("settings.yaml")
	if err != nil {
		slog.Error("resolve settings path", "err", err)
		path = legacyFilePath("settings.yaml")
	}
	return loadSettings(path, legacyFilePath("settings.yaml"))
}

func loadSettings(path, legacyPath string) *Settings {
	s := DefaultSettings()
	data, err := os.ReadFile(path) //nolint:gosec // path is resolved from the OS user config directory
	if err == nil {
		if err := yaml.Unmarshal(data, s); err != nil {
			slog.Error("parse settings", "err", err)
			return DefaultSettings()
		}
		return s
	}
	if !os.IsNotExist(err) || legacyPath == path {
		if !os.IsNotExist(err) {
			slog.Error("read settings", "err", err)
		}
		return s
	}

	data, err = os.ReadFile(legacyPath) //nolint:gosec // legacy path is derived from the current executable
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("read legacy settings", "err", err)
		}
		return s
	}
	if err := yaml.Unmarshal(data, s); err != nil {
		slog.Error("parse legacy settings", "err", err)
		return DefaultSettings()
	}
	if err := writePrivateFile(path, data); err != nil {
		slog.Error("migrate settings", "err", err)
		return s
	}
	if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("remove migrated legacy settings", "err", err)
	}
	return s
}

// Save writes settings atomically to the user config directory.
func (s *Settings) Save() error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	path, err := configFilePath("settings.yaml")
	if err != nil {
		return err
	}
	return writePrivateFile(path, data)
}
