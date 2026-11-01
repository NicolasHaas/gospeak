package client

import "golang.org/x/sys/windows"

const (
	screenConnectionRefused  = windows.WSAECONNREFUSED
	screenNetworkUnreachable = windows.WSAENETUNREACH
	screenHostUnreachable    = windows.WSAEHOSTUNREACH
)
