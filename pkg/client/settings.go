package client

import (
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Settings stores user preferences persisted as YAML next to the binary.
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

func settingsPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "settings.yaml"
	}
	return filepath.Join(filepath.Dir(exe), "settings.yaml")
}

// LoadSettings loads settings from YAML or returns defaults.
func LoadSettings() *Settings {
	s := DefaultSettings()
	data, err := os.ReadFile(settingsPath())
	if err != nil {
		return s
	}
	if err := yaml.Unmarshal(data, s); err != nil {
		slog.Error("parse settings", "err", err)
		return DefaultSettings()
	}
	return s
}

// Save writes settings to YAML.
func (s *Settings) Save() error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath(), data, 0600)
}
