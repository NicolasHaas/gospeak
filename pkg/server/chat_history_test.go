package server

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestChatHistoryAccessAndPersistence(t *testing.T) {
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
	session := mustCreateScopedSession(t, srv.sessions, 1, "reader", model.RoleUser, channel.ID)
	denied := &bufferConn{}
	srv.handleMessage(handler, session.ID, &pb.ControlMessage{ChatHistoryReq: &pb.ChatHistoryRequest{ChannelID: other.ID}}, st, denied)
	got, err := protocol.ReadControlMessage(denied)
	if err != nil || got.ErrorResponse == nil {
		t.Fatalf("foreign history = %#v, %v", got, err)
	}

	// A scoped account can write only to its channel without joining voice.
	srv.handleMessage(handler, session.ID, &pb.ControlMessage{ChatMsg: &pb.ChatMessage{ChannelID: other.ID, Text: "secret"}}, st, &bufferConn{})
	srv.handleMessage(handler, session.ID, &pb.ControlMessage{ChatMsg: &pb.ChatMessage{ID: 999, ChannelID: channel.ID, SenderID: 999, SenderName: "forged", Timestamp: 1, Text: "hello"}}, st, &bufferConn{})
	pageSize := int64(10)
	rows, err := st.NonTx().ListMessages(model.MessageFilters{LimitToChannelID: &channel.ID, PageSize: &pageSize})
	if err != nil || len(rows) != 1 || rows[0].Body != "hello" || rows[0].SenderName != "reader" || rows[0].SenderID != session.UserID || rows[0].ID == 999 {
		t.Fatalf("stored messages = %#v, %v", rows, err)
	}
	storedID := rows[0].ID
	rows, err = st.NonTx().ListMessages(model.MessageFilters{LimitToChannelID: &other.ID, PageSize: &pageSize})
	if err != nil || len(rows) != 0 {
		t.Fatalf("foreign messages = %#v, %v", rows, err)
	}

	response := &bufferConn{}
	srv.handleMessage(handler, session.ID, &pb.ControlMessage{ChatHistoryReq: &pb.ChatHistoryRequest{ChannelID: channel.ID, Limit: 10}}, st, response)
	got, err = protocol.ReadControlMessage(response)
	if err != nil || got.ChatHistoryResp == nil || len(got.ChatHistoryResp.Messages) != 1 || got.ChatHistoryResp.Messages[0].ID != storedID {
		t.Fatalf("history = %#v, %v", got, err)
	}
}

func TestChatHistoryCursorAndBounds(t *testing.T) {
	srv, st, handler := newTestServer(t)
	channel := model.NewChannel()
	if err := st.NonTx().CreateChannel(channel); err != nil {
		t.Fatal(err)
	}
	session := mustCreateSession(t, srv.sessions, 1, "reader", model.RoleUser)
	ids := make([]int64, 0, 3)
	for _, body := range []string{"first", "second", "third"} {
		message := &model.Message{ChannelID: channel.ID, SenderID: session.UserID, SenderName: session.Username, Body: body}
		if err := st.NonTx().CreateMessageWithRetention(message, 500, 0); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, message.ID)
	}
	read := func(before, limit int64) *pb.ControlMessage {
		t.Helper()
		conn := &bufferConn{}
		srv.handleChatHistory(handler, session.ID, &pb.ChatHistoryRequest{ChannelID: channel.ID, BeforeID: before, Limit: limit}, st, conn)
		result, err := protocol.ReadControlMessage(conn)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := read(0, 2).ChatHistoryResp
	if first == nil || !first.HasMore || len(first.Messages) != 2 || first.Messages[0].ID != ids[2] || first.Messages[1].ID != ids[1] {
		t.Fatalf("first page = %#v", first)
	}
	second := read(first.Messages[1].ID, 2).ChatHistoryResp
	if second == nil || second.HasMore || len(second.Messages) != 1 || second.Messages[0].ID != ids[0] {
		t.Fatalf("second page = %#v", second)
	}
	if got := read(-1, 2); got.ErrorResponse == nil {
		t.Fatalf("negative cursor = %#v", got)
	}
	if got := read(0, chatPageLimit+1); got.ErrorResponse == nil {
		t.Fatalf("oversize page = %#v", got)
	}
}

func TestChatHistoryHidesExpiredRowsBeforeSweep(t *testing.T) {
	srv, st, handler := newTestServer(t)
	channel := model.NewChannel()
	if err := st.NonTx().CreateChannel(channel); err != nil {
		t.Fatal(err)
	}
	session := mustCreateSession(t, srv.sessions, 1, "reader", model.RoleUser)
	msg := &model.Message{ChannelID: channel.ID, SenderID: session.UserID, SenderName: session.Username, Body: "expired"}
	if err := st.NonTx().CreateMessage(msg); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-60 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05.000000000")
	if _, err := st.(*datastore.ProviderFactory).DB.Exec("UPDATE messages SET created_at = ? WHERE id = ?", old, msg.ID); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	srv.handleChatHistory(handler, session.ID, &pb.ChatHistoryRequest{ChannelID: channel.ID}, st, conn)
	response, err := protocol.ReadControlMessage(conn)
	if err != nil || response.ChatHistoryResp == nil || len(response.ChatHistoryResp.Messages) != 0 {
		t.Fatalf("expired history = %#v, %v", response, err)
	}
}

type historyProbeFactory struct {
	datastore.DataProviderFactory
	beforeList func()
}

func (f historyProbeFactory) NonTx() datastore.DataStore {
	return historyProbeStore{DataStore: f.DataProviderFactory.NonTx(), beforeList: f.beforeList}
}

type historyProbeStore struct {
	datastore.DataStore
	beforeList func()
}

