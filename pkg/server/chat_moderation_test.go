package server

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestChatPersistenceDisabledKeepsLiveChat(t *testing.T) {
	srv, st, handler := newTestServerWithConfig(t, func(cfg *Config) { cfg.ChatHistoryLimit = 0 })
	channel := model.NewChannel()
	if err := st.NonTx().CreateChannel(channel); err != nil {
		t.Fatal(err)
	}
	writer := mustCreateSession(t, srv.sessions, 1, "writer", model.RoleUser)
	viewer := mustCreateSession(t, srv.sessions, 2, "viewer", model.RoleUser)
	server, client := net.Pipe()
	handler.setConn(viewer.ID, server)
	t.Cleanup(func() { handler.removeConn(viewer.ID); _ = client.Close() })
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// Selecting a text channel still works when history is disabled.
	handler.mu.Lock()
	handler.chatSelection[viewer.ID] = channel.ID
	handler.mu.Unlock()
	done := make(chan struct{})
	go func() {
		srv.handleChatMessage(handler, writer.ID, &pb.ChatMessage{ChannelID: channel.ID, Text: "live"}, st, &bufferConn{})
		close(done)
	}()
	event, err := protocol.ReadControlMessage(client)
	if err != nil || event.ChatEvent == nil || event.ChatEvent.ID != 0 || event.ChatEvent.Text != "live" {
		t.Fatalf("live = %#v, %v", event, err)
	}
	<-done
	rows, err := st.NonTx().ListMessages(model.MessageFilters{LimitToChannelID: &channel.ID})
	if err != nil || len(rows) != 0 {
		t.Fatalf("disabled storage = %#v, %v", rows, err)
	}
	legacy := &model.Message{ChannelID: channel.ID, SenderID: writer.UserID, SenderName: writer.Username, Body: "retained"}
	if err := st.NonTx().CreateMessage(legacy); err != nil {
		t.Fatal(err)
	}
	resp := &bufferConn{}
	srv.handleChatHistory(handler, viewer.ID, &pb.ChatHistoryRequest{ChannelID: channel.ID}, st, resp)
	if !bytes.Contains(resp.buffer.Bytes(), []byte(`"messages":[]`)) {
		t.Fatalf("disabled history wire = %s", resp.buffer.String())
	}
	result, err := protocol.ReadControlMessage(resp)
	if err != nil || result.ChatHistoryResp == nil || len(result.ChatHistoryResp.Messages) != 0 {
		t.Fatalf("disabled history = %#v, %v", result, err)
	}
	rows, err = st.NonTx().ListMessages(model.MessageFilters{LimitToChannelID: &channel.ID})
	if err != nil || len(rows) != 1 || rows[0].ID != legacy.ID {
		t.Fatalf("previously stored row changed = %#v, %v", rows, err)
	}
	admin, err := st.NonTx().CreateUser("admin", model.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	adminSession := mustCreateSession(t, srv.sessions, admin.ID, admin.Username, model.RoleAdmin)
	deleteReply := &bufferConn{}
	srv.handleMessage(handler, adminSession.ID, &pb.ControlMessage{ChatDeleteReq: &pb.ChatDeleteRequest{ChannelID: channel.ID, MessageID: legacy.ID}}, st, deleteReply)
	denied, err := protocol.ReadControlMessage(deleteReply)
	if err != nil || denied.ErrorResponse == nil {
		t.Fatalf("delete while disabled = %#v, %v", denied, err)
	}
	rows, err = st.NonTx().ListMessages(model.MessageFilters{LimitToChannelID: &channel.ID})
	if err != nil || len(rows) != 1 || rows[0].ID != legacy.ID {
		t.Fatalf("disabled delete changed row = %#v, %v", rows, err)
	}
}

func TestChatDeleteAuthorizationAndChannel(t *testing.T) {
	srv, st, handler := newTestServer(t)
	channel := model.NewChannel()
	if err := st.NonTx().CreateChannel(channel); err != nil {
		t.Fatal(err)
	}
	other := model.NewChannel()
	other.Name = "other"
	if err := st.NonTx().CreateChannel(other); err != nil {
		t.Fatal(err)
	}
	admin, err := st.NonTx().CreateUser("admin", model.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	moderator, err := st.NonTx().CreateUser("moderator", model.RoleModerator)
	if err != nil {
		t.Fatal(err)
	}
	user := mustCreateSession(t, srv.sessions, 999, "user", model.RoleUser)
	mod := mustCreateSession(t, srv.sessions, moderator.ID, moderator.Username, model.RoleModerator)
	owner := mustCreateSession(t, srv.sessions, admin.ID, admin.Username, model.RoleAdmin)
	scoped := mustCreateScopedSession(t, srv.sessions, 1000, "scoped", model.RoleModerator, other.ID)
	message := &model.Message{ChannelID: channel.ID, SenderID: admin.ID, SenderName: admin.Username, Body: "admin text"}
	if err := st.NonTx().CreateMessage(message); err != nil {
		t.Fatal(err)
	}
	try := func(sessionID uint32, channelID int64) *pb.ControlMessage {
		t.Helper()
		conn := &bufferConn{}
		srv.handleMessage(handler, sessionID, &pb.ControlMessage{ChatDeleteReq: &pb.ChatDeleteRequest{ChannelID: channelID, MessageID: message.ID}}, st, conn)
		got, err := protocol.ReadControlMessage(conn)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, tc := range []struct {
		sessionID uint32
		channelID int64
	}{{user.ID, channel.ID}, {mod.ID, other.ID}, {scoped.ID, channel.ID}} {
		if got := try(tc.sessionID, tc.channelID); got.ErrorResponse == nil {
			t.Fatalf("denial = %#v", got)
		}
		rows, err := st.NonTx().ListMessages(model.MessageFilters{LimitToChannelID: &channel.ID})
		if err != nil || len(rows) != 1 {
			t.Fatalf("denial deleted row = %#v, %v", rows, err)
		}
	}
	viewer := mustCreateSession(t, srv.sessions, 1001, "viewer", model.RoleUser)
	serverConn, clientConn := net.Pipe()
	handler.setConn(viewer.ID, serverConn)
	t.Cleanup(func() { handler.removeConn(viewer.ID); _ = clientConn.Close() })
	handler.mu.Lock()
	handler.chatSelection[viewer.ID] = channel.ID
	handler.mu.Unlock()
	legacy := mustCreateSession(t, srv.sessions, 1002, "legacy", model.RoleUser)
	srv.channels.Join(legacy.ID, channel.ID)
	legacyServer, legacyClient := net.Pipe()
	handler.setConn(legacy.ID, legacyServer)
	t.Cleanup(func() { handler.removeConn(legacy.ID); _ = legacyClient.Close() })
	if err := clientConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan *pb.ControlMessage, 1)
	go func() { done <- try(mod.ID, channel.ID) }()
	event, err := protocol.ReadControlMessage(clientConn)
	if err != nil || event.ChatDeleteEvent == nil || event.ChatDeleteEvent.MessageID != message.ID {
		t.Fatalf("subscriber deletion event = %#v, %v", event, err)
	}
	if got := <-done; got.ChatDeleteEvent == nil || got.ChatDeleteEvent.MessageID != message.ID {
		t.Fatalf("moderator delete admin message = %#v", got)
	}
	if err := legacyClient.SetReadDeadline(time.Now().Add(25 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if unexpected, err := protocol.ReadControlMessage(legacyClient); err == nil {
		t.Fatalf("legacy listener received incompatible event: %#v", unexpected)
	} else {
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("legacy listener read = %v, want timeout", err)
		}
	}
	if got := try(owner.ID, channel.ID); got.ErrorResponse == nil {
		t.Fatalf("duplicate delete = %#v", got)
	}
	rows, err := st.NonTx().ListMessages(model.MessageFilters{LimitToChannelID: &channel.ID})
	if err != nil || len(rows) != 0 {
		t.Fatalf("deleted row = %#v, %v", rows, err)
	}
	message = &model.Message{ChannelID: channel.ID, SenderID: moderator.ID, SenderName: moderator.Username, Body: "moderator text"}
	if err := st.NonTx().CreateMessage(message); err != nil {
		t.Fatal(err)
	}
	if err := st.NonTx().UpdateUserRole(moderator.ID, model.RoleUser); err != nil {
		t.Fatal(err)
	}
	if got := try(mod.ID, channel.ID); got.ErrorResponse == nil {
		t.Fatalf("durably demoted moderator deleted message = %#v", got)
	}
	// The watcher consumes the admin's deletion event so the broadcast can complete.
	done = make(chan *pb.ControlMessage, 1)
	go func() { done <- try(owner.ID, channel.ID) }()
	if _, err := protocol.ReadControlMessage(clientConn); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got.ChatDeleteEvent == nil || got.ChatDeleteEvent.MessageID != message.ID {
		t.Fatalf("admin delete = %#v", got)
	}
}
