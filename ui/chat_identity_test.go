package ui

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestChatHistoryStaleWireAttempts(t *testing.T) {
	for _, scenario := range []string{"ABA", "initial-retry", "same-cursor-retry"} {
		t.Run(scenario, func(t *testing.T) {
			f := test.NewApp()
			defer f.Quit()
			a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine()}
			a.buildUI()
			cert := httptest.NewTLSServer(nil)
			defer cert.Close()
			listener, err := tls.Listen("tcp", "127.0.0.1:0", cert.TLS)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			requests := make(chan pb.ChatHistoryRequest, 8)
			replies := make(chan *pb.ControlMessage, 8)
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				if _, err = protocol.ReadControlMessage(conn); err != nil {
					return
				}
				if err = protocol.WriteControlMessage(conn, &pb.ControlMessage{AuthResponse: &pb.AuthResponse{SessionID: 1, Username: "Guest", Role: "user", MediaCipher: "aes128", EncryptionKey: make([]byte, 16), VoiceRegistrationKey: make([]byte, 32), HistoryRequestIDs: true}}); err != nil {
					return
				}
				writerDone := make(chan struct{})
				go func() {
					defer close(writerDone)
					for reply := range replies {
						if protocol.WriteControlMessage(conn, reply) != nil {
							return
						}
					}
				}()
				defer func() { close(replies); <-writerDone }()
				for {
					msg, err := protocol.ReadControlMessage(conn)
					if err != nil {
						return
					}
					if msg.ChatHistoryReq != nil {
						requests <- *msg.ChatHistoryReq
					}
				}
			}()
			pages := make(chan pb.ChatHistoryResponse, 8)
			a.engine.OnChatHistory = func(page pb.ChatHistoryResponse) { pages <- page }
			if err := a.engine.Connect(listener.Addr().String(), "127.0.0.1:9601", "", "Guest", client.SPKIFingerprint(cert.Certificate())); err != nil {
				t.Fatal(err)
			}
			defer func() { a.resetChat(); a.engine.Disconnect(); <-done }()
			var lastID int64
			request := func(channel, before int64) pb.ChatHistoryRequest {
				t.Helper()
				select {
				case r := <-requests:
					if r.ChannelID != channel || r.BeforeID != before || r.RequestID <= lastID {
						t.Fatalf("wrong target or reused attempt: %+v, prior=%d", r, lastID)
					}
					lastID = r.RequestID
					return r
				case <-time.After(time.Second):
					t.Fatal("request not observed")
					return pb.ChatHistoryRequest{}
				}
			}
			page := func(req pb.ChatHistoryRequest, id int64) pb.ChatHistoryResponse {
				t.Helper()
				replies <- &pb.ControlMessage{ChatHistoryResp: &pb.ChatHistoryResponse{RequestID: req.RequestID, ChannelID: req.ChannelID, HasMore: true, Messages: []pb.ChatMessage{{ID: id, ChannelID: req.ChannelID}}}}
				select {
				case p := <-pages:
					return p
				case <-time.After(time.Second):
					t.Fatal("callback missing")
					return pb.ChatHistoryResponse{}
				}
			}
			a.selectChatChannel(1)
			first := request(1, 0)
			var stale pb.ChatHistoryResponse
			switch scenario {
			case "ABA":
				stale = page(first, 90) // Engine callback queued; UI adoption deliberately delayed.
				a.selectChatChannel(2)
				request(2, 0)
				a.selectChatChannel(1)
				request(1, 0)
			case "initial-retry":
				stale = page(first, 90)
				a.expireChatHistory(a.chatAttempt)
				a.selectChatChannel(1)
				request(1, 0)
			case "same-cursor-retry":
				a.addChatHistory(page(first, 100))
				a.loadEarlierChat()
				earlier := request(1, 100)
				stale = page(earlier, 90)
				a.expireChatHistory(a.chatAttempt)
				a.loadEarlierChat()
				request(1, 100)
			}
			before, rows, timer := a.chatBeforeID, len(a.chatRows), a.chatTimer
			a.addChatHistory(stale)
			if !a.chatLoading || a.chatBeforeID != before || len(a.chatRows) != rows || a.chatTimer != timer {
				t.Fatal("stale queued wire reply mutated newer attempt")
			}
			a.chatDeleted[80] = true
			a.addChatMessage(pb.ChatMessage{ID: 95, ChannelID: 1})
			a.addChatMessage(pb.ChatMessage{ID: 0, ChannelID: 1, Text: "live only"})
			valid := pb.ChatHistoryResponse{RequestID: a.chatRequestID, ChannelID: 1, HasMore: false, Messages: []pb.ChatMessage{{ID: 99, ChannelID: 1}, {ID: 95, ChannelID: 1}, {ID: 80, ChannelID: 1}}}
			a.addChatHistory(valid)
			if a.chatLoading || a.chatHasMore || a.chatBeforeID != 80 {
				t.Fatal("valid page did not complete with oldest returned cursor")
			}
			for i, row := range a.chatRows {
				if row.ID == 80 {
					t.Fatal("history resurrected deletion")
				}
				if i > 0 && row.ID > 0 && (a.chatRows[i-1].ID == 0 || a.chatRows[i-1].ID >= row.ID) {
					t.Fatal("stored IDs not merged in order")
				}
			}
			if a.chatRows[len(a.chatRows)-1].ID != 0 {
				t.Fatal("live-only row lost or reordered")
			}
		})
	}
}
