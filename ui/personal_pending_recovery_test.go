package ui

import (
	"crypto/tls"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestPersonalCredentialPendingRecoveryDialog(t *testing.T) {
	for _, mode := range []string{"fresh-grant", "already-committed", "wrong-grant", "save-failure", "stale-completion", "unknown-auth", "unknown-network", "wrong-server", "wrong-account", "local-owner", "local-admin", "local-wrong-account", "local-stale", "retrust-check", "retrust-grant", "retrust-local"} {
		t.Run(mode, func(t *testing.T) {
			isolateAudioSettingsConfig(t)
			f := test.NewApp()
			defer f.Quit()
			completions := make(chan func(), 32)
			fyne.SetCurrentApp(&audioSettingsApp{App: f, driver: &audioSettingsDriver{Driver: f.Driver(), completion: completions}})
			defer fyne.SetCurrentApp(f)
			cert := httptest.NewTLSServer(nil)
			defer cert.Close()
			ln, err := tls.Listen("tcp", "127.0.0.1:0", cert.TLS)
			if err != nil {
				t.Fatal(err)
			}
			addr, pin := ln.Addr().String(), client.SPKIFingerprint(cert.Certificate())
			p := client.PendingPersonalCredential{ControlAddr: addr, ServerIdentity: pin, Username: "person", AccountID: 7, OldToken: "revoked-old", Candidate: strings.Repeat("b", 64), Attempt: strings.Repeat("a", 64), Version: 3}
			if mode == "wrong-server" || strings.HasPrefix(mode, "retrust-") {
				p.ServerIdentity = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
			}
			bs := client.NewBookmarkStore()
			if err := bs.Save(); err != nil {
				t.Fatal(err)
			}
			if err := bs.StagePersonalCredential(p, true); err != nil {
				t.Fatal(err)
			}
			mutated := make(chan bool, 1)
			stopped := make(chan struct{})
			initial := p // immutable fixture state owned by the endpoint worker
			go func() {
				defer close(stopped)
				current := "neither-old-nor-candidate"
				if mode == "already-committed" || mode == "retrust-check" {
					current = initial.Candidate
				}
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					func() {
						defer conn.Close()
						_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
						m, err := protocol.ReadControlMessage(conn)
						if err != nil {
							return
						}
						if m.AuthRequest != nil {
							if mode == "unknown-network" {
								return
							}
							if mode == "unknown-auth" {
								_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{ErrorResponse: &pb.ErrorResponse{Code: 4, Message: "temporary failure"}})
								return
							}
							local := (strings.HasPrefix(mode, "local-") || mode == "retrust-local") && m.AuthRequest.Token == "operator-recovered"
							if m.AuthRequest.Token != current && !local {
								_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{ErrorResponse: &pb.ErrorResponse{Code: 2, Message: "authentication failed"}})
								return
							}
							id := int64(7)
							if mode == "local-wrong-account" {
								id = 8
							}
							_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{AuthResponse: &pb.AuthResponse{SessionID: 1, UserID: id, Username: "person", Role: "admin", CredentialVersion: 5, MediaCipher: "aes128", EncryptionKey: make([]byte, 16), VoiceRegistrationKey: make([]byte, 32)}})
							return
						}
						if m.RedeemPersonalRecoveryReq != nil {
							if mode == "wrong-grant" {
								_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{RedeemPersonalRecoveryResp: &pb.RedeemPersonalRecoveryResponse{Error: "recovery unavailable"}})
								return
							}
							disk := client.NewBookmarkStore()
							err := disk.Load()
							r := m.RedeemPersonalRecoveryReq
							staged := err == nil && len(disk.PendingCredentials) == 1 && disk.PendingCredentials[0].Candidate == initial.Candidate && disk.PendingCredentials[0].Attempt != initial.Attempt && disk.PendingCredentials[0].RecoveryGrant == r.Grant && r.Replacement == initial.Candidate && r.TargetID == 7
							mutated <- staged
							current = r.Replacement
							// Lost ACK: production UI authenticates the retained candidate afterwards.
						}
					}()
				}
			}()
			defer func() { _ = ln.Close(); <-stopped }()
			a := &App{fyneApp: f, window: f.NewWindow("Recovery"), engine: client.NewEngine(), bookmarks: bs}
			defer a.window.Close()
			a.showPersonalCredentialDialog()
			buttons, entries := credentialDialogControls(a.window)
			if len(entries) != 5 {
				t.Fatalf("entries=%d", len(entries))
			}
			button := "Redeem recovery grant"
			entries[4].SetText(strings.Repeat("c", 64))
			if mode == "wrong-account" {
				entries[3].SetText("8")
			}
			if strings.HasPrefix(mode, "local-") || mode == "retrust-local" {
				entries[2].SetText("operator-recovered")
				button = "Verify operator-recovered credential"
			}
			if mode == "retrust-check" {
				button = "Check saved candidate"
			}
			test.Tap(buttons[button])
			if mode == "wrong-account" {
				if a.credentialBusy {
					t.Fatal("wrong account started recovery")
				}
				return
			}
			if !a.credentialBusy {
				t.Fatal("pending attempt blocked explicit recovery before verification")
			}
			complete := func() {
				t.Helper()
				select {
				case fn := <-completions:
					fn()
				case <-time.After(4 * time.Second):
					t.Fatal("owned credential request did not complete")
				}
			}
			if mode == "save-failure" {
				if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "gospeak", "servers.yaml"), []byte("bookmarks: ["), 0600); err != nil { // #nosec G703 -- Deliberately corrupt the test-owned config under t.TempDir.
					t.Fatal(err)
				}
			}
			if mode == "stale-completion" || mode == "local-stale" {
				q, err := bs.RecoverPendingPersonalCredential(p, strings.Repeat("d", 64), p.ServerIdentity)
				if err != nil {
					t.Fatal(err)
				}
				p = q
			}
			complete() // preflight/local verification; recovery stage happens on UI owner
			if strings.HasPrefix(mode, "retrust-") {
				trustButtons, _ := credentialDialogControls(a.window)
				confirm := trustButtons["Re-trust pending server"]
				if confirm == nil {
					t.Fatal("TLS key change has no explicit pending-credential fingerprint confirmation")
				}
				test.Tap(confirm)
				q, found, err := bs.PendingPersonalCredential(p.ControlAddr, p.Username, p.AccountID)
				if err != nil || !found || q.Candidate != p.Candidate || q.AccountID != p.AccountID || q.ServerIdentity != pin || q.Attempt == p.Attempt {
					t.Fatal("confirmed trust did not privately preserve/rebind pending attempt")
				}
				p = q
				test.Tap(buttons[button])
				complete()
				if mode == "retrust-grant" {
					select {
					case ok := <-mutated:
						if !ok {
							t.Fatal("retrusted recovery preceded private stage")
						}
					case <-time.After(4 * time.Second):
						t.Fatal("retrusted grant not redeemed")
					}
					complete()
				}
			}
			if mode == "wrong-grant" {
				p = bs.PendingCredentials[0]
				if p.Candidate != strings.Repeat("b", 64) || p.RecoveryGrant != strings.Repeat("c", 64) {
					t.Fatal("fresh grant changed the retained candidate")
				}
				complete()
			}
			if mode == "already-committed" {
				complete()
			}
			if mode == "fresh-grant" {
				select {
				case ok := <-mutated:
					if !ok {
						t.Fatal("redemption preceded exact private stage")
					}
				case <-time.After(4 * time.Second):
					t.Fatal("fresh grant not redeemed")
				}
				complete()
			}
			if a.credentialBusy {
				t.Fatal("credential worker not released")
			}
			if mode == "save-failure" {
				select {
				case <-mutated:
					t.Fatal("save failure sent mutation")
				default:
				}
				return
			}
			disk := client.NewBookmarkStore()
			if err := disk.Load(); err != nil {
				t.Fatal(err)
			}
			success := mode == "fresh-grant" || mode == "already-committed" || mode == "local-owner" || mode == "local-admin" || strings.HasPrefix(mode, "retrust-")
			if success {
				token := p.Candidate
				if strings.HasPrefix(mode, "local-") || mode == "retrust-local" {
					token = "operator-recovered"
				}
				if len(disk.PendingCredentials) != 0 || disk.Bookmarks[0].Token != token {
					t.Fatal("verified recovery did not privately reconcile")
				}
			} else {
				if len(disk.PendingCredentials) != 1 || disk.PendingCredentials[0] != p || disk.Bookmarks[0].Token != p.OldToken {
					t.Fatal("failure or stale completion changed pending state")
				}
				select {
				case <-mutated:
					t.Fatal("unverified state sent mutation")
				default:
				}
			}
		})
	}
}
