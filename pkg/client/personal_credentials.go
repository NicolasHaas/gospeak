package client

import (
	"context"
	"fmt"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
)

// PendingPersonalCredential is private, account- and attempt-bound recovery
// state, not a server acknowledgement. Candidate is fixed until reconciled.
type PendingPersonalCredential struct {
	ControlAddr    string `yaml:"control_addr"`
	Username       string `yaml:"username"`
	AccountID      int64  `yaml:"account_id"`
	ServerIdentity string `yaml:"server_identity"`
	Attempt        string `yaml:"attempt"`
	OldToken       string `yaml:"old_token"`
	Candidate      string `yaml:"candidate"`
	Version        int64  `yaml:"version"`
	RecoveryGrant  string `yaml:"recovery_grant,omitempty"`
}

func (p PendingPersonalCredential) valid() bool {
	return p.ControlAddr != "" && p.Username != "" && p.AccountID > 0 && p.ServerIdentity != "" && p.Version >= 0 &&
		crypto.IsGeneratedToken(p.Attempt) && crypto.IsGeneratedToken(p.Candidate) && p.Candidate != p.OldToken && p.Candidate != p.Attempt && p.Candidate != p.RecoveryGrant &&
		(p.OldToken != "" || p.RecoveryGrant != "") && (p.RecoveryGrant == "" || crypto.IsGeneratedToken(p.RecoveryGrant))
}

// credentialDiskCopy leaves live UI state untouched even if Load fails. The
// existing store path preserves an explicit decision not to migrate legacy data.
func (bs *BookmarkStore) credentialDiskCopy() (*BookmarkStore, error) {
	if bs.loadErr != nil {
		return nil, fmt.Errorf("bookmarks were not loaded; refusing credential change")
	}
	next := &BookmarkStore{path: bs.path, legacyPath: bs.legacyPath}
	if err := next.Load(); err != nil {
		return nil, err
	}
	return next, nil
}

// PersonalCredentialAttempts reloads ambiguous saves without publishing stale
// bookmark memory. Callers retain the same candidate until verified promotion.
func (bs *BookmarkStore) PersonalCredentialAttempts() ([]PendingPersonalCredential, error) {
	disk, err := bs.credentialDiskCopy()
	if err != nil {
		return nil, err
	}
	for _, p := range disk.PendingCredentials {
		if !p.valid() {
			return nil, fmt.Errorf("invalid saved credential attempt")
		}
	}
	return append([]PendingPersonalCredential(nil), disk.PendingCredentials...), nil
}

// ReloadPersonalCredentialState resolves an ambiguous private Save from disk
// without destroying live memory if the read itself fails.
func (bs *BookmarkStore) ReloadPersonalCredentialState() error {
	disk, err := bs.credentialDiskCopy()
	if err != nil {
		return err
	}
	*bs = *disk
	return nil
}

// PendingPersonalCredential reads the disk-authoritative record after a restart
// or an ambiguous Save. It does not generate a new candidate or change a token.
func (bs *BookmarkStore) PendingPersonalCredential(addr, username string, accountID int64) (PendingPersonalCredential, bool, error) {
	disk, err := bs.credentialDiskCopy()
	if err != nil {
		return PendingPersonalCredential{}, false, err
	}
	for _, p := range disk.PendingCredentials {
		if p.ControlAddr == addr && p.Username == username {
			if !p.valid() || p.AccountID != accountID {
				return PendingPersonalCredential{}, false, fmt.Errorf("pending credential account mismatch")
			}
			return p, true, nil
		}
	}
	return PendingPersonalCredential{}, false, nil
}

// StagePersonalCredential must succeed before a server mutation is sent. The
// caller supplies the identity of the verified TLS peer, not an unverified pin.
// Any Save error (including after rename) aborts sending. Retry/reload the same
// record; never replace an uncertain candidate. This store remains single-owner.
func (bs *BookmarkStore) StagePersonalCredential(p PendingPersonalCredential, consentUnsaved bool) error {
	return bs.stagePersonalCredential(p, consentUnsaved, (*BookmarkStore).Save)
}

