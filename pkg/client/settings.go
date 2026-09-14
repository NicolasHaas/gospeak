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
	path         string
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

// LoadLegacySettings loads settings directly from the legacy location. It is
// used when the user explicitly declines migration.
func LoadLegacySettings() *Settings {
	path := legacyFilePath("settings.yaml")
	return loadSettings(path, "")
}

func loadSettings(path, legacyPath string) *Settings {
	s := DefaultSettings()
	loadedPath := path
	data, err := readPrivateFile(path, path != legacyPath)
	if os.IsNotExist(err) && legacyPath != "" && legacyPath != path {
		data, err = readPrivateFile(legacyPath, false)
		loadedPath = legacyPath
	}
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("read settings", "err", err)
		}
		return s
	}
	s.path = loadedPath
	if err := yaml.Unmarshal(data, s); err != nil {
		slog.Error("parse settings", "err", err)
		return s
	}
	return s
}

// Save writes settings atomically to the user config directory.
func (s *Settings) Save() error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	path := s.path
	if path == "" {
		var err error
		path, err = configFilePath("settings.yaml")
		if err != nil {
			return err
		}
	}
	return writePrivateFile(path, data)
}
