package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const configDirName = "gospeak"

var errExclusiveConfigPublicationUnsupported = errors.New("atomic no-clobber config publication is unsupported on this platform")

func configFilePath(name string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("user config directory: %w", err)
	}
	return filepath.Join(dir, configDirName, name), nil
}

func legacyFilePath(name string) string {
	executable, err := os.Executable()
	if err != nil {
		return name
	}
	return filepath.Join(filepath.Dir(executable), name)
}

type privateFileSnapshot struct {
	data     []byte
	closeFn  func() error
	verifyFn func() error
	removeFn func() error
}

func (snapshot *privateFileSnapshot) Close() error {
	if snapshot.closeFn == nil {
		return nil
	}
	closeFn := snapshot.closeFn
	snapshot.closeFn = nil
	return closeFn()
}

func (snapshot *privateFileSnapshot) Verify() error {
	if snapshot.verifyFn == nil {
		return fmt.Errorf("verify private file: unsupported")
	}
	return snapshot.verifyFn()
}

func (snapshot *privateFileSnapshot) Remove() error {
	if snapshot.removeFn == nil {
		return fmt.Errorf("remove private file: unsupported")
	}
	if err := snapshot.removeFn(); err != nil {
		return err
	}
	snapshot.removeFn = nil
	return snapshot.Close()
}

func readPrivateFile(path string, protectParent bool) ([]byte, error) {
	snapshot, err := openPrivateFileSnapshot(path, protectParent)
	if err != nil {
		return nil, err
	}
	defer func() { _ = snapshot.Close() }()
	return snapshot.data, nil
}
