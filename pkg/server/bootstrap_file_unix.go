//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package server

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func prepareBootstrapCredentialFile(path string, data []byte) (string, error) {
	return prepareTLSFile(path, data, 0o600)
}

func syncBootstrapCredentialDirectory(path string) error {
	dir, err := os.Open(path) //nolint:gosec // path comes from operator-provided DataDir
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func readBootstrapCredentialFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open credential file %q: invalid file descriptor", path)
	}
	defer func() { _ = file.Close() }()

	before, err := inspectBootstrapCredentialFD(fd, path)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, bootstrapTokenHexLength+2))
	if err != nil {
		return nil, fmt.Errorf("read credential file %q: %w", path, err)
	}
	if len(data) != bootstrapTokenHexLength+1 {
		return nil, fmt.Errorf("credential file %q has an invalid size", path)
	}
	after, err := inspectBootstrapCredentialFD(fd, path)
	if err != nil {
		return nil, err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Uid != after.Uid || before.Size != after.Size {
		return nil, fmt.Errorf("credential file %q changed while being read", path)
	}
	return data, nil
}

func inspectBootstrapCredentialFD(fd int, path string) (*unix.Stat_t, error) {
	stat := new(unix.Stat_t)
	if err := unix.Fstat(fd, stat); err != nil {
		return nil, fmt.Errorf("inspect credential file %q: %w", path, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("credential path %q is not a regular file", path)
	}
	euid := int64(os.Geteuid())
	if int64(stat.Uid) != euid {
		return nil, fmt.Errorf("credential file %q is owned by uid %d, want %d", path, stat.Uid, euid)
	}
	if permissions := stat.Mode & 0o7777; permissions != 0o600 {
		return nil, fmt.Errorf("credential file %q permissions are %04o, want 0600", path, permissions)
	}
	if stat.Size != bootstrapTokenHexLength+1 {
		return nil, fmt.Errorf("credential file %q has an invalid size", path)
	}
	return stat, nil
}
