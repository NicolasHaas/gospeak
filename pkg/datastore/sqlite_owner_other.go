//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package datastore

import "os"

func verifySQLiteFileOwner(_ *os.File) error {
	return nil
}
