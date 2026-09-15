package server

import (
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestCreateChannelUsesRuneLimits(t *testing.T) {
	t.Run("accepts name at rune limit", func(t *testing.T) {
		srv, st, handler := newTestServer(t)
		admin := mustCreateSession(t, srv.sessions, 1, "admin", model.RoleAdmin)
		name := strings.Repeat("界", model.MaxChannelNameLength)

		srv.handleCreateChannel(admin.ID, &pb.CreateChannelRequest{Name: name}, st, &nopConn{}, handler)

		channels, err := st.NonTx().ListChannels()
		if err != nil {
			t.Fatalf("ListChannels(): %v", err)
		}
		if len(channels) != 1 || channels[0].Name != name {
			t.Fatalf("channels = %#v, want one channel with %d-rune name", channels, model.MaxChannelNameLength)
		}
	})

	t.Run("rejects name over rune limit", func(t *testing.T) {
		srv, st, handler := newTestServer(t)
		admin := mustCreateSession(t, srv.sessions, 1, "admin", model.RoleAdmin)
		conn := &bufferConn{}
		name := strings.Repeat("界", model.MaxChannelNameLength+1)

		srv.handleCreateChannel(admin.ID, &pb.CreateChannelRequest{Name: name}, st, conn, handler)

		response, err := protocol.ReadControlMessage(conn)
		if err != nil {
			t.Fatalf("ReadControlMessage(): %v", err)
		}
		if response.ErrorResponse == nil {
			t.Fatalf("response = %#v, want channel-name rejection", response)
		}
		channels, err := st.NonTx().ListChannels()
		if err != nil {
			t.Fatalf("ListChannels(): %v", err)
		}
		if len(channels) != 0 {
			t.Fatalf("created %d channels for oversized name", len(channels))
		}
	})

	for _, test := range []struct {
		name  string
		runes int
	}{
		{name: "preserves description at rune limit", runes: model.MaxChannelDescLength},
		{name: "truncates description over rune limit", runes: model.MaxChannelDescLength + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv, st, handler := newTestServer(t)
			admin := mustCreateSession(t, srv.sessions, 1, "admin", model.RoleAdmin)
			description := strings.Repeat("界", test.runes)
			want := strings.Repeat("界", model.MaxChannelDescLength)

			srv.handleCreateChannel(admin.ID, &pb.CreateChannelRequest{
				Name:        "unicode-description",
				Description: description,
			}, st, &nopConn{}, handler)

			channels, err := st.NonTx().ListChannels()
			if err != nil {
				t.Fatalf("ListChannels(): %v", err)
			}
			if len(channels) != 1 {
				t.Fatalf("created %d channels, want 1", len(channels))
			}
			if got := channels[0].Description; got != want || !utf8.ValidString(got) {
				t.Fatalf("description = %q (valid UTF-8: %t), want %d intact runes", got, utf8.ValidString(got), model.MaxChannelDescLength)
			}
		})
	}
}

func TestChatMessagesUseRuneLimits(t *testing.T) {
	for _, test := range []struct {
		name      string
		runes     int
		wantCount int64
	}{
		{name: "accepts text at rune limit", runes: maxChatMessageRunes, wantCount: 1},
		{name: "rejects text over rune limit", runes: maxChatMessageRunes + 1, wantCount: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv, _, handler := newTestServer(t)
			session := mustCreateSession(t, srv.sessions, 1, "sender", model.RoleUser)
			srv.channels.Join(session.ID, 1)

			srv.handleChatMessage(handler, session.ID, &pb.ChatMessage{Text: strings.Repeat("界", test.runes)})

			if got := srv.metrics.ChatMessagesSent.Load(); got != test.wantCount {
				t.Fatalf("ChatMessagesSent = %d, want %d", got, test.wantCount)
			}
		})
	}
}

func TestModerationReasonsTruncateAtRuneBoundary(t *testing.T) {
	for _, action := range []struct {
		name string
		ban  bool
	}{
		{name: "kick"},
		{name: "ban", ban: true},
	} {
		for _, reasonRunes := range []int{maxModerationReasonRunes, maxModerationReasonRunes + 1} {
			name := "at limit"
			if reasonRunes > maxModerationReasonRunes {
				name = "over limit"
			}
			t.Run(action.name+" "+name, func(t *testing.T) {
				srv, st, handler := newTestServer(t)
				adminUser, err := st.NonTx().CreateUser("admin-"+action.name, model.RoleAdmin)
				if err != nil {
					t.Fatalf("CreateUser(admin): %v", err)
				}
				targetUser, err := st.NonTx().CreateUser("target-"+action.name, model.RoleUser)
				if err != nil {
					t.Fatalf("CreateUser(target): %v", err)
				}
				admin := mustCreateSession(t, srv.sessions, adminUser.ID, adminUser.Username, adminUser.Role)
				target := mustCreateSession(t, srv.sessions, targetUser.ID, targetUser.Username, targetUser.Role)
				serverConn, clientConn := net.Pipe()
				t.Cleanup(func() { _ = clientConn.Close() })
				handler.setConn(target.ID, serverConn)

				if err := clientConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatalf("SetReadDeadline(): %v", err)
				}
				type readResult struct {
					response *pb.ControlMessage
					err      error
				}
				readDone := make(chan readResult, 1)
				go func() {
					response, readErr := protocol.ReadControlMessage(clientConn)
					readDone <- readResult{response: response, err: readErr}
				}()

				reason := strings.Repeat("界", reasonRunes)
				if action.ban {
					srv.handleBanUser(handler, admin.ID, &pb.BanUserRequest{UserID: targetUser.ID, Reason: reason}, st, &nopConn{})
				} else {
					srv.handleKickUser(handler, admin.ID, &pb.KickUserRequest{UserID: targetUser.ID, Reason: reason}, &nopConn{})
				}

				result := <-readDone
				if result.err != nil {
					t.Fatalf("ReadControlMessage(): %v", result.err)
				}
				if result.response.ErrorResponse == nil {
					t.Fatalf("response = %#v, want moderation disconnect", result.response)
				}
				prefix := "you have been kicked: "
				if action.ban {
					prefix = "you have been banned: "
				}
				want := prefix + strings.Repeat("界", maxModerationReasonRunes)
				if got := result.response.ErrorResponse.Message; got != want || !utf8.ValidString(got) {
					t.Fatalf("message = %q (valid UTF-8: %t), want %d-rune reason", got, utf8.ValidString(got), maxModerationReasonRunes)
				}
			})
		}
	}
}
