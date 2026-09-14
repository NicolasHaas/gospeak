//go:build linux

package client

import "golang.org/x/sys/unix"

func supportsPrivateExclusivePublication() bool { return true }

func publishPrivateExclusive(dirFD int, temporaryName, destinationName string) error {
	return unix.Renameat2(dirFD, temporaryName, dirFD, destinationName, unix.RENAME_NOREPLACE)
}
