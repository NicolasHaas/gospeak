package client

import (
	"bytes"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestTextChatControlsAndEventsWithoutVoice(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	conn := &recordingConn{}
	g.control = &ControlClient{conn: conn}
	e.mu.Lock()
	e.generation = g
	e.state = StateConnected
	e.channelID = 0
	e.mu.Unlock()
	if err := e.LoadChatHistory(9, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.SendChat(9, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteChatMessage(9, 12); err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(conn.b.Bytes())
	for i, check := range []func(*pb.ControlMessage) bool{
		func(m *pb.ControlMessage) bool { return m.ChatHistoryReq != nil && m.ChatHistoryReq.ChannelID == 9 },
		func(m *pb.ControlMessage) bool {
			return m.ChatMsg != nil && m.ChatMsg.ChannelID == 9 && m.ChatMsg.Text == "hello"
		},
		func(m *pb.ControlMessage) bool {
			return m.ChatDeleteReq != nil && m.ChatDeleteReq.ChannelID == 9 && m.ChatDeleteReq.MessageID == 12
		},
	} {
		message, err := protocol.ReadControlMessage(reader)
		if err != nil {
			t.Fatal(err)
		}
		if !check(message) {
			t.Fatalf("control message %d: %+v", i, message)
		}
	}
	messages := make(chan pb.ChatMessage, 1)
	history := make(chan pb.ChatHistoryResponse, 1)
	deletions := make(chan pb.ChatDeleteEvent, 1)
	e.OnChatMessage = func(m pb.ChatMessage) { messages <- m }
	e.OnChatHistory = func(r pb.ChatHistoryResponse) { history <- r }
	e.OnChatDeleted = func(d pb.ChatDeleteEvent) { deletions <- d }
	e.handleEventGeneration(g, &pb.ControlMessage{ChatEvent: &pb.ChatMessage{ID: 12, ChannelID: 9}})
	e.handleEventGeneration(g, &pb.ControlMessage{ChatHistoryResp: &pb.ChatHistoryResponse{ChannelID: 9, Messages: []pb.ChatMessage{{ID: 12}}}})
	e.handleEventGeneration(g, &pb.ControlMessage{ChatDeleteEvent: &pb.ChatDeleteEvent{ChannelID: 9, MessageID: 12}})
	select {
	case m := <-messages:
		if m.ID != 12 || m.ChannelID != 9 {
			t.Fatalf("live: %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("missing live callback")
	}
	select {
	case r := <-history:
		if r.ChannelID != 9 || len(r.Messages) != 1 || r.Messages[0].ID != 12 {
			t.Fatalf("history: %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("missing history callback")
	}
	select {
	case d := <-deletions:
		if d.ChannelID != 9 || d.MessageID != 12 {
			t.Fatalf("deletion: %+v", d)
		}
	case <-time.After(time.Second):
		t.Fatal("missing deletion callback")
	}
}

func TestChatQueueSaturationDisconnectsInsteadOfDroppingDeletion(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.mu.Lock()
	e.generation = g
	e.state = StateConnected
	e.mu.Unlock()
	e.OnChatDeleted = func(pb.ChatDeleteEvent) { t.Error("saturated chat callback ran") }
	e.callbackQueueMu.Lock()
	e.callbackQueue = make([]func(), maxReliableCallbackQueue)
	e.callbackQueueRunning = true
	e.callbackQueueMu.Unlock()
	e.handleEventGeneration(g, &pb.ControlMessage{ChatDeleteEvent: &pb.ChatDeleteEvent{ChannelID: 9, MessageID: 12}})
	select {
	case <-g.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("full callback queue left the client connected with stale chat")
	}
	select {
	case <-g.done:
	case <-time.After(time.Second):
		t.Fatal("chat-overload disconnect did not complete")
	}
}
