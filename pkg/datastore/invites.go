package datastore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

// ListInvites reads metadata only, including expired and exhausted invitations.
func (s *baseProvider) ListInvites(afterID int64, limit int) ([]model.Token, bool, error) {
	if afterID < 0 || limit < 1 || limit > MaxInvitePageSize {
		return nil, false, fmt.Errorf("datastore: invalid invite page")
	}
	rows, err := s.QueryContext(context.Background(), `SELECT id, role, channel_scope, created_by, max_uses, use_count, expires_at, created_at FROM tokens WHERE kind = ? AND id > ? ORDER BY id LIMIT ?`, tokenKindInvite, afterID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("datastore: list invites: %w", err)
	}
	defer rows.Close()
	invites := make([]model.Token, 0, limit)
	for rows.Next() {
		if len(invites) == limit {
			return invites, true, nil
		}
		var invite model.Token
		var role int
		var expiry sql.NullString
		var created string
		if err := rows.Scan(&invite.ID, &role, &invite.ChannelScope, &invite.CreatedBy, &invite.MaxUses, &invite.UseCount, &expiry, &created); err != nil {
			return nil, false, fmt.Errorf("datastore: scan invite: %w", err)
		}
		invite.Role = model.Role(role)
		invite.CreatedAt, err = parseDBTime(created)
		if err != nil {
			return nil, false, err
		}
		if expiry.Valid {
			invite.ExpiresAt, err = parseDBTime(expiry.String)
			if err != nil {
				return nil, false, err
			}
		}
		invites = append(invites, invite)
	}
	return invites, false, rows.Err()
}

// RevokeInvite permanently expires an invite under provisioning's writer lock.
// Retain the row: deleting the last token could trigger first-run bootstrap on restart.
func (s *baseProvider) RevokeInvite(id int64) (bool, error) {
	if id <= 0 {
		return false, fmt.Errorf("datastore: invalid invite ID")
	}
	const revokedExpiry = "1970-01-01 00:00:01"
	result, err := s.ExecContext(context.Background(), "UPDATE tokens SET expires_at = ? WHERE id = ? AND kind = ? AND (expires_at IS NULL OR expires_at != ?)", revokedExpiry, id, tokenKindInvite, revokedExpiry)
	if err != nil {
		return false, fmt.Errorf("datastore: revoke invite: %w", err)
	}
	count, err := result.RowsAffected()
	return count > 0, err
}