func (bs *BookmarkStore) stagePersonalCredential(p PendingPersonalCredential, consentUnsaved bool, persist func(*BookmarkStore) error) error {
	return bs.savePersonalCredential(p, consentUnsaved, nil, persist)
}

// RecoverPendingPersonalCredential explicitly replaces only the operation, never
// the retained candidate. The caller verifies the bound server before staging.
func (bs *BookmarkStore) RecoverPendingPersonalCredential(p PendingPersonalCredential, grant, serverIdentity string) (PendingPersonalCredential, error) {
	q := p
	if !p.valid() || serverIdentity != p.ServerIdentity || !crypto.IsGeneratedToken(grant) || grant == p.RecoveryGrant {
		return q, fmt.Errorf("fresh recovery grant for the bound server required")
	}
	var err error
	q.Attempt, err = crypto.GenerateToken()
	if err != nil {
		return q, err
	}
	q.RecoveryGrant = grant
	return q, bs.savePersonalCredential(q, false, &p, (*BookmarkStore).Save)
}

// RetrustPendingPersonalCredential requires explicit out-of-band fingerprint
// confirmation. Trust and the exact pending attempt move together in one private
// save; no credential is tried, promoted, discarded or regenerated here.
func (bs *BookmarkStore) RetrustPendingPersonalCredential(p PendingPersonalCredential, identity string) error {
	return bs.retrustPendingPersonalCredential(p, identity, (*BookmarkStore).Save)
}

func (bs *BookmarkStore) retrustPendingPersonalCredential(p PendingPersonalCredential, identity string, persist func(*BookmarkStore) error) error {
	if !p.valid() || identity == "" || identity == p.ServerIdentity {
		return fmt.Errorf("changed server identity required")
	}
	next, err := bs.credentialDiskCopy()
	if err != nil {
		return err
	}
	for i, pending := range next.PendingCredentials {
		if pending.ControlAddr != p.ControlAddr || pending.Username != p.Username {
			continue
		}
		if pending != p {
			return fmt.Errorf("stale pending trust confirmation")
		}
		p.ServerIdentity = identity
		p.Attempt, err = crypto.GenerateToken()
		if err != nil {
			return err
		}
		next.PendingCredentials[i] = p
		next.TrustServer(p.ControlAddr, identity)
		if err := persist(next); err != nil {
			return err
		}
		*bs = *next
		return nil
	}
	return fmt.Errorf("credential attempt is no longer pending")
}

func (bs *BookmarkStore) savePersonalCredential(p PendingPersonalCredential, consentUnsaved bool, expected *PendingPersonalCredential, persist func(*BookmarkStore) error) error {
	if !p.valid() {
		return fmt.Errorf("invalid pending credential")
	}
	next, err := bs.credentialDiskCopy()
	if err != nil {
		return err
	}
	if pin := next.PinForAddr(p.ControlAddr); pin != "" && pin != p.ServerIdentity {
		return fmt.Errorf("pending credential server identity mismatch")
	}
	for i, pending := range next.PendingCredentials {
		if pending.ControlAddr == p.ControlAddr && pending.Username == p.Username {
			if expected != nil {
				unchanged := p
				unchanged.Attempt, unchanged.RecoveryGrant = expected.Attempt, expected.RecoveryGrant
				if pending != *expected || unchanged != *expected || p.Attempt == expected.Attempt {
					return fmt.Errorf("stale or mismatched recovery transition")
				}
				next.PendingCredentials[i] = p
			} else if pending != p {
				return fmt.Errorf("credential replacement already pending; reconcile the saved candidate")
			}
			if err := persist(next); err != nil {
				return err
			}
			*bs = *next
			return nil
		}
	}
	if expected != nil {
		return fmt.Errorf("credential attempt is no longer pending")
	}
	saved := false
	for _, bookmark := range next.Bookmarks {
		if bookmark.ControlAddr == p.ControlAddr && bookmark.Username == p.Username {
			if bookmark.Token != p.OldToken {
				return fmt.Errorf("saved credential changed before staging")
			}
			saved = true
		}
	}
	if !saved {
		if !consentUnsaved {
			return fmt.Errorf("private staging of an unsaved account requires consent")
		}
		next.Add(Bookmark{Name: p.ControlAddr, ControlAddr: p.ControlAddr, Username: p.Username, Token: p.OldToken})
	}
	next.PendingCredentials = append(next.PendingCredentials, p)
	if err := persist(next); err != nil {
		return err
	}
	*bs = *next
	return nil
}

