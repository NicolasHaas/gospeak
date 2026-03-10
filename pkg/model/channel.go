package model

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

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
