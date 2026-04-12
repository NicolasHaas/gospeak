package client

import (
	"log/slog"
	"os"

	"gopkg.in/yaml.v3"
)

// Settings stores user preferences persisted as YAML in the user config directory.
type Settings struct {
	path         string
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
	s := DefaultSettings()
	path, err := configFilePath(settingsFileName)
	s.path = path
	if err != nil {
		slog.Warn("resolve settings path", "err", err)
	}
	if err := migrateLegacyConfigFile(path, settingsFileName); err != nil {
		slog.Warn("migrate settings", "err", err)
	}

	data, err := os.ReadFile(path)
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

	if s.path == "" {
		path, pathErr := configFilePath(settingsFileName)
		if pathErr != nil {
			slog.Warn("resolve settings path", "err", pathErr)
		}
		if path == "" {
			return pathErr
		}
		s.path = path
	}

	if err := ensureParentDir(s.path); err != nil {
		return err
	}

	return os.WriteFile(s.path, data, 0600)
}