// ReconcilePersonalCredential tries only the disk-staged candidate, pinned to
// the original verified TLS identity. Transport, trust, capacity and credential
// rejection all leave pending state intact. No fallback, regeneration or rollback
// is performed here. An older server without an account ID cannot prove binding.
func (bs *BookmarkStore) ReconcilePersonalCredential(ctx context.Context, p PendingPersonalCredential) error {
	saved, found, err := bs.PendingPersonalCredential(p.ControlAddr, p.Username, p.AccountID)
	if err != nil {
		return err
	}
	if !found || saved != p {
		return fmt.Errorf("credential attempt is not current")
	}
	if err := VerifyPersonalCredential(ctx, p); err != nil {
		return err
	}
	return bs.PromotePersonalCredential(p)
}

// PromotePersonalCredential may be called only after the exact candidate has
// authenticated the bound account over the bound verified server identity.
// A response acknowledgement alone is not proof of reconciliation.
func (bs *BookmarkStore) PromotePersonalCredential(p PendingPersonalCredential) error {
	return bs.promotePersonalCredential(p, (*BookmarkStore).Save)
}

func (bs *BookmarkStore) promotePersonalCredential(p PendingPersonalCredential, persist func(*BookmarkStore) error) error {
	return bs.finishPersonalCredential(p, p.Candidate, persist)
}

// PromoteRecoveredPersonalCredential requires explicit successful verification of
// token against p's account and server. The saved attempt still supplies the CAS.
func (bs *BookmarkStore) PromoteRecoveredPersonalCredential(p PendingPersonalCredential, token string) error {
	if token == "" {
		return fmt.Errorf("verified personal credential required")
	}
	return bs.finishPersonalCredential(p, token, (*BookmarkStore).Save)
}

func (bs *BookmarkStore) finishPersonalCredential(p PendingPersonalCredential, token string, persist func(*BookmarkStore) error) error {
	if !p.valid() {
		return fmt.Errorf("invalid pending credential")
	}
	next, err := bs.credentialDiskCopy()
	if err != nil {
		return err
	}
	if pin := next.PinForAddr(p.ControlAddr); pin != "" && pin != p.ServerIdentity {
		return fmt.Errorf("pending credential server identity mismatch")
	}
	index := -1
	for i, pending := range next.PendingCredentials {
		if pending.ControlAddr == p.ControlAddr && pending.Username == p.Username {
			if pending != p {
				return fmt.Errorf("stale credential completion")
			}
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("credential attempt is not pending")
	}
	found := false
	for i := range next.Bookmarks {
		b := &next.Bookmarks[i]
		if b.ControlAddr == p.ControlAddr && b.Username == p.Username {
			if b.Token != p.OldToken {
				return fmt.Errorf("saved credential changed before promotion")
			}
			b.Token = token
			found = true
		}
	}
	if !found {
		return fmt.Errorf("credential bookmark no longer exists")
	}
	next.PendingCredentials = append(next.PendingCredentials[:index], next.PendingCredentials[index+1:]...)
	if err := persist(next); err != nil {
		return err
	}
	*bs = *next
	return nil
}