func (s historyProbeStore) ListMessages(filters model.MessageFilters) ([]model.Message, error) {
	s.beforeList()
	return s.DataStore.ListMessages(filters)
}

func TestChatSelectsBeforeHistoryQuery(t *testing.T) {
	srv, st, handler := newTestServer(t)
	channel := model.NewChannel()
	if err := st.NonTx().CreateChannel(channel); err != nil {
		t.Fatal(err)
	}
	session := mustCreateSession(t, srv.sessions, 1, "viewer", model.RoleUser)
	handler.setConn(session.ID, newCloseTrackingConn())
	t.Cleanup(func() { handler.removeConn(session.ID) })
	probe := historyProbeFactory{DataProviderFactory: st, beforeList: func() {
		handler.mu.RLock()
		selected := handler.chatSelection[session.ID]
		handler.mu.RUnlock()
		if selected != channel.ID {
			t.Fatalf("history queried before text selection: %d", selected)
		}
	}}
	conn := &bufferConn{}
	srv.handleChatHistory(handler, session.ID, &pb.ChatHistoryRequest{ChannelID: channel.ID}, probe, conn)
	if response, err := protocol.ReadControlMessage(conn); err != nil || response.ChatHistoryResp == nil {
		t.Fatalf("history = %#v, %v", response, err)
	}
}

func TestChatHistoryFrameBound(t *testing.T) {
	srv, st, handler := newTestServer(t)
	channel := model.NewChannel()
	if err := st.NonTx().CreateChannel(channel); err != nil {
		t.Fatal(err)
	}
	session := mustCreateSession(t, srv.sessions, 1, "reader", model.RoleUser)
	for i := 0; i < chatPageLimit; i++ {
		msg := &model.Message{ChannelID: channel.ID, SenderID: session.UserID, SenderName: session.Username, Body: strings.Repeat("<", model.MessageMaxBodyLength)}
		if err := st.NonTx().CreateMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	conn := &bufferConn{}
	srv.handleChatHistory(handler, session.ID, &pb.ChatHistoryRequest{ChannelID: channel.ID}, st, conn)
	if conn.buffer.Len() > protocol.MaxControlMessage+4 {
		t.Fatalf("frame exceeds limit: %d", conn.buffer.Len())
	}
	response, err := protocol.ReadControlMessage(conn)
	if err != nil || response.ChatHistoryResp == nil || len(response.ChatHistoryResp.Messages) != chatPageLimit {
		t.Fatalf("max-size history = %#v, %v", response, err)
	}
}

func TestChatHistoryCapsOversizedLegacyRows(t *testing.T) {
	srv, st, handler := newTestServer(t)
	channel := model.NewChannel()
	if err := st.NonTx().CreateChannel(channel); err != nil {
		t.Fatal(err)
	}
	session := mustCreateSession(t, srv.sessions, 1, "reader", model.RoleUser)
	msg := &model.Message{ChannelID: channel.ID, SenderID: session.UserID, SenderName: session.Username, Body: "valid"}
	if err := st.NonTx().CreateMessage(msg); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("<", 100000)
	if _, err := st.(*datastore.ProviderFactory).DB.Exec("UPDATE messages SET body = ?, sender_name = ? WHERE id = ?", big, big, msg.ID); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	srv.handleChatHistory(handler, session.ID, &pb.ChatHistoryRequest{ChannelID: channel.ID}, st, conn)
	response, err := protocol.ReadControlMessage(conn)
	if err != nil || response.ChatHistoryResp == nil || len(response.ChatHistoryResp.Messages) != 1 {
		t.Fatalf("legacy history = %#v, %v", response, err)
	}
	got := response.ChatHistoryResp.Messages[0]
	if len(got.Text) != model.MessageMaxBodyLength || len(got.SenderName) != model.MaxUsernameLength {
		t.Fatalf("legacy wire lengths = %d / %d", len(got.Text), len(got.SenderName))
	}
	var stored string
	if err := st.(*datastore.ProviderFactory).DB.QueryRow("SELECT body FROM messages WHERE id = ?", msg.ID).Scan(&stored); err != nil || stored != big {
		t.Fatalf("stored legacy row changed: len=%d, err=%v", len(stored), err)
	}
}

func TestChatSelectionDoesNotJoinVoice(t *testing.T) {
	srv, st, handler := newTestServer(t)
	channel := model.NewChannel()
	if err := st.NonTx().CreateChannel(channel); err != nil {
		t.Fatal(err)
	}
	viewer := mustCreateSession(t, srv.sessions, 1, "viewer", model.RoleUser)
	writer := mustCreateSession(t, srv.sessions, 2, "writer", model.RoleUser)
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { handler.removeConn(viewer.ID); _ = clientConn.Close() })
	handler.setConn(viewer.ID, serverConn)
	resp := &bufferConn{}
	srv.handleChatHistory(handler, viewer.ID, &pb.ChatHistoryRequest{ChannelID: channel.ID}, st, resp)
	if got := srv.channels.ChannelOf(viewer.ID); got != 0 {
		t.Fatalf("text selection joined voice channel %d", got)
	}
	if err := clientConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		srv.handleChatMessage(handler, writer.ID, &pb.ChatMessage{ChannelID: channel.ID, Text: "event"}, st, &bufferConn{})
		close(done)
	}()
	event, err := protocol.ReadControlMessage(clientConn)
	if err != nil || event.ChatEvent == nil || event.ChatEvent.Text != "event" || event.ChatEvent.ID == 0 {
		t.Fatalf("text-only event = %#v, %v", event, err)
	}
	<-done
	handler.removeConn(viewer.ID)
	if _, selected := handler.chatSelection[viewer.ID]; selected {
		t.Fatal("chat subscription survived disconnect")
	}
}
