//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package client

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const privateFileMode = 0o600

func openConfigDirectory(path string, create, protect bool) (int, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return -1, fmt.Errorf("resolve config directory %q: %w", path, err)
	}
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, &os.PathError{Op: "open", Path: string(filepath.Separator), Err: err}
	}
	parts := strings.FieldsFunc(filepath.Clean(absolute), func(r rune) bool { return r == filepath.Separator })
	walked := string(filepath.Separator)
	for _, part := range parts {
		next, openErr := unix.Openat(current, part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			if mkdirErr := unix.Mkdirat(current, part, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(current)
				return -1, &os.PathError{Op: "mkdir", Path: filepath.Join(walked, part), Err: mkdirErr}
			}
			next, openErr = unix.Openat(current, part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			_ = unix.Close(current)
			return -1, &os.PathError{Op: "open", Path: filepath.Join(walked, part), Err: openErr}
		}
		_ = unix.Close(current)
		current = next
		walked = filepath.Join(walked, part)
	}

	stat := new(unix.Stat_t)
	if err := unix.Fstat(current, stat); err != nil {
		_ = unix.Close(current)
		return -1, fmt.Errorf("inspect config directory %q: %w", path, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(current)
		return -1, fmt.Errorf("config path %q is not a directory", path)
	}
	if (create || protect) && int64(stat.Uid) != int64(os.Geteuid()) {
		_ = unix.Close(current)
		return -1, fmt.Errorf("config directory %q is not owned by the current user", path)
	}
	if protect {
		if err := unix.Fchmod(current, 0o700); err != nil {
			_ = unix.Close(current)
			return -1, fmt.Errorf("secure config directory %q: %w", path, err)
		}
	}
	return current, nil
}

func inspectPrivateFile(fd int, path string) (*unix.Stat_t, error) {
	stat := new(unix.Stat_t)
	if err := unix.Fstat(fd, stat); err != nil {
		return nil, fmt.Errorf("inspect config file %q: %w", path, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("config path %q is not a regular file", path)
	}
	if int64(stat.Uid) != int64(os.Geteuid()) {
		return nil, fmt.Errorf("config file %q is not owned by the current user", path)
	}
	if stat.Nlink != 1 {
		return nil, fmt.Errorf("config file %q has %d hard links, want 1", path, stat.Nlink)
	}
	return stat, nil
}

func openPrivateFileSnapshot(path string, protectParent bool) (*privateFileSnapshot, error) {
	dirFD, err := openConfigDirectory(filepath.Dir(path), false, protectParent)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	readFD, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = unix.Close(dirFD)
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	initial, err := inspectPrivateFile(readFD, path)
	if err != nil {
		_ = unix.Close(readFD)
		_ = unix.Close(dirFD)
		return nil, err
	}
	if initial.Mode&0o7777 != privateFileMode {
		if err := unix.Fchmod(readFD, privateFileMode); err != nil {
			_ = unix.Close(readFD)
			_ = unix.Close(dirFD)
			return nil, fmt.Errorf("secure config file %q: %w", path, err)
		}
	}
	fd, err := unix.Openat(dirFD, name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = unix.Close(readFD)
		_ = unix.Close(dirFD)
		return nil, &os.PathError{Op: "reopen", Path: path, Err: err}
	}
	reopened, err := inspectPrivateFile(fd, path)
	_ = unix.Close(readFD)
	if err != nil || initial.Dev != reopened.Dev || initial.Ino != reopened.Ino {
		_ = unix.Close(fd)
		_ = unix.Close(dirFD)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("config file %q changed while being secured", path)
	}
	first, err := readAllAtStart(fd)
	if err != nil {
		_ = unix.Close(fd)
		_ = unix.Close(dirFD)
		return nil, &os.PathError{Op: "read", Path: path, Err: err}
	}
	middle, err := inspectPrivateFile(fd, path)
	if err != nil {
		_ = unix.Close(fd)
		_ = unix.Close(dirFD)
		return nil, err
	}
	second, err := readAllAtStart(fd)
	if err != nil {
		_ = unix.Close(fd)
		_ = unix.Close(dirFD)
		return nil, &os.PathError{Op: "reread", Path: path, Err: err}
	}
	after, err := inspectPrivateFile(fd, path)
	if err != nil {
		_ = unix.Close(fd)
		_ = unix.Close(dirFD)
		return nil, err
	}
	if !sameUnixFileState(middle, after) || !bytes.Equal(first, second) {
		_ = unix.Close(fd)
		_ = unix.Close(dirFD)
		return nil, fmt.Errorf("config file %q changed while being read", path)
	}

	snapshot := &privateFileSnapshot{data: first}
	snapshot.closeFn = func() error {
		return errors.Join(unix.Close(fd), unix.Close(dirFD))
	}
	snapshot.verifyFn = func() error {
		return verifyUnixSnapshot(fd, path, first, after)
	}
	snapshot.removeFn = func() error {
		return removeUnixSnapshot(dirFD, fd, name, path, first, after)
	}
	return snapshot, nil
}

func verifyUnixSnapshot(fd int, path string, data []byte, state *unix.Stat_t) error {
	currentData, err := readAllAtStart(fd)
	if err != nil {
		return &os.PathError{Op: "revalidate", Path: path, Err: err}
	}
	currentState, err := inspectPrivateFile(fd, path)
	if err != nil {
		return err
	}
	if !bytes.Equal(currentData, data) || !sameUnixFileState(state, currentState) {
		return fmt.Errorf("config file %q changed after it was read", path)
	}
	return nil
}

func readAllAtStart(fd int) ([]byte, error) {
	var data []byte
	buffer := make([]byte, 32*1024)
	var offset int64
	for {
		count, err := unix.Pread(fd, buffer, offset)
		if count > 0 {
			data = append(data, buffer[:count]...)
			offset += int64(count)
		}
		if errors.Is(err, io.EOF) || count == 0 {
			return data, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func scrubUnixSnapshot(dirFD, fd int, path string) error {
	if err := unix.Ftruncate(fd, 0); err != nil {
		return &os.PathError{Op: "scrub", Path: path, Err: err}
	}
	if err := unix.Fsync(fd); err != nil {
		return &os.PathError{Op: "sync scrubbed", Path: path, Err: err}
	}
	return unix.Fsync(dirFD)
}

func removeUnixSnapshot(dirFD, fd int, name, path string, data []byte, state *unix.Stat_t) error {
	directoryState := new(unix.Stat_t)
	if err := unix.Fstat(dirFD, directoryState); err != nil {
		return &os.PathError{Op: "inspect directory before remove", Path: filepath.Dir(path), Err: err}
	}
	if int64(directoryState.Uid) != int64(os.Geteuid()) || directoryState.Mode&0o200 == 0 || directoryState.Mode&0o022 != 0 {
		return fmt.Errorf("legacy directory %q is not exclusively writable by the current user", filepath.Dir(path))
	}
	if err := verifyUnixSnapshot(fd, path, data, state); err != nil {
		return err
	}
	currentState, err := inspectPrivateFile(fd, path)
	if err != nil {
		return err
	}
	pathState := new(unix.Stat_t)
	if err := unix.Fstatat(dirFD, name, pathState, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &os.PathError{Op: "inspect before scrub", Path: path, Err: err}
	}
	if currentState.Dev != pathState.Dev || currentState.Ino != pathState.Ino {
		return fmt.Errorf("legacy config %q changed before removal", path)
	}
	if err := scrubUnixSnapshot(dirFD, fd, path); err != nil {
		return err
	}
	expected := new(unix.Stat_t)
	if err := unix.Fstat(fd, expected); err != nil {
		return &os.PathError{Op: "inspect scrubbed", Path: path, Err: err}
	}
	current := new(unix.Stat_t)
	if err := unix.Fstatat(dirFD, name, current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &os.PathError{Op: "inspect before remove", Path: path, Err: err}
	}
	if expected.Dev != current.Dev || expected.Ino != current.Ino {
		return fmt.Errorf("legacy config %q changed before removal", path)
	}
	if err := unix.Unlinkat(dirFD, name, 0); err != nil {
		return &os.PathError{Op: "remove", Path: path, Err: err}
	}
	return unix.Fsync(dirFD)
}

func sameUnixFileState(left, right *unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Uid == right.Uid && left.Mode == right.Mode &&
		left.Nlink == right.Nlink && left.Size == right.Size
}

func writePrivateFile(path string, data []byte) error {
	return writePrivateFileMode(path, data, false)
}

func writePrivateFileIfAbsent(path string, data []byte) error {
	return writePrivateFileMode(path, data, true)
}

func writePrivateFileMode(path string, data []byte, exclusive bool) error {
	dir := filepath.Dir(path)
	dirFD, err := openConfigDirectory(dir, true, true)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirFD) }()

	name := filepath.Base(path)
	stat := new(unix.Stat_t)
	if err := unix.Fstatat(dirFD, name, stat, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		if exclusive {
			return &os.PathError{Op: "create", Path: path, Err: os.ErrExist}
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
			return fmt.Errorf("config destination %q is not a single-link regular file", path)
		}
		if int64(stat.Uid) != int64(os.Geteuid()) {
			return fmt.Errorf("config destination %q is not owned by the current user", path)
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("inspect config destination %q: %w", path, err)
	}

	if exclusive && !supportsPrivateExclusivePublication() {
		return errExclusiveConfigPublicationUnsupported
	}
	temporaryName, temporaryFD, err := createPrivateTemp(dirFD)
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	file := os.NewFile(uintptr(temporaryFD), filepath.Join(dir, temporaryName))
	if file == nil {
		_ = unix.Close(temporaryFD)
		return fmt.Errorf("create temporary config: invalid file descriptor")
	}
	if _, err := file.Write(data); err != nil {
		cleanupErr := scrubPrivateTemp(file)
		return errors.Join(fmt.Errorf("write temporary config: %w", err), cleanupErr)
	}
	if err := file.Sync(); err != nil {
		cleanupErr := scrubPrivateTemp(file)
		return errors.Join(fmt.Errorf("sync temporary config: %w", err), cleanupErr)
	}
	if exclusive {
		err = publishPrivateExclusive(dirFD, temporaryName, name)
	} else {
		err = unix.Renameat(dirFD, temporaryName, dirFD, name)
	}
	if err != nil {
		cleanupErr := scrubPrivateTemp(file)
		if exclusive && errors.Is(err, unix.EEXIST) {
			if cleanupErr != nil {
				return fmt.Errorf("destination exists but temporary config cleanup failed: %w", cleanupErr)
			}
			return &os.PathError{Op: "create", Path: path, Err: os.ErrExist}
		}
		return errors.Join(fmt.Errorf("publish config: %w", err), cleanupErr)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close published config: %w", err)
	}
	if err := unix.Fsync(dirFD); err != nil {
		return fmt.Errorf("sync config directory: %w", err)
	}
	return nil
}

func scrubPrivateTemp(file *os.File) error {
	truncateErr := file.Truncate(0)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(truncateErr, syncErr, closeErr)
}

func createPrivateTemp(dirFD int) (string, int, error) {
	for range 100 {
		random, err := randomName()
		if err != nil {
			return "", -1, err
		}
		name := ".gospeak-" + random
		fd, err := unix.Openat(dirFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, privateFileMode)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", -1, err
		}
		return name, fd, nil
	}
	return "", -1, fmt.Errorf("too many temporary-name collisions")
}

func randomName() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return hex.EncodeToString(random), nil
}
