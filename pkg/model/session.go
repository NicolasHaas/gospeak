package model

import "net"

// Session represents an active client session (in-memory only).
type Session struct {
	ID        uint32
	UserID    int64
	Username  string
	Role      Role
	ChannelID int64
	UDPAddr   *net.UDPAddr
	Muted     bool
	Deafened  bool
}
