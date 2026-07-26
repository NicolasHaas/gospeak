package model

import (
	"net"
	"time"
)

// Session represents an active client session (in-memory only).
type Session struct {
	ID                       uint32
	UserID                   int64
	Username                 string
	Role                     Role
	ChannelScope             int64
	ChannelID                int64
	ScreenAuthToken          string
	VoiceRegistrationKey     []byte
	VoiceRegistrationCounter uint64
	VoiceEndpointUpdatedAt   time.Time
	UDPAddr                  *net.UDPAddr
	Muted                    bool
	Deafened                 bool
}
