package client

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LegacyConfigStatus reports legacy client files found beside the executable.
type LegacyConfigStatus struct {
	Settings  bool
	Bookmarks bool
}

// Any reports whether at least one legacy client file was found.
func (status LegacyConfigStatus) Any() bool {
	return status.Settings || status.Bookmarks
}

// DetectLegacyConfig securely checks for legacy client files without moving or
// deleting them.
func DetectLegacyConfig() (LegacyConfigStatus, error) {
	settingsCurrent, settingsPathErr := configFilePath("settings.yaml")
	bookmarksCurrent, bookmarksPathErr := configFilePath("servers.yaml")
	if err := errors.Join(settingsPathErr, bookmarksPathErr); err != nil {
		return LegacyConfigStatus{}, err
	}
	return detectLegacyConfigPaths(
		legacyFilePath("settings.yaml"), settingsCurrent,
		legacyFilePath("servers.yaml"), bookmarksCurrent,
	)
}

func detectLegacyConfigPaths(settingsLegacy, settingsCurrent, bookmarksLegacy, bookmarksCurrent string) (LegacyConfigStatus, error) {
	settings, settingsErr := legacyPrivateFileExistsPath(settingsLegacy, settingsCurrent)
	bookmarks, bookmarksErr := legacyPrivateFileExistsPath(bookmarksLegacy, bookmarksCurrent)
	return LegacyConfigStatus{Settings: settings, Bookmarks: bookmarks}, errors.Join(settingsErr, bookmarksErr)
}

func legacyPrivateFileExistsPath(legacyPath, currentPath string) (bool, error) {
	if legacyPath == currentPath {
		return false, nil
	}
	snapshot, err := openPrivateFileSnapshot(legacyPath, false)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect legacy %q: %w", legacyPath, err)
	}
	return true, snapshot.Close()
}

// MigrateLegacyConfig copies only the legacy files in the user-confirmed
// detection status and removes each original only after successful publication.
func MigrateLegacyConfig(confirmed LegacyConfigStatus) error {
	return migrateConfirmedLegacyConfig(
		confirmed,
		func() error { return migrateLegacyFile("settings.yaml", validateSettings) },
		func() error { return migrateLegacyFile("servers.yaml", validateBookmarks) },
	)
}

func migrateConfirmedLegacyConfig(confirmed LegacyConfigStatus, migrateSettings, migrateBookmarks func() error) error {
	var settingsErr, bookmarksErr error
	if confirmed.Settings {
		settingsErr = migrateSettings()
	}
	if confirmed.Bookmarks {
		bookmarksErr = migrateBookmarks()
	}
	return errors.Join(settingsErr, bookmarksErr)
}

func migrateLegacyFile(name string, validate func([]byte) error) error {
	legacyPath := legacyFilePath(name)
	currentPath, err := configFilePath(name)
	if err != nil {
		return fmt.Errorf("migrate %s: %w", name, err)
	}
	return migrateLegacyFilePaths(name, legacyPath, currentPath, validate)
}

func migrateLegacyFilePaths(name, legacyPath, currentPath string, validate func([]byte) error) error {
	return migrateLegacyFilePathsAfterPublish(name, legacyPath, currentPath, validate, nil)
}

func migrateLegacyFilePathsAfterPublish(name, legacyPath, currentPath string, validate func([]byte) error, afterPublish func()) error {
	if legacyPath == currentPath {
		return nil
	}
	legacy, err := openPrivateFileSnapshot(legacyPath, false)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("migrate %s: %w", name, err)
	}
	defer func() { _ = legacy.Close() }()
	if err := validate(legacy.data); err != nil {
		return fmt.Errorf("parse legacy %s: %w", name, err)
	}

	if err := writePrivateFileIfAbsent(currentPath, legacy.data); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("publish %s: %w", name, err)
	}
	current, err := openPrivateFileSnapshot(currentPath, true)
	if err != nil {
		return fmt.Errorf("read current %s: %w", name, err)
	}
	defer func() { _ = current.Close() }()
	if !bytes.Equal(current.data, legacy.data) {
		return fmt.Errorf("migrate %s: current and legacy files differ; legacy file was kept", name)
	}
	if afterPublish != nil {
		afterPublish()
	}
	if err := current.Verify(); err != nil {
		return fmt.Errorf("verify current %s before legacy removal: %w", name, err)
	}
	if err := legacy.Remove(); err != nil {
		return fmt.Errorf("remove legacy %s: %w", name, err)
	}
	return nil
}

func validateSettings(data []byte) error {
	settings := DefaultSettings()
	return yaml.Unmarshal(data, settings)
}

func validateBookmarks(data []byte) error {
	bookmarks := BookmarkStore{}
	return yaml.Unmarshal(data, &bookmarks)
}
