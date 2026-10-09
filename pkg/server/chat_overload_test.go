package server

import (
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestChatHistoryGlobalRefusalRefillAndRetry(t *testing.T) {
	srv, st, handler := newTestServer(t)
	srv.cfg.AllowNoToken = true
	srv.controlGlobalBudget = newControlMessageLimiter(5, 1, func() time.Time { return time.Unix(1700000000, 0) })
	channel := model.NewChannel()
	channel.Name = "overload-chat"
	if err := st.NonTx().CreateChannel(channel); err != nil {
		t.Fatal(err)
	}
	seed, err := st.NonTx().CreateUser("seed", model.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"older", "newer"} {
		if err := st.NonTx().CreateMessageWithRetention(&model.Message{ChannelID: channel.ID, SenderID: seed.ID, SenderName: "seed", Body: text}, 500, 0); err != nil {
			t.Fatal(err)
		}
	}
	c := connectPublicationClient(t, srv, handler, st)
	auth, err := c.Authenticate("", "overload-viewer")
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan *pb.ControlMessage, 8)
	c.SetEventHandler(func(m *pb.ControlMessage) { events <- m })
	c.StartReceiving()
	read := func() *pb.ControlMessage {
		t.Helper()
		select {
		case m := <-events:
			return m
		case <-time.After(time.Second):
			t.Fatal("no control response")
			return nil
		}
	}
	budget := func(tokens float64) {
		srv.controlGlobalBudget.mu.Lock()
		srv.controlGlobalBudget.tokens = tokens
		srv.controlGlobalBudget.mu.Unlock()
	}
	request := &pb.ControlMessage{ChatHistoryReq: &pb.ChatHistoryRequest{ChannelID: channel.ID, Limit: 1}}
	for _, earlier := range []bool{false, true} {
		budget(0)
		if err := c.Send(request); err != nil {
			t.Fatal(err)
		}
		refused := read()
		if refused.ErrorResponse == nil || refused.ErrorResponse.Code != 8 {
			t.Fatalf("not refused: %#v", refused)
		}
		handler.mu.RLock()
		selection := handler.chatSelection[auth.SessionID]
		handler.mu.RUnlock()
		if !earlier && selection != 0 {
			t.Fatal("refused request installed a selection")
		}
		budget(5)
		if err := c.Send(request); err != nil {
			t.Fatal(err)
		}
		accepted := read()
		if accepted.ChatHistoryResp == nil || len(accepted.ChatHistoryResp.Messages) != 1 {
			t.Fatalf("retry failed: %#v", accepted)
		}
		page := accepted.ChatHistoryResp
		if earlier && page.Messages[0].ID >= request.ChatHistoryReq.BeforeID {
			t.Fatal("earlier retry lost its cursor")
		}
		handler.mu.RLock()
		selection = handler.chatSelection[auth.SessionID]
		handler.mu.RUnlock()
		if selection != channel.ID {
			t.Fatal("accepted retry did not install live selection")
		}
		request.ChatHistoryReq.BeforeID = page.Messages[0].ID
	}
	// The accepted selection must actually receive live fanout, independently of voice.
	srv.handleChatMessage(handler, auth.SessionID, &pb.ChatMessage{ChannelID: channel.ID, Text: "live after retry"}, st, &bufferConn{})
	live := read()
	if live.ChatEvent == nil || live.ChatEvent.Text != "live after retry" {
		t.Fatalf("selection did not recover live fanout: %#v", live)
	}
}
