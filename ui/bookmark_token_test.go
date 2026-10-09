package ui

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestBookmarkTokenTarget(t *testing.T) {
	b := &client.Bookmark{ControlAddr: "trusted.example", Username: "Alice", Token: "saved"}
	for _, tc := range []struct{ address, username, token, want string }{
		{"trusted.example:9600", " Alice ", "saved", "saved"},
		{"trusted.example", "Alice", "saved", "saved"},
		{"trusted.example:9700", "Alice", "saved", ""},
		{"other.example", "Alice", "saved", ""},
		{"trusted.example", "Bob", "saved", ""},
		{"other.example", "Bob", "manual", "manual"},
		{"other.example", "Bob", "", ""},
	} {
		if got := bookmarkTokenFor(b, tc.address, tc.username, tc.token); got != tc.want {
			t.Errorf("%#v: got %q", tc, got)
		}
	}
}

func TestConnectDialogBindsSavedToken(t *testing.T) {
	isolateAudioSettingsConfig(t)
	f := test.NewApp()
	defer f.Quit()
	// Queue asynchronous error dialogs; tests only exercise synchronous widgets.
	completions := make(chan func(), 32)
	fyne.SetCurrentApp(&audioSettingsApp{App: f, driver: &audioSettingsDriver{Driver: f.Driver(), completion: completions}})
	defer fyne.SetCurrentApp(f)
	for _, tc := range []struct {
		name, edit, token string
		bypass            bool
	}{
		{name: "server edit", edit: "server"},
		{name: "username edit", edit: "username"},
		{name: "Connect guard", edit: "server", bypass: true},
		{name: "manual token", edit: "server", token: "manual-token"},
		{name: "same bookmark", token: "saved-token"},
		{name: "switch bookmark", edit: "select", token: "second-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			certServer := httptest.NewTLSServer(nil)
			defer certServer.Close()
			listener, err := tls.Listen("tcp", "127.0.0.1:0", certServer.TLS)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			addr := listener.Addr().String()
			bookmarkAddr := addr
			if tc.edit == "server" {
				bookmarkAddr = "trusted.example:9600"
			}
			bs := &client.BookmarkStore{Bookmarks: []client.Bookmark{
				{Name: "First", ControlAddr: bookmarkAddr, Username: "Alice", Token: "saved-token", LastUsed: 2},
				{Name: "Second", ControlAddr: addr, Username: "Bob", Token: "second-token", LastUsed: 1},
			}, TrustedServerPins: map[string]string{addr: client.SPKIFingerprint(certServer.Certificate())}}
			a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine(), bookmarks: bs}
			defer a.window.Close()
			a.showConnectDialog()
			var entries []*widget.Entry
			var connect *widget.Button
			var saved *widget.Select
			var visit func(fyne.CanvasObject)
			visit = func(o fyne.CanvasObject) {
				switch v := o.(type) {
				case *widget.Entry:
					entries = append(entries, v)
				case *widget.Select:
					saved = v
				case *widget.Button:
					if v.Text == "Connect" {
						connect = v
					}
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
			for _, o := range a.window.Canvas().Overlays().List() {
				visit(o)
			}
			if len(entries) < 4 || connect == nil || saved == nil {
				t.Fatal("Connect controls missing")
			}
			// Container order is server, advanced voice, username, password.
			server, username, token := entries[0], entries[2], entries[3]
			if token.Text != "saved-token" {
				t.Fatal("latest bookmark not selected")
			}
			if tc.name == "manual token" {
				token.SetText(tc.token)
			}
			switch tc.edit {
			case "server":
				if tc.bypass {
					server.Text = addr
				} else {
					server.SetText(addr)
				}
			case "username":
				username.SetText("Other")
			case "select":
				saved.SetSelected(saved.Options[2])
			}
			if !tc.bypass && tc.token == "" && token.Text != "" {
				t.Error("edited identity retained saved token")
			}
			got := make(chan *pb.AuthRequest, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				msg, err := protocol.ReadControlMessage(conn)
				if err == nil {
					got <- msg.AuthRequest
				}
			}()
			test.Tap(connect)
			select {
			case request := <-got:
				if request == nil || request.Token != tc.token {
					t.Errorf("AuthRequest token = %#v, want %q", request, tc.token)
				}
			case <-time.After(3 * time.Second):
				t.Error("AuthRequest not received")
			}
			_ = listener.Close()
			<-done
			a.engine.Disconnect()
			select {
			case <-completions: // Connect has reached its final error callback.
			case <-time.After(time.Second):
				t.Fatal("Connect did not finish")
			}
			if bs.Bookmarks[0].Token != "saved-token" || bs.Bookmarks[1].Token != "second-token" {
				t.Fatal("stored bookmark tokens mutated")
			}
		})
	}
}
