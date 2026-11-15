package ui

import (
	"crypto/tls"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestInviteAdminDialogWireFlow(t *testing.T) {
	f := test.NewApp()
	defer f.Quit()
	defer fyne.SetCurrentApp(f)
	driver := &audioSettingsDriver{Driver: f.Driver(), completion: make(chan func(), 32)}
	fyne.SetCurrentApp(&audioSettingsApp{App: f, driver: driver})
	a := &App{fyneApp: f, window: f.NewWindow("Invites"), engine: client.NewEngine()}
	defer a.window.Close()
	certificate := httptest.NewTLSServer(nil)
	defer certificate.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", certificate.TLS)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan *pb.ControlMessage, 4)
	replies := make(chan *pb.ControlMessage, 4)
	stop, done := make(chan struct{}), make(chan struct{})
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
		if err := protocol.WriteControlMessage(conn, &pb.ControlMessage{AuthResponse: &pb.AuthResponse{SessionID: 1, Username: "Admin", Role: "admin", MediaCipher: "aes128", EncryptionKey: make([]byte, 16), VoiceRegistrationKey: make([]byte, 32)}}); err != nil {
			return
		}
		for {
			msg, err := protocol.ReadControlMessage(conn)
			if err != nil {
				return
			}
			if msg.ListInvitesReq == nil && msg.RevokeInviteReq == nil {
				continue
			}
			select {
			case requests <- msg:
			case <-stop:
				return
			}
			select {
			case reply := <-replies:
				if protocol.WriteControlMessage(conn, reply) != nil {
					return
				}
			case <-stop:
				return
			}
		}
	}()
	defer func() { close(stop); _ = listener.Close(); a.engine.Disconnect(); <-done }()
	if err := a.engine.Connect(listener.Addr().String(), "127.0.0.1:9601", "", "Admin", client.SPKIFingerprint(certificate.Certificate())); err != nil {
		t.Fatal(err)
	}
	request := func() *pb.ControlMessage {
		t.Helper()
		select {
		case msg := <-requests:
			return msg
		case <-time.After(3 * time.Second):
			t.Fatal("invite request missing")
			return nil
		}
	}
	apply := func(reply *pb.ControlMessage) {
		t.Helper()
		replies <- reply
		select {
		case fn := <-driver.completion:
			fn()
		case <-time.After(3 * time.Second):
			t.Fatal("invite UI completion missing")
		}
	}
	var list *widget.List
	buttons := map[string]*widget.Button{}
	collect := func() {
		buttons = map[string]*widget.Button{}
		var visit func(fyne.CanvasObject)
		visit = func(object fyne.CanvasObject) {
			switch value := object.(type) {
			case *widget.List:
				list = value
			case *widget.Button:
				buttons[value.Text] = value
			case *fyne.Container:
				for _, child := range value.Objects {
					visit(child)
				}
			case fyne.Widget:
				for _, child := range test.WidgetRenderer(value).Objects() {
					visit(child)
				}
			}
		}
		for _, overlay := range a.window.Canvas().Overlays().List() {
			visit(overlay)
		}
	}
	a.showInviteListDialog()
	collect()
	if req := request(); req.ListInvitesReq == nil || req.ListInvitesReq.AfterID != 0 {
		t.Fatal("wrong initial page")
	}
	if !buttons["Refresh"].Disabled() {
		t.Fatal("refresh enabled during request")
	}
	apply(&pb.ControlMessage{ListInvitesResp: &pb.ListInvitesResponse{Invites: []pb.InviteInfo{{ID: 12, Role: "admin"}, {ID: 42, Role: "user"}}, HasMore: true}})
	if list == nil || list.Length() != 2 || buttons["Next page"].Disabled() {
		t.Fatal("page not displayed")
	}
	test.Tap(buttons["Next page"])
	if req := request(); req.ListInvitesReq == nil || req.ListInvitesReq.AfterID != 42 {
		t.Fatal("wrong next cursor")
	}
	apply(&pb.ControlMessage{ListInvitesResp: &pb.ListInvitesResponse{Error: "permission denied"}})
	if buttons["Refresh"].Disabled() {
		t.Fatal("error left request pending")
	}
	list.Select(1)
	test.Tap(buttons["Revoke selected"])
	collect()
	if buttons["Yes"] == nil {
		t.Fatal("revoke confirmation missing")
	}
	test.Tap(buttons["Yes"])
	if req := request(); req.RevokeInviteReq == nil || req.RevokeInviteReq.InviteID != 42 {
		t.Fatal("revoked display index instead of stable ID")
	}
	apply(&pb.ControlMessage{RevokeInviteResp: &pb.RevokeInviteResponse{Revoked: true}})
	if req := request(); req.ListInvitesReq == nil || req.ListInvitesReq.AfterID != 0 {
		t.Fatal("revocation did not refresh current page")
	}
	// Closing a dialog before its response must not mutate its list or a replacement dialog.
	collect()
	test.Tap(buttons["Close"])
	apply(&pb.ControlMessage{ListInvitesResp: &pb.ListInvitesResponse{}})
	if list.Length() != 2 {
		t.Fatal("closed dialog consumed response")
	}
}

func TestInviteEntryMetadata(t *testing.T) {
	if text := inviteEntryText(pb.InviteInfo{ExpiresAt: 1}); !strings.Contains(text, "revoked") {
		t.Fatal("revoked expiry is not identified")
	}
	text := inviteEntryText(pb.InviteInfo{ID: 42, Role: "admin", CreatedBy: 7, UseCount: 3})
	for _, part := range []string{"#42", "admin", "3/unlimited", "expires never", "Created by #7"} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing %q", part)
		}
	}
}
