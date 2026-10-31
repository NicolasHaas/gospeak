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
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestRolePermissionControls(t *testing.T) {
	f := test.NewApp()
	defer f.Quit()
	certificate := httptest.NewTLSServer(nil)
	defer certificate.Close()
	for _, role := range []string{"admin", "moderator", "user"} {
		t.Run(role, func(t *testing.T) {
			a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine(), serverSaved: true}
			defer a.window.Close()
			a.buildUI()
			a.bindEvents()
			a.engine.OnAudioFailure = func(error) {}
			connected := make(chan struct{}, 1)
			onState := a.engine.OnStateChange
			a.engine.OnStateChange = func(state client.State) {
				onState(state)
				if state == client.StateConnected {
					connected <- struct{}{}
				}
			}
			listener, err := tls.Listen("tcp", "127.0.0.1:0", certificate.TLS)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				if _, err := protocol.ReadControlMessage(conn); err != nil {
					return
				}
				if err := protocol.WriteControlMessage(conn, &pb.ControlMessage{AuthResponse: &pb.AuthResponse{
					SessionID: 1, Username: "Guest", Role: role, MediaCipher: "aes128",
					EncryptionKey: make([]byte, 16), VoiceRegistrationKey: make([]byte, 32),
				}}); err != nil {
					return
				}
				for {
					if _, err := protocol.ReadControlMessage(conn); err != nil {
						return
					}
				}
			}()
			defer func() {
				_ = listener.Close()
				a.engine.Disconnect()
				<-done
				fyne.DoAndWait(func() {})
			}()
			if err := a.engine.Connect(listener.Addr().String(), "127.0.0.1:9601", "", "Guest", client.SPKIFingerprint(certificate.Certificate())); err != nil {
				t.Fatal(err)
			}
			select {
			case <-connected:
			case <-time.After(3 * time.Second):
				t.Fatal("connected callback missing")
			}
			fyne.DoAndWait(func() {})
			admin := role == "admin"
			moderation := admin || role == "moderator"
			if a.serverBtn.Visible() != admin {
				t.Errorf("Server Settings visible=%v, want %v", a.serverBtn.Visible(), admin)
			}
			clearDialogs := func() {
				for _, overlay := range a.window.Canvas().Overlays().List() {
					a.window.Canvas().Overlays().Remove(overlay)
				}
			}
			checkButton := func(name string, want bool) {
				t.Helper()
				found := false
				var visit func(fyne.CanvasObject)
				visit = func(o fyne.CanvasObject) {
					if !o.Visible() {
						return
					}
					switch v := o.(type) {
					case *widget.Button:
						found = found || v.Text == name
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
				visit(a.chatBox)
				for _, overlay := range a.window.Canvas().Overlays().List() {
					visit(overlay)
				}
				if found != want {
					t.Errorf("%s present=%v, want %v", name, found, want)
				}
			}
			a.showServerSettings() // Also guard the dialog independently of toolbar visibility.
			for _, name := range []string{"Create Token", "Create Channel", "Manage Bans", "Export Channels (YAML)", "Export Users (YAML)", "Import Channels (YAML)"} {
				checkButton(name, admin)
			}
			clearDialogs()
			a.engine.OnRoleChanged(true, "Role updated")
			fyne.DoAndWait(func() {})
			if a.serverBtn.Visible() != admin {
				t.Errorf("role callback: Server Settings visible=%v, want %v", a.serverBtn.Visible(), admin)
			}
			clearDialogs()
			for _, allowSub := range []bool{false, true} {
				channel := pb.ChannelInfo{ID: 2, Name: "Games", AllowSubChannels: allowSub}
				a.channels = []pb.ChannelInfo{channel}
				row := a.channelList.CreateItem().(*fyne.Container)
				a.updateChannelListItem(0, row)
				if got := row.Objects[4].Visible(); got != (admin || allowSub) {
					t.Errorf("sub-channels=%v: gear visible=%v", allowSub, got)
				}
				a.showChannelSettingsDialog(channel)
				checkButton("Delete Channel", admin)
				checkButton("Create Sub-Channel", allowSub)
				checkButton("Join Channel", true)
				clearDialogs()
			}
			a.showChannelSettingsDialog(pb.ChannelInfo{ID: 1, Name: "Lobby"})
			checkButton("Delete Channel", false)
			clearDialogs()
			a.showUserContextMenu(pb.UserInfo{ID: 2, Username: "Member", Role: "user"})
			checkButton("Kick User", moderation)
			clearDialogs()
			a.chatChannelID = 2
			a.chatRows = []pb.ChatMessage{{ID: 1, ChannelID: 2, Text: "stored message"}}
			a.renderChat()
			checkButton("Delete", moderation)
		})
	}
}
