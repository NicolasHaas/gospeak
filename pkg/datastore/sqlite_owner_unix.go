//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package datastore

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

func verifySQLiteFileOwner(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat open file: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect file owner")
	}
	ownerUID := strconv.FormatUint(uint64(stat.Uid), 10)
	currentUID := strconv.Itoa(os.Geteuid())
	if ownerUID != currentUID {
		return fmt.Errorf("file is owned by uid %s, current uid is %s", ownerUID, currentUID)
	}
	return nil
}
