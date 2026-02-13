// Package model defines the core domain types for GoSpeak.
package model

import (
	"net"
	"time"
)

// Role represents a user's permission level.
type Role int

const (
	RoleUser      Role = iota // Default role, can join channels and talk
	RoleModerator             // Can kick users
	RoleAdmin                 // Full control: create/delete channels, manage tokens, kick, ban
)

func (r Role) String() string {
	switch r {
	case RoleUser:
		return "user"
	case RoleModerator:
		return "moderator"
	case RoleAdmin:
		return "admin"
	default:
		return "unknown"
	}
}

// ParseRole converts a string to a Role.
func ParseRole(s string) Role {
	switch s {
	case "admin":
		return RoleAdmin
	case "moderator":
		return RoleModerator
	default:
		return RoleUser
	}
}

// Permission represents a specific action that can be checked against a role.
type Permission int

const (
	PermCreateChannel Permission = iota
	PermDeleteChannel
	PermKickUser
	PermBanUser
	PermManageTokens
	PermEditChannel
	PermManageRoles
)

// User represents a registered user.
type User struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// Channel represents a voice channel on the server.
type Channel struct {
	ID               int64     `json:"id"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	MaxUsers         int       `json:"max_users"`          // 0 = unlimited
	ParentID         int64     `json:"parent_id"`          // 0 = root channel
	IsTemp           bool      `json:"is_temp"`            // temp channels auto-delete when empty
	AllowSubChannels bool      `json:"allow_sub_channels"` // users can create temp sub-channels here
	CreatedAt        time.Time `json:"created_at"`
}

// Token represents an invite/auth token.
type Token struct {
	ID           int64     `json:"id"`
	Value        string    `json:"-"`             // raw token value (only shown on creation)
	Hash         string    `json:"-"`             // SHA-256 hash stored in DB
	Role         Role      `json:"role"`          // role granted to user of this token
	ChannelScope int64     `json:"channel_scope"` // 0 = server-wide
	CreatedBy    int64     `json:"created_by"`
	MaxUses      int       `json:"max_uses"` // 0 = unlimited
	UseCount     int       `json:"use_count"`
	ExpiresAt    time.Time `json:"expires_at"`
	CreatedAt    time.Time `json:"created_at"`
}

// IsExpired returns true if the token has expired.
func (t *Token) IsExpired() bool {
	if t.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(t.ExpiresAt)
}

// IsExhausted returns true if the token has no remaining uses.
func (t *Token) IsExhausted() bool {
	if t.MaxUses == 0 {
		return false // unlimited
	}
	return t.UseCount >= t.MaxUses
}

// Ban represents a banned user or IP.
type Ban struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"` // 0 if IP ban
	IP        string    `json:"ip"`      // empty if user ban
	Reason    string    `json:"reason"`
	BannedBy  int64     `json:"banned_by"`
	ExpiresAt time.Time `json:"expires_at"` // zero = permanent
	CreatedAt time.Time `json:"created_at"`
}

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
