package ui

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
	"github.com/NicolasHaas/gospeak/pkg/server"
)

// The real engine uses an ALSA null device, never a microphone/speaker. These
// checks exercise production dialogs and session retirement, not audio quality.
func TestCredentialDialogsAtAccountSessionCapacity(t *testing.T) {
	for _, limit := range []int{1, 2} {
		for _, action := range []string{"rotate", "admin", "preflight-failure"} {
			t.Run(strconv.Itoa(limit)+"/"+action, func(t *testing.T) {
				admin := action == "admin"
				isolateAudioSettingsConfig(t)
				alsa := filepath.Join(t.TempDir(), "alsa.conf")
				if err := os.WriteFile(alsa, []byte("pcm.!default { type null }\n"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("ALSA_CONFIG_PATH", alsa)
				t.Setenv("JACK_NO_START_SERVER", "1")
				f := test.NewApp()
				defer f.Quit()
				completions := make(chan func(), 32)
				fyne.SetCurrentApp(&audioSettingsApp{App: f, driver: &audioSettingsDriver{Driver: f.Driver(), completion: completions}})
				defer fyne.SetCurrentApp(f)
				cfg := server.DefaultConfig()
				cfg.DataDir = t.TempDir()
				cfg.DBPath = filepath.Join(cfg.DataDir, "test.db")
				cfg.MaxSessionsPerUser = limit
				cfg.EnableScreenShare = false
				tcp, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				cfg.ControlAddr = tcp.Addr().String()
				if err := tcp.Close(); err != nil {
					t.Fatal(err)
				}
				udp, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				cfg.VoiceAddr = udp.LocalAddr().String()
				if err := udp.Close(); err != nil {
					t.Fatal(err)
				}
				st, err := datastore.NewProviderFactory(cfg.DBPath)
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				role := model.RoleUser
				if admin {
					role = model.RoleAdmin
				}
				actor, err := st.NonTx().CreateUser("actor", role)
				if err != nil {
					t.Fatal(err)
				}
				if err := st.NonTx().UpdateUserPersonalToken(actor.ID, crypto.HashToken("old-personal"), time.Now()); err != nil {
					t.Fatal(err)
				}
				// Retained ordinary invite prevents first-run setup; no production state.
				if err := st.NonTx().CreateToken("synthetic-invite", model.RoleUser, 0, actor.ID, 1, time.Time{}); err != nil {
					t.Fatal(err)
				}
				target, err := st.NonTx().CreateUser("target", model.RoleUser)
				if err != nil {
					t.Fatal(err)
				}
				if err := st.NonTx().UpdateUserPersonalToken(target.ID, crypto.HashToken("target-personal"), time.Now()); err != nil {
					t.Fatal(err)
				}
				pin, stop := startCredentialUIRealServer(t, cfg, st)
				defer stop()
				engine := client.NewEngine()
				defer engine.Disconnect()
				if err := engine.ConnectWithAutoToken(cfg.ControlAddr, cfg.VoiceAddr, "old-personal", actor.Username, pin, nil); err != nil {
					t.Fatal(err)
				}
				var other *client.ControlClient
				if limit == 2 {
					other, _, err = client.OpenPersonalTokenControl(context.Background(), cfg.ControlAddr, pin, actor.Username, "old-personal")
					if err != nil {
						t.Fatal(err)
					}
					defer other.Close()
				}
				// Reproduce the original spare-slot failure without any mutation.
				extra, _, err := client.OpenPersonalTokenControl(context.Background(), cfg.ControlAddr, pin, actor.Username, "old-personal")
				if err == nil {
					_ = extra.Close()
					t.Fatal("fixture did not fill account capacity")
				}
				if client.PersonalCredentialRejected(err) {
					t.Fatal("capacity misclassified as credential rejection")
				}
				bs := client.NewBookmarkStore()
				bs.Add(client.Bookmark{ControlAddr: cfg.ControlAddr, Username: actor.Username, Token: "old-personal"})
				bs.TrustServer(cfg.ControlAddr, pin)
				if err := bs.Save(); err != nil {
					t.Fatal(err)
				}
				a := &App{fyneApp: f, window: f.NewWindow("capacity"), engine: engine, bookmarks: bs, connectServer: cfg.ControlAddr, connectToken: "old-personal"}
				defer a.window.Close()
				button := "Rotate my credential"
				if admin {
					a.showPersonalTokenAdminDialog()
					button = "Inspect target"
				} else {
					a.showPersonalCredentialDialog()
				}
				buttons, entries := credentialDialogControls(a.window)
				if admin {
					entries[0].SetText(strconv.FormatInt(target.ID, 10))
				}
				test.Tap(buttons[button])
				confirm, _ := credentialDialogControls(a.window)
				if confirm["Yes"] == nil {
					t.Fatal("full account has no explicit disconnect confirmation")
				}
				test.Tap(confirm["No"])
				if engine.GetState() != client.StateConnected || a.credentialBusy {
					t.Fatal("declining disconnected client or started operation")
				}
				if action == "preflight-failure" {
					entries[2].SetText("incorrect-personal")
				}
				test.Tap(buttons[button])
				confirm, _ = credentialDialogControls(a.window)
				test.Tap(confirm["Yes"])
				complete := func() {
					t.Helper()
					select {
					case fn := <-completions:
						fn()
					case <-time.After(8 * time.Second):
						t.Fatal("credential action did not complete")
					}
				}
				complete() // joined engine disconnect; starts dedicated preflight/exchange
				complete() // rotation private stage or administrator inspection
				if engine.GetState() != client.StateDisconnected {
					t.Fatal("dedicated operation retained media session")
				}
				if action == "preflight-failure" {
					if a.credentialBusy || len(bs.PendingCredentials) != 0 || bs.Bookmarks[0].Token != "old-personal" {
						t.Fatal("failed preflight changed private credential or retained operation")
					}
					got, err := st.NonTx().GetUserByID(actor.ID)
					if err != nil || got.PersonalTokenHash != crypto.HashToken("old-personal") {
						t.Fatal("failed preflight changed server credential")
					}
					return
				}
				if admin {
					buttons, _ = credentialDialogControls(a.window)
					if buttons["Revoke credential"].Disabled() {
						t.Fatal("admin inspect failed at capacity")
					}
					test.Tap(buttons["Revoke credential"])
					confirm, _ = credentialDialogControls(a.window)
					test.Tap(confirm["Yes"])
					complete()
					targetNow, err := st.NonTx().GetUserByID(target.ID)
					if err != nil || targetNow.PersonalTokenHash != "" {
						t.Fatal("admin mutation failed at capacity")
					}
				} else {
					complete() // mutation and exact candidate reconciliation
					if err := bs.Load(); err != nil {
						t.Fatal(err)
					}
					if len(bs.PendingCredentials) != 0 || bs.Bookmarks[0].Token == "old-personal" {
						t.Fatal("rotation failed at capacity")
					}
					got, err := st.NonTx().GetUserByPersonalTokenHash(crypto.HashToken(bs.Bookmarks[0].Token))
					if err != nil || got == nil || got.ID != actor.ID || got.Role != role {
						t.Fatal("rotation changed identity/rights")
					}
					if other != nil {
						if _, err := other.PersonalTokenExchange(t.Context(), &pb.ControlMessage{PersonalTokenStatusReq: &pb.PersonalTokenStatusRequest{}}); err == nil {
							t.Fatal("rotation retained another live account session")
						}
					}
				}
			})
		}
	}
}
