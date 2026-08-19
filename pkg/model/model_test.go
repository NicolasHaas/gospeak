package model

import (
	"strings"
	"testing"
	"time"
)

func TestChannel_NewChannel_DefaultValues(t *testing.T) {
	ch := NewChannel()
	tests := []struct {
		name     string
		got      any
		expected any
	}{
		{"Name", ch.Name, ChannelDefaultName},
		{"Description", ch.Description, ChannelDefaultDescription},
		{"MaxUsers", ch.MaxUsers, ChannelDefaultMaxUsers},
		{"ParentID", ch.ParentID, int64(ChannelDefaultParentID)},
		{"IsTemp", ch.IsTemp, ChannelDefaultIsTemp},
		{"AllowSubChannels", ch.AllowSubChannels, ChannelDefaultAllowedSubChannels},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("NewChannel().%s = %v, want %v", tt.name, tt.got, tt.expected)
			}
		})
	}
}

func TestChannel_Validate(t *testing.T) {
	tests := []struct {
		name    string
		channel *Channel
		wantErr error
	}{
		{
			"valid channel",
			&Channel{
				Name:        "General",
				Description: "Main channel",
				MaxUsers:    50,
			},
			nil,
		},
		{
			"empty name",
			&Channel{
				Name:        "",
				Description: "Bad",
			},
			ErrChannelNameEmpty,
		},
		{
			"name too long",
			&Channel{
				Name: strings.Repeat("a", MaxChannelNameLength+1),
			},
			ErrChannelNameTooLong,
		},
		{
			"name with invalid UTF-8",
			&Channel{Name: string([]byte{'s', 'a', 'f', 'e', 0xff})},
			ErrChannelNameInvalidUTF8,
		},
		{
			"name with control character",
			&Channel{Name: "General\x1b"},
			ErrChannelNameControl,
		},
		{
			"name with Unicode format character",
			&Channel{Name: "safe\u202eevil"},
			ErrChannelNameControl,
		},
		{
			"description too long",
			&Channel{
				Name:        "Valid",
				Description: strings.Repeat("x", MaxChannelDescLength+1),
			},
			ErrChannelDescTooLong,
		},
		{
			"description with invalid UTF-8",
			&Channel{Name: "Valid", Description: string([]byte{'b', 'a', 'd', 0xff})},
			ErrChannelDescInvalidUTF8,
		},
		{
			"description with control character",
			&Channel{Name: "Valid", Description: "first line\nsecond line"},
			ErrChannelDescControl,
		},
		{
			"description with Unicode format character",
			&Channel{Name: "Valid", Description: "zero\u200bwidth"},
			ErrChannelDescControl,
		},
		{
			"max users negative",
			&Channel{
				Name:     "Bad",
				MaxUsers: -1,
			},
			ErrChannelMaxUsers,
		},
		{
			"max users too high",
			&Channel{
				Name:     "Bad",
				MaxUsers: MaxChannelUsers + 1,
			},
			ErrChannelMaxUsers,
		},
		{
			"negative parent id",
			&Channel{
				Name:     "Valid",
				ParentID: -1,
			},
			ErrChannelParentID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.channel.Validate()
			if err != tt.wantErr {
				t.Errorf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestMessage_Validate(t *testing.T) {
	tests := []struct {
		name    string
		message Message
		wantErr error
	}{
		{"valid message", Message{Body: "Hello, world!"}, nil},
		{"empty body", Message{Body: ""}, ErrMessageBodyEmpty},
		{"body only spaces", Message{Body: "    "}, ErrMessageBodyEmpty},
		{"body too long", Message{Body: strings.Repeat("a", MessageMaxBodyLength+1)}, ErrMessageBodyTooLong},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.message.Validate()
			if err != tt.wantErr {
				t.Errorf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestToken_IsExpired(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name  string
		token Token
		want  bool
	}{
		{
			"no expiry (zero time)",
			Token{ExpiresAt: time.Time{}},
			false,
		},
		{
			"expires in future",
			Token{ExpiresAt: now.Add(1 * time.Hour)},
			false,
		},
		{
			"expired in past",
			Token{ExpiresAt: now.Add(-1 * time.Hour)},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.token.IsExpired(); got != tt.want {
				t.Errorf("IsExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestToken_IsExhausted(t *testing.T) {
	tests := []struct {
		name  string
		token Token
		want  bool
	}{
		{
			"unlimited uses (MaxUses=0)",
			Token{MaxUses: 0, UseCount: 100},
			false,
		},
		{
			"not exhausted (UseCount < MaxUses)",
			Token{MaxUses: 5, UseCount: 3},
			false,
		},
		{
			"exhausted (UseCount == MaxUses)",
			Token{MaxUses: 5, UseCount: 5},
			true,
		},
		{
			"exhausted (UseCount > MaxUses)",
			Token{MaxUses: 5, UseCount: 6},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.token.IsExhausted(); got != tt.want {
				t.Errorf("IsExhausted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUser_ValidateUsername(t *testing.T) {
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
		{"valid unicode latin", "ñoño", nil},
		{"valid cyrillic", "Иван", nil},
		{"valid cjk", "太郎", nil},
		{"valid accented", "café", nil},
		{"valid lithuanian", "Žilvinas", nil},
		{"empty", "", ErrUsernameEmpty},
		{"too long", strings.Repeat("a", MaxUsernameLength+1), ErrUsernameTooLong},
		{"way too long", strings.Repeat("x", 65), ErrUsernameTooLong},
		{"too long unicode", strings.Repeat("ñ", MaxUsernameLength+1), ErrUsernameTooLong},
		{"contains space", "has space", ErrUsernameInvalidChars},
		{"contains dot", "user.name", ErrUsernameInvalidChars},
		{"contains @", "user@name", ErrUsernameInvalidChars},
		{"emoji", "user😀", ErrUsernameInvalidChars},
		{"tab character", "user\tname", ErrUsernameInvalidChars},
		{"newline", "user\nname", ErrUsernameInvalidChars},
		{"zero-width space", "user\u200Bname", ErrUsernameInvalidChars},
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

func TestRole_Valid(t *testing.T) {
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

func TestRole_String(t *testing.T) {
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

func TestRole_ParseRole(t *testing.T) {
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
