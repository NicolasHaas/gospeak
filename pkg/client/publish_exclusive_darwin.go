//go:build darwin

package client

import "golang.org/x/sys/unix"

func supportsPrivateExclusivePublication() bool { return true }

func publishPrivateExclusive(dirFD int, temporaryName, destinationName string) error {
	return unix.RenameatxNp(dirFD, temporaryName, dirFD, destinationName, unix.RENAME_EXCL)
}
