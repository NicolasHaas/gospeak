package model

import (
	"errors"
	"fmt"
	"time"
)

const MaxUsernameLength = 32

var ErrUsernameEmpty = errors.New("username must not be empty")
var ErrUsernameTooLong = fmt.Errorf("username must not exceed %d characters", MaxUsernameLength)
var ErrUsernameInvalidChars = errors.New("username must contain only alphanumeric characters, underscores, or hyphens")
var ErrInvalidRole = errors.New("invalid role: must be user (0), moderator (1), or admin (2)")

// User represents a registered user.
type User struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

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
