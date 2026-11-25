package client

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func pendingCredentialFixture() PendingPersonalCredential {
	return PendingPersonalCredential{ControlAddr: "server.test:9600", Username: "person", AccountID: 7, ServerIdentity: "SHA256:fixture", Attempt: strings.Repeat("a", 64), OldToken: "historical-personal", Candidate: strings.Repeat("b", 64), Version: 3}
}

func TestPersonalCredentialStageRejectsReusedRecoveryOrAttemptSecret(t *testing.T) {
	for _, field := range []string{"grant", "attempt", "old"} {
		p := pendingCredentialFixture()
		switch field {
		case "grant":
			p.RecoveryGrant = p.Candidate
		case "attempt":
			p.Attempt = p.Candidate
		case "old":
			p.OldToken = p.Candidate
		}
		bs := &BookmarkStore{path: filepath.Join(t.TempDir(), "servers.yaml")}
		if err := bs.StagePersonalCredential(p, true); err == nil {
			t.Fatal("replacement reused a disclosed or existing credential")
		}
		if _, err := os.Stat(bs.path); !os.IsNotExist(err) {
			t.Fatal("invalid credential wrote pending state")
		}
	}
}

func TestPersonalCredentialStageFailureDoesNotPublishMemory(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	bs := &BookmarkStore{path: filepath.Join(blocker, "servers.yaml")}
	if err := bs.StagePersonalCredential(pendingCredentialFixture(), true); err == nil {
		t.Fatal("invalid path accepted")
	}
	if len(bs.PendingCredentials) != 0 || len(bs.Bookmarks) != 0 {
		t.Fatal("failed private stage published in-memory credential state")
	}
}

func TestPersonalCredentialUnsavedRequiresConsent(t *testing.T) {
	bs := &BookmarkStore{path: filepath.Join(t.TempDir(), "servers.yaml")}
	if err := bs.StagePersonalCredential(pendingCredentialFixture(), false); err == nil {
		t.Fatal("unsaved account staged without consent")
	}
	if _, err := os.Stat(bs.path); !os.IsNotExist(err) {
		t.Fatal("declined staging wrote a file")
	}
	if !reflect.DeepEqual(bs.PendingCredentials, []PendingPersonalCredential(nil)) {
		t.Fatal("declined staging changed memory")
	}
}

func TestPersonalCredentialCrashPendingAndStaleCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.yaml")
	bs := &BookmarkStore{path: path}
	p := pendingCredentialFixture()
	if err := bs.StagePersonalCredential(p, true); err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, path)
	// A new process reads exactly the staged candidate, not a newly generated one.
	restarted := &BookmarkStore{path: path}
	saved, found, err := restarted.PendingPersonalCredential(p.ControlAddr, p.Username, p.AccountID)
	if err != nil || !found || saved != p {
		t.Fatal("pending candidate did not survive restart")
	}
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	if restarted.Bookmarks[0].Token != p.OldToken {
		t.Fatal("staging prematurely promoted candidate")
	}
	// Another account/address cannot consume the record.
	for _, changed := range []PendingPersonalCredential{
		func() PendingPersonalCredential { q := p; q.AccountID++; return q }(),
		func() PendingPersonalCredential { q := p; q.ControlAddr = "other.test:9600"; return q }(),
		func() PendingPersonalCredential { q := p; q.Attempt = strings.Repeat("c", 64); return q }(),
		func() PendingPersonalCredential { q := p; q.ServerIdentity = "SHA256:other"; return q }(),
	} {
		if err := restarted.PromotePersonalCredential(changed); err == nil {
			t.Fatal("mismatched completion promoted credential")
		}
	}
	if err := restarted.PromotePersonalCredential(saved); err != nil {
		t.Fatal(err)
	}
	if restarted.Bookmarks[0].Token != p.Candidate || len(restarted.PendingCredentials) != 0 {
		t.Fatal("candidate promotion failed")
	}
	newer := p
	newer.OldToken = p.Candidate
	newer.Candidate = strings.Repeat("d", 64)
	newer.Attempt = strings.Repeat("e", 64)
	newer.Version++
	if err := restarted.StagePersonalCredential(newer, false); err != nil {
		t.Fatal(err)
	}
	if err := bs.PromotePersonalCredential(p); err == nil {
		t.Fatal("old callback overwrote newer disk operation")
	}
	got, found, err := bs.PendingPersonalCredential(p.ControlAddr, p.Username, p.AccountID)
	if err != nil || !found || got != newer {
		t.Fatal("stale callback damaged pending state")
	}
}

