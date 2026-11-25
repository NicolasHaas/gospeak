package client

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersonalCredentialPendingRecoveryGuardsAndPersistence(t *testing.T) {
	for _, afterSave := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-save", true: "ambiguous-save"}[afterSave], func(t *testing.T) {
			bs := &BookmarkStore{path: filepath.Join(t.TempDir(), "servers.yaml")}
			p := pendingCredentialFixture()
			if err := bs.StagePersonalCredential(p, true); err != nil {
				t.Fatal(err)
			}
			for _, wrong := range []PendingPersonalCredential{
				func() PendingPersonalCredential { q := p; q.AccountID++; return q }(),
				func() PendingPersonalCredential { q := p; q.ControlAddr = "other:9600"; return q }(),
				func() PendingPersonalCredential { q := p; q.ServerIdentity = "other"; return q }(),
				func() PendingPersonalCredential { q := p; q.Attempt = strings.Repeat("d", 64); return q }(),
			} {
				if _, err := bs.RecoverPendingPersonalCredential(wrong, strings.Repeat("c", 64), wrong.ServerIdentity); err == nil {
					t.Fatal("mismatched recovery staged")
				}
			}
			if _, err := bs.RecoverPendingPersonalCredential(p, strings.Repeat("c", 64), "wrong-peer"); err == nil {
				t.Fatal("wrong peer staged")
			}
			q := p
			q.Attempt = strings.Repeat("d", 64)
			q.RecoveryGrant = strings.Repeat("c", 64)
			persist := func(next *BookmarkStore) error {
				if afterSave {
					if err := next.Save(); err != nil {
						return err
					}
				}
				return errors.New("injected private save failure")
			}
			if err := bs.savePersonalCredential(q, false, &p, persist); err == nil {
				t.Fatal("failed recovery stage succeeded")
			}
			if bs.PendingCredentials[0] != p {
				t.Fatal("failed save published memory")
			}
			saved, found, err := bs.PendingPersonalCredential(p.ControlAddr, p.Username, p.AccountID)
			expected := p
			if afterSave {
				expected = q
			}
			if err != nil || !found || saved != expected {
				t.Fatal("ambiguous save lost exact candidate")
			}
			if err := bs.StagePersonalCredential(saved, false); err != nil {
				t.Fatal(err)
			}
			if afterSave {
				if err := bs.PromotePersonalCredential(p); err == nil {
					t.Fatal("stale callback promoted")
				}
				if _, err := bs.RecoverPendingPersonalCredential(p, strings.Repeat("e", 64), p.ServerIdentity); err == nil {
					t.Fatal("stale replacement won")
				}
			}
			// Even verified local recovery must not publish memory on a failed final save.
			if err := bs.finishPersonalCredential(saved, "local-recovered", persist); err == nil {
				t.Fatal("failed finalization succeeded")
			}
			if len(bs.PendingCredentials) != 1 || bs.Bookmarks[0].Token != p.OldToken {
				t.Fatal("failed finalization published memory")
			}
		})
	}
}
