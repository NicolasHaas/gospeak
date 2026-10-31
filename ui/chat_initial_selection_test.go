package ui

import (
	"crypto/tls"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestInitialTextSelectionUsesAuthenticatedScope(t *testing.T) {
	f := test.NewApp()
	defer f.Quit()
	a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.bindEvents()
	disconnected := make(chan struct{}, 1)
	onState := a.engine.OnStateChange
	a.engine.OnStateChange = func(state client.State) {
		onState(state)
		if state == client.StateDisconnected {
			disconnected <- struct{}{}
		}
	}
	updated := make(chan struct{}, 4)
	onChannels := a.engine.OnChannelsUpdate
	a.engine.OnChannelsUpdate = func(channels []pb.ChannelInfo) {
		onChannels(channels)
		updated <- struct{}{}
	}
	// Consume expected headless audio failures without opening a dialog.
	a.engine.OnAudioFailure = func(error) {}
	certificate := httptest.NewTLSServer(nil)
	defer certificate.Close()
	// A full Lobby remains readable, but ordinary voice autojoin skips it.
	channels := []pb.ChannelInfo{{ID: 1, Name: "Lobby", MaxUsers: 1, Users: []pb.UserInfo{{ID: 7, Username: "Member"}}}, {ID: 2, Name: "Scoped"}, {ID: 3, Name: "Chat"}}

	// Reuse the same engine/UI: the second login must forget the first scope.
	for _, scope := range []int64{2, 0} {
		listener, err := tls.Listen("tcp", "127.0.0.1:0", certificate.TLS)
		if err != nil {
			t.Fatal(err)
		}
		requests := make(chan *pb.ControlMessage, 16)
		peer := make(chan net.Conn, 1)
		joins := make(chan int64, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			peer <- conn
			if _, err := protocol.ReadControlMessage(conn); err != nil {
				return
			}
			if err := protocol.WriteControlMessage(conn, &pb.ControlMessage{AuthResponse: &pb.AuthResponse{
				SessionID: 1, Username: "Guest", Role: "user", ChannelScope: scope, Channels: channels,
				MediaCipher: "aes128", EncryptionKey: make([]byte, 16), VoiceRegistrationKey: make([]byte, 32),
			}}); err != nil {
				return
			}
			for {
				msg, err := protocol.ReadControlMessage(conn)
				if err != nil {
					return
				}
				if msg.JoinChannelRequest != nil {
					select {
					case joins <- msg.JoinChannelRequest.ChannelID:
					default:
						t.Error("unexpected duplicate voice autojoin")
						return
					}
				}
				if msg.ChatHistoryReq != nil {
					select {
					case requests <- msg:
					default:
						t.Error("unexpected history request flood")
						return
					}
				}
			}
		}()
		defer func() {
			_ = listener.Close()
			a.engine.Disconnect()
			<-done
		}()
		if err := a.engine.Connect(listener.Addr().String(), "127.0.0.1:9601", "", "Guest", client.SPKIFingerprint(certificate.Certificate())); err != nil {
			t.Fatal(err)
		}
		select {
		case <-updated:
		case <-time.After(3 * time.Second):
			t.Fatal("initial channel callback not delivered")
		}
		want := scope
		if want == 0 {
			want = 1
		}
		select {
		case msg := <-requests:
			if msg.ChatHistoryReq.ChannelID != want {
				t.Errorf("scope %d: initial history targeted %d, want %d (foreign history attempted)", scope, msg.ChatHistoryReq.ChannelID, want)
			}
		case <-time.After(3 * time.Second):
			t.Error("initial history request not sent")
		}
		fyne.DoAndWait(func() {
			if a.engine.GetChannelID() != 0 || a.chatChannelID != want {
				t.Errorf("selection before voice ACK: voice=%d text=%d, want 0/%d", a.engine.GetChannelID(), a.chatChannelID, want)
			}
			// A user may choose another text channel before a delayed voice ACK.
			if scope == 0 {
				a.selectChatChannel(3)
				a.selectedChannelID = 3
				want = 3
			}
		})
		conn := <-peer
		select {
		case id := <-joins:
			if err := protocol.WriteControlMessage(conn, &pb.ControlMessage{ChannelJoinResponse: &pb.ChannelJoinResponse{Success: true, ChannelID: id}}); err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("voice autojoin not requested")
		}
		if err := protocol.WriteControlMessage(conn, &pb.ControlMessage{ServerStateEvent: &pb.ServerStateEvent{Channels: channels}}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-updated:
		case <-time.After(3 * time.Second):
			t.Error("post-join channel update not delivered")
		}
		fyne.DoAndWait(func() {
			if a.chatChannelID != want || a.engine.GetChannelID() != 2 {
				t.Errorf("voice ACK/state update changed text selection: voice=%d text=%d, want 2/%d", a.engine.GetChannelID(), a.chatChannelID, want)
			}
		})
		a.engine.Disconnect()
		select {
		case <-disconnected:
		case <-time.After(3 * time.Second):
			t.Fatal("disconnect callback not delivered")
		}
		fyne.DoAndWait(func() {})
		if a.engine.GetChannelScope() != 0 {
			t.Error("disconnect retained authenticated scope")
		}
		if err := listener.Close(); err != nil {
			t.Error(err)
		}
		<-done
		// Every history request in the scoped session must target its scope.
		for len(requests) > 0 {
			if msg := <-requests; scope != 0 && msg.ChatHistoryReq.ChannelID != scope {
				t.Errorf("foreign history request: %+v", msg.ChatHistoryReq)
			}
		}
	}
}
