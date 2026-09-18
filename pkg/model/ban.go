package model

import (
	"fmt"
	"net/netip"
	"time"
)

// Ban represents one active account or exact-address ban.
type Ban struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"` // positive for account bans
	Username  string    `json:"username,omitempty"`
	IP        string    `json:"ip"` // canonical exact address for IP bans
	BannedBy  int64     `json:"banned_by"`
	ExpiresAt time.Time `json:"expires_at"` // zero = permanent
	CreatedAt time.Time `json:"created_at"`
}

// CanonicalIPAddress validates an exact address and returns its stable textual
// form. IPv4-mapped IPv6 addresses collapse to IPv4; CIDRs, hostnames, ports,
// and scoped addresses are rejected.
func CanonicalIPAddress(value string) (string, error) {
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return "", fmt.Errorf("invalid exact IP address: %w", err)
	}
	if addr.Zone() != "" {
		return "", fmt.Errorf("scoped IP addresses are not supported")
	}
	return addr.Unmap().String(), nil
}
