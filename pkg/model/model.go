// Package model defines the core domain types for GoSpeak.
package model

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode/utf8"
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

// Valid returns true if the role is a recognised value (User, Moderator, or Admin).
func (r Role) Valid() bool {
	return r >= RoleUser && r <= RoleAdmin
}

// MaxUsernameLength is the maximum allowed length for a username in bytes.
const MaxUsernameLength = 32

// ErrUsernameEmpty is returned when a username is blank.
var ErrUsernameEmpty = errors.New("username must not be empty")

// ErrUsernameTooLong is returned when a username exceeds MaxUsernameLength.
var ErrUsernameTooLong = fmt.Errorf("username must not exceed %d characters", MaxUsernameLength)

// ErrUsernameInvalidChars is returned when a username contains disallowed characters.
var ErrUsernameInvalidChars = errors.New("username must contain only alphanumeric characters, underscores, or hyphens")

// ErrInvalidRole is returned when a role value is not recognised.
var ErrInvalidRole = errors.New("invalid role: must be user (0), moderator (1), or admin (2)")

// ValidateUsername checks that a username is 1-32 ASCII alphanumeric, underscore,
// or hyphen characters. Returns nil on success or a descriptive error.
func ValidateUsername(name string) error {
	if len(name) == 0 {
		return ErrUsernameEmpty
	}
	if len(name) > MaxUsernameLength {
		return ErrUsernameTooLong
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return ErrUsernameInvalidChars
		}
	}
	return nil
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
	ID                     int64     `json:"id"`
	Username               string    `json:"username"`
	Role                   Role      `json:"role"`
	PersonalTokenHash      string    `json:"-"`
	PersonalTokenCreatedAt time.Time `json:"-"`
	CreatedAt              time.Time `json:"created_at"`
}

const (
	ChannelDefaultName               = "Lobby"
	ChannelDefaultDescription        = "Default voice channel"
	ChannelDefaultMaxUsers           = 0
	ChannelDefaultParentID           = 0
	ChannelDefaultIsTemp             = false
	ChannelDefaultAllowedSubChannels = false

	MaxChannelNameLength = 64
	MaxChannelDescLength = 256
	MaxChannelUsers      = 256
)

var ErrChannelNameEmpty = errors.New("channel name must not be empty")
var ErrChannelNameTooLong = errors.New("channel name too long")
var ErrChannelDescTooLong = errors.New("channel description too long")
var ErrChannelMaxUsers = errors.New("channel max users out of range")
var ErrChannelParentID = errors.New("channel parent id out of range")

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

// Creates and returns a new channel using default values
//
// Can be expanded in the future to accept opts ...ChannelOptions
func NewChannel() *Channel {
	return &Channel{
		Name:             ChannelDefaultName,
		Description:      ChannelDefaultDescription,
		MaxUsers:         ChannelDefaultMaxUsers,
		ParentID:         ChannelDefaultParentID,
		IsTemp:           ChannelDefaultIsTemp,
		AllowSubChannels: ChannelDefaultAllowedSubChannels,
	}
}

// Validates a channel returning a list of errors
func (ch *Channel) Validate() error {
	// Name
	if strings.TrimSpace(ch.Name) == "" {
		return ErrChannelNameEmpty
	} else if utf8.RuneCountInString(ch.Name) > MaxChannelNameLength {
		return ErrChannelNameTooLong
	}

	// Description
	if utf8.RuneCountInString(ch.Description) > MaxChannelDescLength {
		return ErrChannelDescTooLong
	}

	// MaxUsers
	if ch.MaxUsers < 0 || ch.MaxUsers > MaxChannelUsers {
		return ErrChannelMaxUsers
	}

	// ParentID rule example (optional)
	if ch.ParentID < 0 {
		return ErrChannelParentID
	}

	return nil
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
