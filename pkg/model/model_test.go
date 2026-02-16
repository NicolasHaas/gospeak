package model

import (
	"strings"
	"testing"
)

func TestValidateUsername(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"valid simple", "alice", nil},
		{"valid with numbers", "user123", nil},
		{"valid with underscore", "my_user", nil},
		{"valid with hyphen", "my-user", nil},
		{"valid mixed", "A-b_3", nil},
		{"valid max length", strings.Repeat("a", MaxUsernameLength), nil},
		{"empty", "", ErrUsernameEmpty},
		{"too long", strings.Repeat("a", MaxUsernameLength+1), ErrUsernameTooLong},
		{"way too long", strings.Repeat("x", 65), ErrUsernameTooLong},
		{"contains space", "has space", ErrUsernameInvalidChars},
		{"contains dot", "user.name", ErrUsernameInvalidChars},
		{"contains @", "user@name", ErrUsernameInvalidChars},
		{"unicode letter", "ñoño", ErrUsernameInvalidChars},
		{"emoji", "user😀", ErrUsernameInvalidChars},
		{"tab character", "user\tname", ErrUsernameInvalidChars},
		{"newline", "user\nname", ErrUsernameInvalidChars},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUsername(tt.input)
			if err != tt.wantErr {
				t.Errorf("ValidateUsername(%q) = %v, want %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestRoleValid(t *testing.T) {
	tests := []struct {
		name string
		role Role
		want bool
	}{
		{"RoleUser", RoleUser, true},
		{"RoleModerator", RoleModerator, true},
		{"RoleAdmin", RoleAdmin, true},
		{"negative", Role(-1), false},
		{"three", Role(3), false},
		{"large", Role(99), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.role.Valid(); got != tt.want {
				t.Errorf("Role(%d).Valid() = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}

func TestRoleString(t *testing.T) {
	tests := []struct {
		role Role
		want string
	}{
		{RoleUser, "user"},
		{RoleModerator, "moderator"},
		{RoleAdmin, "admin"},
		{Role(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.role.String(); got != tt.want {
				t.Errorf("Role(%d).String() = %q, want %q", tt.role, got, tt.want)
			}
		})
	}
}

func TestParseRole(t *testing.T) {
	tests := []struct {
		input string
		want  Role
	}{
		{"admin", RoleAdmin},
		{"moderator", RoleModerator},
		{"user", RoleUser},
		{"", RoleUser},
		{"unknown", RoleUser},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := ParseRole(tt.input); got != tt.want {
				t.Errorf("ParseRole(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
