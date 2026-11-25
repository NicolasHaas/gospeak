package ui

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/NicolasHaas/gospeak/pkg/client"
)

func TestPendingCredentialTrustConfirmationGuards(t *testing.T) {
	for _, mode := range []string{"decline", "stale-generation", "stale-record", "screen", "wrong-address"} {
		t.Run(mode, func(t *testing.T) {
			isolateAudioSettingsConfig(t)
			f := test.NewApp()
			defer f.Quit()
			srv := httptest.NewTLSServer(nil)
			defer srv.Close()
			p := client.PendingPersonalCredential{ControlAddr: srv.Listener.Addr().String(), Username: "person", AccountID: 7, ServerIdentity: "old-pin", Attempt: strings.Repeat("a", 64), Candidate: strings.Repeat("b", 64), OldToken: "old-token", Version: 3}
			bs := client.NewBookmarkStore()
			if err := bs.StagePersonalCredential(p, true); err != nil {
				t.Fatal(err)
			}
			var trustErr error
			if mode == "screen" {
				_, trustErr = client.NewScreenClientContext(context.Background(), p.ControlAddr, 1, "synthetic", p.ServerIdentity)
			} else {
				_, trustErr = client.NewControlClientContext(context.Background(), p.ControlAddr, p.ServerIdentity)
			}
			if trustErr == nil {
				t.Fatal("expected real TLS mismatch")
			}
			a := &App{fyneApp: f, window: f.NewWindow("pending trust"), bookmarks: bs}
			defer a.window.Close()
			supplied := p
			if mode == "wrong-address" {
				supplied.ControlAddr = "other.test:9600"
			}
			handled := a.confirmPendingCredentialTrust(fmt.Errorf("wrapped: %w", trustErr), supplied, widget.NewLabel(""))
			if mode == "screen" || mode == "wrong-address" {
				if handled || len(a.window.Canvas().Overlays().List()) != 0 {
					t.Fatal("foreign trust failure offered pending control trust")
				}
			} else {
				if !handled {
					t.Fatal("control mismatch did not offer confirmation")
				}
				buttons, _ := credentialDialogControls(a.window)
				switch mode {
				case "decline":
					test.Tap(buttons["Cancel"])
				case "stale-generation":
					a.connectAttempt++
					test.Tap(buttons["Re-trust pending server"])
				case "stale-record":
					q, err := bs.RecoverPendingPersonalCredential(p, strings.Repeat("c", 64), p.ServerIdentity)
					if err != nil {
						t.Fatal(err)
					}
					p = q
					test.Tap(buttons["Re-trust pending server"])
				}
			}
			saved, found, err := bs.PendingPersonalCredential(p.ControlAddr, p.Username, p.AccountID)
			if err != nil || !found || saved != p || bs.PinForAddr(p.ControlAddr) == client.SPKIFingerprint(srv.Certificate()) {
				t.Fatal("declined/foreign/stale confirmation changed bound pending state or trust")
			}
		})
	}
}
