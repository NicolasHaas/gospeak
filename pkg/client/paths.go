package client

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	configDirEnv  = "GOSPEAK_CONFIG_DIR"
	configDirName = "gospeak"

	settingsFileName  = "settings.yaml"
	bookmarksFileName = "servers.yaml"
)

var (
	osUserConfigDir = os.UserConfigDir
	osExecutable    = os.Executable
)

func configFilePath(name string) (string, error) {
	if dir := strings.TrimSpace(os.Getenv(configDirEnv)); dir != "" {
		return filepath.Join(dir, name), nil
	}

	baseDir, err := osUserConfigDir()
	if err == nil {
		return filepath.Join(baseDir, configDirName, name), nil
	}

	legacyPath, legacyErr := legacyConfigFilePath(name)
	if legacyErr == nil {
		return legacyPath, err
	}

	return name, err
}

func legacyConfigFilePath(name string) (string, error) {
	exePath, err := osExecutable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exePath), name), nil
}

func migrateLegacyConfigFile(targetPath, name string) error {
	legacyPath, err := legacyConfigFilePath(name)
	if err != nil || targetPath == "" || legacyPath == "" || targetPath == legacyPath {
		return nil
	}

	if _, err := os.Stat(targetPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	data, err := os.ReadFile(legacyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0700); err != nil {
		return err
	}

	return os.WriteFile(targetPath, data, 0600)
}

func ensureParentDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0700)
}