func TestPersonalCredentialPersistenceFailureAndPostRenameAmbiguity(t *testing.T) {
	for _, afterSave := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-write", true: "after-publication"}[afterSave], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "servers.yaml")
			bs := &BookmarkStore{path: path}
			p := pendingCredentialFixture()
			// Same production staging path, injected failure at the persistence boundary.
			persist := func(next *BookmarkStore) error {
				if afterSave {
					if err := next.Save(); err != nil {
						return err
					}
				}
				return errors.New("injected persistence failure")
			}
			if err := bs.stagePersonalCredential(p, true, persist); err == nil {
				t.Fatal("failed stage reported success")
			}
			if len(bs.Bookmarks) != 0 || len(bs.PendingCredentials) != 0 {
				t.Fatal("failed stage published memory")
			}
			saved, found, err := bs.PendingPersonalCredential(p.ControlAddr, p.Username, p.AccountID)
			if err != nil || found != afterSave || (found && saved != p) {
				t.Fatal("disk-authoritative ambiguous state lost")
			}
			if afterSave {
				other := p
				other.Candidate = strings.Repeat("c", 64)
				if err := bs.StagePersonalCredential(other, true); err == nil {
					t.Fatal("ambiguous stage silently replaced candidate")
				}
			}
			// Re-persisting exactly the same record can establish a successful stage.
			if err := bs.StagePersonalCredential(p, true); err != nil {
				t.Fatal(err)
			}
			if err := bs.promotePersonalCredential(p, persist); err == nil {
				t.Fatal("failed final save reported success")
			}
			if bs.Bookmarks[0].Token != p.OldToken || len(bs.PendingCredentials) != 1 {
				t.Fatal("failed final save published memory")
			}
			restarted := &BookmarkStore{path: path}
			if err := restarted.Load(); err != nil {
				t.Fatal(err)
			}
			if afterSave {
				if restarted.Bookmarks[0].Token != p.Candidate {
					t.Fatal("post-publication final save lost candidate")
				}
			} else {
				if len(restarted.PendingCredentials) != 1 || restarted.PendingCredentials[0] != p {
					t.Fatal("final save failure lost recoverable candidate")
				}
			}
		})
	}
}

func TestPersonalCredentialStagePreservesLegacyAndTrust(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy", "servers.yaml")
	current := filepath.Join(root, "current", "servers.yaml")
	p := pendingCredentialFixture()
	old := &BookmarkStore{path: legacy}
	old.Add(Bookmark{ControlAddr: p.ControlAddr, Username: p.Username, Token: p.OldToken})
	old.TrustServer(p.ControlAddr, p.ServerIdentity)
	if err := old.Save(); err != nil {
		t.Fatal(err)
	}
	bs := &BookmarkStore{path: current, legacyPath: legacy}
	if err := bs.Load(); err != nil {
		t.Fatal(err)
	}
	if err := bs.StagePersonalCredential(p, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(current); !os.IsNotExist(err) {
		t.Fatal("staging migrated legacy config")
	}
	if bs.PinForAddr(p.ControlAddr) != p.ServerIdentity {
		t.Fatal("staging changed trust")
	}
	q := p
	q.ServerIdentity = "wrong-identity"
	if err := bs.StagePersonalCredential(q, false); err == nil {
		t.Fatal("staging changed verified identity")
	}
}
