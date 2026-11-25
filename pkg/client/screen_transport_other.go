//go:build !windows

package client

import "syscall"

const (
	screenConnectionRefused  = syscall.ECONNREFUSED
	screenNetworkUnreachable = syscall.ENETUNREACH
	screenHostUnreachable    = syscall.EHOSTUNREACH
)
