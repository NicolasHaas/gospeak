package ui

import (
	"crypto/tls"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func credentialDialogControls(w fyne.Window) (map[string]*widget.Button, []*widget.Entry) {
	buttons := make(map[string]*widget.Button)
	var entries []*widget.Entry
	var visit func(fyne.CanvasObject)
	visit = func(o fyne.CanvasObject) {
		switch v := o.(type) {
		case *widget.Button:
			buttons[v.Text] = v
		case *widget.Entry:
			entries = append(entries, v)
		case *fyne.Container:
			for _, child := range v.Objects {
				visit(child)
			}
		case fyne.Widget:
			for _, child := range test.WidgetRenderer(v).Objects() {
				visit(child)
			}
		}
	}
	for _, o := range w.Canvas().Overlays().List() {
		visit(o)
	}
	return buttons, entries
}

func TestPersonalCredentialDialogStagesBeforeWireAndSurvivesDisconnect(t *testing.T) {
	for _, saveFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "rotate", true: "stage-failure"}[saveFailure], func(t *testing.T) {
			isolateAudioSettingsConfig(t)
			f := test.NewApp()
			defer f.Quit()
			completions := make(chan func(), 32)
			fyne.SetCurrentApp(&audioSettingsApp{App: f, driver: &audioSettingsDriver{Driver: f.Driver(), completion: completions}})
			defer fyne.SetCurrentApp(f)
			cert := httptest.NewTLSServer(nil)
			defer cert.Close()
			listener, err := tls.Listen("tcp", "127.0.0.1:0", cert.TLS)
			if err != nil {
				t.Fatal(err)
			}
			bs := client.NewBookmarkStore()
			addr := listener.Addr().String()
			bs.Add(client.Bookmark{ControlAddr: addr, Username: "person", Token: "historical-personal"})
			bs.TrustServer(addr, client.SPKIFingerprint(cert.Certificate()))
			if err := bs.Save(); err != nil {
				t.Fatal(err)
			}
			stop := make(chan struct{})
			mutated := make(chan bool, 1)
			var clients sync.WaitGroup
			go func() {
				defer close(stop)
				current := "historical-personal"
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					clients.Add(1)
					func() {
						defer clients.Done()
						defer conn.Close()
						_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
						msg, err := protocol.ReadControlMessage(conn)
						if err != nil || msg.AuthRequest == nil || !msg.AuthRequest.PersonalOnly {
							return
						}
						if msg.AuthRequest.Token != current {
							_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{ErrorResponse: &pb.ErrorResponse{Code: 2, Message: "authentication failed"}})
							return
						}
						_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{AuthResponse: &pb.AuthResponse{SessionID: 1, UserID: 7, Username: "person", Role: "user", CredentialVersion: 3, MediaCipher: "aes128", EncryptionKey: make([]byte, 16), VoiceRegistrationKey: make([]byte, 32)}})
						msg, err = protocol.ReadControlMessage(conn)
						if err != nil || msg.RotatePersonalTokenReq == nil {
							return
						}
						disk := client.NewBookmarkStore()
						err = disk.Load()
						staged := err == nil && len(disk.PendingCredentials) == 1 && disk.PendingCredentials[0].Candidate == msg.RotatePersonalTokenReq.Replacement && disk.PendingCredentials[0].OldToken == current
						mutated <- staged
						current = msg.RotatePersonalTokenReq.Replacement
						// No success acknowledgement: production UI must reconcile its candidate.
					}()
				}
			}()
			defer func() { _ = listener.Close(); <-stop; clients.Wait() }()
			a := &App{fyneApp: f, window: f.NewWindow("Credentials"), engine: client.NewEngine(), bookmarks: bs, connectServer: addr, connectToken: "historical-personal"}
			defer a.window.Close()
			a.showPersonalCredentialDialog()
			buttons, entries := credentialDialogControls(a.window)
			if buttons["Rotate my credential"] == nil || len(entries) < 2 {
				t.Fatal("production credential controls missing")
			}
			entries[1].SetText("person")
			test.Tap(buttons["Rotate my credential"])
			var stage func()
			select {
			case stage = <-completions:
			case <-time.After(4 * time.Second):
				t.Fatal("preflight did not complete")
			}
			if saveFailure {
				path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "gospeak", "servers.yaml")
				// Use the actual configured path, resolved by the existing store convention.
				if _, err := os.Stat(path); err != nil { // #nosec G703 -- Test-owned config root is set to t.TempDir; inspect only its synthetic bookmark.
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("bookmarks: ["), 0600); err != nil { // #nosec G703 -- Deliberately corrupt the test-owned bookmark under t.TempDir.
					t.Fatal(err)
				}
			}
			stage()
			if saveFailure {
				select {
				case <-mutated:
					t.Fatal("failed private stage sent a mutation")
				default:
				}
				if a.credentialBusy {
					t.Fatal("failed stage retained operation ownership")
				}
				return
			}
			select {
			case staged := <-mutated:
				if !staged {
					t.Fatal("wire mutation preceded private staging")
				}
			case <-time.After(4 * time.Second):
				t.Fatal("rotation not sent")
			}
			// Media disconnect/cancellation cannot discard this operation's completion.
			a.connectAttempt++
			a.engine.Disconnect()
			select {
			case finish := <-completions:
				finish()
			case <-time.After(4 * time.Second):
				t.Fatal("credential completion lost with media generation")
			}
			disk := client.NewBookmarkStore()
			if err := disk.Load(); err != nil {
				t.Fatal(err)
			}
			if len(disk.PendingCredentials) != 0 || disk.Bookmarks[0].Token == "historical-personal" {
				t.Fatal("lost acknowledgement was not reconciled privately")
			}
			if a.connectToken != "historical-personal" {
				t.Fatal("stale completion overwrote another connection attempt")
			}
		})
	}
}
