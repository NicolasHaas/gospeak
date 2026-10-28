package ui

import (
	"crypto/tls"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestChatDraftsStayWithTextChannel(t *testing.T) {
	f := test.NewApp()
	defer f.Quit()
	a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	certificate := httptest.NewTLSServer(nil)
	defer certificate.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", certificate.TLS)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requests := make(chan *pb.ControlMessage, 16)
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
			SessionID: 1, Username: "Guest", Role: "user", MediaCipher: "aes128",
			EncryptionKey: make([]byte, 16), VoiceRegistrationKey: make([]byte, 32),
		}}); err != nil {
			return
		}
		for {
			msg, err := protocol.ReadControlMessage(conn)
			if err != nil {
				return
			}
			if msg.ChatMsg != nil || msg.ChatHistoryReq != nil {
				requests <- msg
			}
		}
	}()
	if err := a.engine.Connect(listener.Addr().String(), "127.0.0.1:9601", "", "Guest", client.SPKIFingerprint(certificate.Certificate())); err != nil {
		t.Fatal(err)
	}
	defer func() { a.resetChat(); a.engine.Disconnect(); <-done }()
	next := func() *pb.ControlMessage {
		t.Helper()
		select {
		case msg := <-requests:
			return msg
		case <-time.After(2 * time.Second):
			t.Fatal("missing chat request")
			return nil
		}
	}
	selectChannel := func(id int64) {
		t.Helper()
		a.selectChatChannel(id)
		if msg := next(); msg.ChatHistoryReq == nil || msg.ChatHistoryReq.ChannelID != id {
			t.Fatalf("unexpected request selecting %d: %+v", id, msg)
		}
	}
	selectChannel(1)
	long := "  " + strings.Repeat("界", model.MessageMaxBodyLength+1) + "  "
	a.chatEntry.SetText(long)
	a.chatEntry.OnSubmitted(a.chatEntry.Text)
	if a.chatEntry.Text != long {
		t.Fatal("overlength refusal discarded or modified the draft")
	}
	// The next history request is a wire barrier: a refused send must not
	// appear ahead of it, and changing text selection must not carry its draft.
	selectChannel(2)
	if a.chatEntry.Text != "" {
		t.Error("channel 1 draft followed selection to channel 2")
	}
	a.chatEntry.SetText("channel two")
	selectChannel(1)
	if a.chatEntry.Text != long {
		t.Error("channel 1 draft was not restored")
	}
	a.chatEntry.SetText("channel one")
	selectChannel(2)
	if a.chatEntry.Text != "channel two" {
		t.Error("channel 2 draft was not restored")
	}
	test.Tap(a.chatSend)
	if msg := next(); msg.ChatMsg == nil || msg.ChatMsg.ChannelID != 2 || msg.ChatMsg.Text != "channel two" {
		t.Fatalf("sent draft to wrong channel: %+v", msg)
	}
	if a.chatEntry.Text != "" {
		t.Error("successful send retained composer text")
	}
	selectChannel(1)
	if a.chatEntry.Text != "channel one" {
		t.Error("sending another channel's draft changed this draft")
	}
	selectChannel(2)
	if a.chatEntry.Text != "" {
		t.Error("sent draft resurrected on reselection")
	}
	a.chatEntry.SetText("retry draft")
	a.chatRetry = true
	selectChannel(2)
	if a.chatEntry.Text != "retry draft" {
		t.Error("same-channel history retry changed draft")
	}
	// Full disconnect must not leak drafts into a different server's IDs.
	a.bindEvents()
	disconnected := make(chan struct{})
	onState := a.engine.OnStateChange
	a.engine.OnStateChange = func(state client.State) {
		onState(state)
		if state == client.StateDisconnected {
			close(disconnected)
		}
	}
	a.engine.Disconnect()
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("missing disconnect callback")
	}
	fyne.DoAndWait(func() {})
	if a.chatEntry.Text != "" || a.chatChannelID != 0 || len(a.chatDrafts) != 0 {
		t.Error("disconnect retained composer or text selection")
	}
}
