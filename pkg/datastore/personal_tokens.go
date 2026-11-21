package datastore

import (
	"context"
	"fmt"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

const PersonalRecoveryLifetime = 15 * time.Minute

func (s *txProvider) recoveryTargetAllowed(userID int64) (bool, error) {
	user, err := s.GetUserByID(userID)
	if err != nil {
		return false, err
	}
	if user == nil || (user.Role != model.RoleUser && user.Role != model.RoleModerator) {
		return false, nil
	}
	owner, err := s.IsBootstrapUser(userID)
	if err != nil || owner {
		return false, err
	}
	banned, err := s.IsUserBanned(userID)
	return !banned && err == nil, err
}

// Issuing a grant also revokes the old personal credential. The caller retires
// sessions under the publication fence after committing this writer transaction.
func (s *txProvider) IssuePersonalRecovery(userID, version int64, grantHash string, now time.Time, issuedBy int64) (bool, error) {
	if userID <= 0 || version < 0 || grantHash == "" || now.IsZero() || issuedBy <= 0 {
		return false, nil
	}
	issuer, err := s.GetUserByID(issuedBy)
	if err != nil || issuer == nil || issuer.Role != model.RoleAdmin || issuer.PersonalTokenHash == "" {
		return false, err
	}
	banned, err := s.IsUserBanned(issuedBy)
	if err != nil || banned {
		return false, err
	}
	allowed, err := s.recoveryTargetAllowed(userID)
	if err != nil || !allowed {
		return false, err
	}
	result, err := s.ExecContext(context.Background(), `UPDATE users
  SET personal_token_hash = '', personal_token_created_at = ?, credential_version = credential_version + 1,
      recovery_hash = ?, recovery_expires_at = ?, recovery_issued_by = ?
  WHERE id = ? AND credential_version = ?`, formatDBTime(now), grantHash, now.Add(PersonalRecoveryLifetime).Unix(), issuedBy, userID, version)
	if err != nil {
		return false, fmt.Errorf("datastore: issue personal recovery: %w", err)
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

// Redemption updates the existing identity, consumes the grant and invalidates
// stale administrative requests in one statement. A failed match consumes nothing.
func (s *txProvider) RedeemPersonalRecovery(userID int64, grantHash, replacementHash string, now time.Time) (bool, error) {
	if userID <= 0 || grantHash == "" || replacementHash == "" || grantHash == replacementHash || now.IsZero() {
		return false, nil
	}
	allowed, err := s.recoveryTargetAllowed(userID)
	if err != nil || !allowed {
		return false, err
	}
	result, err := s.ExecContext(context.Background(), `UPDATE users
  SET personal_token_hash = ?, personal_token_created_at = ?, credential_version = credential_version + 1,
      recovery_hash = '', recovery_expires_at = 0, recovery_issued_by = 0
  WHERE id = ? AND recovery_hash = ? AND recovery_expires_at > ?
		AND EXISTS (SELECT 1 FROM users AS issuer WHERE issuer.id = users.recovery_issued_by
		  AND issuer.role = ? AND issuer.personal_token_hash <> ''
		  AND NOT EXISTS (SELECT 1 FROM bans WHERE bans.user_id = issuer.id
		    AND (bans.expires_at IS NULL OR bans.expires_at > ?)))
		AND NOT EXISTS (SELECT 1 FROM users AS other WHERE other.id <> ? AND other.personal_token_hash = ?)`, replacementHash, formatDBTime(now), userID, grantHash, now.Unix(), model.RoleAdmin, formatDBTime(now), userID, replacementHash)
	if err != nil {
		return false, fmt.Errorf("datastore: redeem personal recovery: %w", err)
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *baseProvider) PersonalTokenVersion(userID int64) (int64, error) {
	var version int64
	if err := s.QueryRowContext(context.Background(), "SELECT credential_version FROM users WHERE id = ?", userID).Scan(&version); err != nil {
		return 0, fmt.Errorf("datastore: read credential version: %w", err)
	}
	return version, nil
}

// ReplacePersonalToken invalidates outstanding grants as part of the same CAS.
// Empty replacement revokes; an empty current hash never authenticates.
func (s *txProvider) ReplacePersonalToken(userID, version int64, expectedHash, replacementHash string, createdAt time.Time) (bool, error) {
	if userID <= 0 || version < 0 || (expectedHash == replacementHash && replacementHash != "") {
		return false, nil
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	result, err := s.ExecContext(context.Background(), `UPDATE users
  SET personal_token_hash = ?, personal_token_created_at = ?, credential_version = credential_version + 1,
      recovery_hash = '', recovery_expires_at = 0, recovery_issued_by = 0
  WHERE id = ? AND credential_version = ? AND personal_token_hash = ?
		AND (? = '' OR NOT EXISTS (SELECT 1 FROM users AS other WHERE other.id <> ? AND other.personal_token_hash = ?))`,
		replacementHash, formatDBTime(createdAt), userID, version, expectedHash, replacementHash, userID, replacementHash)
	if err != nil {
		return false, fmt.Errorf("datastore: replace personal token: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("datastore: inspect personal token replacement: %w", err)
	}
	return rows == 1, nil
}

// RevokePersonalRecovery never revokes a subsequently recovered personal token.
func (s *txProvider) RevokePersonalRecovery(userID, version int64) (bool, error) {
	result, err := s.ExecContext(context.Background(), `UPDATE users
	 SET recovery_hash = '', recovery_expires_at = 0, recovery_issued_by = 0,
	 credential_version = credential_version + 1 WHERE id = ? AND credential_version = ?`, userID, version)
	if err != nil {
		return false, fmt.Errorf("datastore: revoke personal recovery: %w", err)
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}
