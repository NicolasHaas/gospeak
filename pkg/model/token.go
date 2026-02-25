package model

import "time"

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
