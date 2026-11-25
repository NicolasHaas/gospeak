package client

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestPendingCredentialRetrustBindingsAndAtomicSave(t *testing.T) {
	for _, mode := range []string{"success", "before-save", "after-save"} {
		t.Run(mode, func(t *testing.T) {
			bs := &BookmarkStore{path: filepath.Join(t.TempDir(), "servers.yaml")}
			p := pendingCredentialFixture()
			if err := bs.StagePersonalCredential(p, true); err != nil {
				t.Fatal(err)
			}
			other := p
			other.Username, other.AccountID = "other", 8
			if err := bs.StagePersonalCredential(other, true); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"account", "address", "attempt", "identity", "candidate"} {
				wrong := p
				switch field {
				case "account":
					wrong.AccountID++
				case "address":
					wrong.ControlAddr = "other.test:9600"
				case "attempt":
					wrong.Attempt = strings.Repeat("c", 64)
				case "identity":
					wrong.ServerIdentity = "wrong"
				case "candidate":
					wrong.Candidate = strings.Repeat("d", 64)
				}
				if err := bs.RetrustPendingPersonalCredential(wrong, "new-pin"); err == nil {
					t.Fatal("mismatched trust confirmation accepted")
				}
			}
			persist := func(next *BookmarkStore) error {
				if mode != "before-save" {
					if err := next.Save(); err != nil {
						return err
					}
				}
				if mode != "success" {
					return errors.New("injected private save uncertainty")
				}
				return nil
			}
			err := bs.retrustPendingPersonalCredential(p, "new-pin", persist)
			if (err == nil) != (mode == "success") {
				t.Fatal("save result not propagated")
			}
			if mode != "success" && (bs.PendingCredentials[0] != p || bs.PinForAddr(p.ControlAddr) == "new-pin") {
				t.Fatal("failed save published memory")
			}
			if err := bs.ReloadPersonalCredentialState(); err != nil {
				t.Fatal(err)
			}
			q := bs.PendingCredentials[0]
			if mode == "before-save" {
				if q != p || bs.PinForAddr(p.ControlAddr) == "new-pin" {
					t.Fatal("failed pre-save changed disk")
				}
			} else {
				if q.ServerIdentity != "new-pin" || q.Attempt == p.Attempt || bs.PinForAddr(p.ControlAddr) != "new-pin" {
					t.Fatal("trust and attempt not saved together")
				}
				unchanged := q
				unchanged.ServerIdentity, unchanged.Attempt = p.ServerIdentity, p.Attempt
				if unchanged != p || bs.PendingCredentials[1] != other || bs.Bookmarks[0].Token != p.OldToken {
					t.Fatal("retrust changed candidate/account/other pending state")
				}
				if err := bs.PromotePersonalCredential(p); err == nil {
					t.Fatal("pre-trust completion promoted")
				}
				if err := bs.RetrustPendingPersonalCredential(p, "third-pin"); err == nil {
					t.Fatal("stale confirmation changed trust")
				}
				if err := bs.StagePersonalCredential(q, false); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
