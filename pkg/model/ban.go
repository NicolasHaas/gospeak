package model

import "time"

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
