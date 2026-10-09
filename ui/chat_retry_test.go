package ui

import (
	"crypto/tls"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

// This peer exercises actual Engine wire requests/callbacks; the server test
// separately forces the real shared limiter and verifies live selection recovery.
func TestChatHistoryRetrySendsSameChannelAndCursor(t *testing.T) {
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
	requests := make(chan pb.ChatHistoryRequest, 4)
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
		if err := protocol.WriteControlMessage(conn, &pb.ControlMessage{AuthResponse: &pb.AuthResponse{SessionID: 1, Username: "Guest", Role: "user", MediaCipher: "aes128", EncryptionKey: make([]byte, 16), VoiceRegistrationKey: make([]byte, 32)}}); err != nil {
			return
		}
		count := 0
		for {
			msg, err := protocol.ReadControlMessage(conn)
			if err != nil {
				return
			}
			if msg.ChatHistoryReq == nil {
				continue
			}
			requests <- *msg.ChatHistoryReq
			count++
			reply := &pb.ControlMessage{ErrorResponse: &pb.ErrorResponse{Code: 8, Message: "server control capacity reached"}}
			if count%2 == 0 {
				id := int64(10)
				if msg.ChatHistoryReq.BeforeID > 0 {
					id = 9
				}
				reply = &pb.ControlMessage{ChatHistoryResp: &pb.ChatHistoryResponse{ChannelID: 2, HasMore: true, Messages: []pb.ChatMessage{{ID: id, ChannelID: 2}}}}
			}
			if err := protocol.WriteControlMessage(conn, reply); err != nil {
				return
			}
		}
	}()
	errors := make(chan error, 2)
	pages := make(chan pb.ChatHistoryResponse, 2)
	a.engine.OnError = func(err error) { errors <- err }
	a.engine.OnChatHistory = func(page pb.ChatHistoryResponse) { pages <- page }
	if err := a.engine.Connect(listener.Addr().String(), "127.0.0.1:9601", "", "Guest", client.SPKIFingerprint(certificate.Certificate())); err != nil {
		t.Fatal(err)
	}
	defer func() { a.resetChat(); a.engine.Disconnect(); <-done }()
	checkRequest := func(before int64) {
		t.Helper()
		select {
		case req := <-requests:
			if req.ChannelID != 2 || req.BeforeID != before {
				t.Fatalf("wrong retry target: %#v", req)
			}
		case <-time.After(time.Second):
			t.Fatal("history request not sent")
		}
	}
	refused := func() {
		t.Helper()
		select {
		case <-errors:
		case <-time.After(time.Second):
			t.Fatal("refusal callback not received")
		}
		if !a.chatLoading {
			t.Fatal("generic refusal misattributed")
		}
		a.expireChatHistory(a.chatAttempt)
	}
	accepted := func() {
		t.Helper()
		select {
		case page := <-pages:
			a.addChatHistory(page)
		case <-time.After(time.Second):
			t.Fatal("history callback not received")
		}
	}
	a.selectChatChannel(2)
	checkRequest(0)
	refused()
	// The formerly selected channel must retry without discarding tombstones.
	a.chatDeleted[8] = true
	a.selectChatChannel(2)
	if !a.chatDeleted[8] {
		t.Fatal("initial retry discarded deletion state")
	}
	checkRequest(0)
	accepted()
	a.loadEarlierChat()
	checkRequest(10)
	refused()
	test.Tap(a.chatMore)
	checkRequest(10)
	accepted()
	if a.chatLoading || a.chatBeforeID != 9 || len(a.chatRows) != 2 {
		t.Fatal("retry did not finish the page")
	}
}

func TestChatHistoryTimeoutIsAttemptQualified(t *testing.T) {
	f := test.NewApp()
	defer f.Quit()
	a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.bindEvents()
	defer a.resetChat()
	a.chatChannelID = 1
	a.startChatLoading()
	old := a.chatAttempt
	a.resetChat()
	a.chatChannelID = 2
	a.startChatLoading()
	current := a.chatAttempt
	a.expireChatHistory(old)
	if !a.chatLoading {
		t.Fatal("old timeout cleared new selection")
	}
	// An uncorrelated error must not change the current attempt.
	a.engine.OnError(errors.New("unrelated request refused"))
	if !a.chatLoading {
		t.Fatal("generic error cleared history")
	}
	a.expireChatHistory(current)
	if a.chatLoading || !a.chatRetry || !a.chatMore.Visible() || a.chatMore.Disabled() {
		t.Fatal("initial timeout left selection unretryable")
	}
	a.startChatLoading()
	a.addChatHistory(pb.ChatHistoryResponse{ChannelID: 2, HasMore: true, Messages: []pb.ChatMessage{{ID: 10, ChannelID: 2}}})
	a.expireChatHistory(a.chatAttempt)
	if a.chatRetry || a.chatLoading {
		t.Fatal("completed request expired")
	}
	a.startChatLoading()
	a.expireChatHistory(a.chatAttempt)
	if a.chatLoading || a.chatRetry || a.chatMore.Disabled() || a.chatBeforeID != 10 || len(a.chatRows) != 1 {
		t.Fatal("earlier-page timeout lost cursor or disabled retry")
	}
}
