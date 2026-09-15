//go:build aix || dragonfly || freebsd || netbsd || openbsd || solaris

package client

func supportsPrivateExclusivePublication() bool { return false }

func publishPrivateExclusive(_ int, _, _ string) error {
	return errExclusiveConfigPublicationUnsupported
}
