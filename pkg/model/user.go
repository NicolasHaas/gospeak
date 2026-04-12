package model

import (
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxUsernameLength = 32

var ErrUsernameEmpty = errors.New("username must not be empty")
var ErrUsernameTooLong = fmt.Errorf("username must not exceed %d characters", MaxUsernameLength)
var ErrUsernameInvalidChars = errors.New("username must contain only letters, digits, underscores, or hyphens")
var ErrInvalidRole = errors.New("invalid role: must be user (0), moderator (1), or admin (2)")

// User represents a registered user.
type User struct {
	ID                     int64     `json:"id"`
	Username               string    `json:"username"`
	Role                   Role      `json:"role"`
	PersonalTokenHash      string    `json:"-"`
	PersonalTokenCreatedAt time.Time `json:"-"`
	CreatedAt              time.Time `json:"created_at"`
}

// ValidateUsername checks that a username is 1-32 Unicode letters, digits,
// underscores, or hyphens. Returns nil on success or a descriptive error.
func ValidateUsername(name string) error {
	if len(name) == 0 {
		return ErrUsernameEmpty
	}
	if utf8.RuneCountInString(name) > MaxUsernameLength {
		return ErrUsernameTooLong
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			continue
		}
		return ErrUsernameInvalidChars
	}
	return nil
}
