package client

import (
	"bytes"
	"math"
	"net"
	"sync"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestHistoryAttemptMatchingAndGenerationReset(t *testing.T) {
	e := NewEngine()
	connect := func() *connectionGeneration {
		g := newConnectionGeneration()
		g.control = &ControlClient{conn: &recordingConn{}}
		e.generation = g
		e.state = StateConnecting
		if !e.publishConnectedGeneration(g, &pb.AuthResponse{HistoryRequestIDs: true}) {
			t.Fatal("publish connection failed")
		}
		return g
	}
	g := connect()
	// Keep callbacks queued to force adoption after a superseding request/reconnect.
	e.callbackQueueRunning = true
	var delivered []pb.ChatHistoryResponse
	e.OnChatHistory = func(r pb.ChatHistoryResponse) { delivered = append(delivered, r) }
	first, err := e.LoadChatHistory(9, 100)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.LoadChatHistory(9, 100)
	if err != nil || second <= first {
		t.Fatal("same-page retry reused ID")
	}
	for _, response := range []pb.ChatHistoryResponse{
		{RequestID: first, ChannelID: 9}, {RequestID: 0, ChannelID: 9}, {RequestID: second + 1, ChannelID: 9}, {RequestID: second, ChannelID: 8}, {RequestID: -1, ChannelID: 9},
	} {
		e.handleEventGeneration(g, &pb.ControlMessage{ChatHistoryResp: &response})
	}
	if len(e.callbackQueue) != 0 || g.historyRequestID != second {
		t.Fatal("unmatched reply consumed request or queued callback")
	}
	e.CancelChatHistory(first)
	if g.historyRequestID != second {
		t.Fatal("old cancellation cleared newer attempt")
	}
	response := &pb.ChatHistoryResponse{RequestID: second, ChannelID: 9, Messages: []pb.ChatMessage{{ID: 80}}}
	e.handleEventGeneration(g, &pb.ControlMessage{ChatHistoryResp: response})
	e.handleEventGeneration(g, &pb.ControlMessage{ChatHistoryResp: response})
	if len(e.callbackQueue) != 1 || g.historyRequestID != 0 {
		t.Fatal("completion not one-shot")
	}
	third, err := e.LoadChatHistory(9, 100)
	if err != nil {
		t.Fatal(err)
	}
	e.CancelChatHistory(third)
	e.handleEventGeneration(g, &pb.ControlMessage{ChatHistoryResp: &pb.ChatHistoryResponse{RequestID: third, ChannelID: 9}})
	if len(e.callbackQueue) != 1 || g.historyRequestID != 0 {
		t.Fatal("timeout retained pending reply")
	}
	g.closeResources()
	g = connect()
	fourth, err := e.LoadChatHistory(9, 100)
	if err != nil || fourth <= third {
		t.Fatal("reconnect reused identity")
	}
	e.handleEventGeneration(g, &pb.ControlMessage{ChatHistoryResp: response})
	if len(e.callbackQueue) != 1 {
		t.Fatal("pre-reconnect ID matched new generation")
	}
	e.runCallbackQueue()
	if len(delivered) != 1 || delivered[0].RequestID != second {
		t.Fatal("queued response lost its genuine identity")
	}
	e.CancelChatHistory(fourth)
	e.nextHistoryRequestID = math.MaxInt64
	if _, err := e.LoadChatHistory(9, 0); err == nil || g.historyRequestID != 0 {
		t.Fatal("request IDs wrapped")
	}
	g.closeResources()
}

func TestHistoryConcurrentAttemptsFollowWireOrder(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	conn := &recordingConn{}
	g.control = &ControlClient{conn: conn}
	g.historyRequestIDs = true
	e.generation = g
	e.state = StateConnected
	t.Cleanup(g.closeResources)
	const attempts = 64
	for range 32 {
		conn.b.Reset()
		start := make(chan struct{})
		errs := make(chan error, attempts)
		var wg sync.WaitGroup
		for channel := range attempts {
			wg.Go(func() {
				<-start
				_, err := e.LoadChatHistory(int64(channel+1), 0)
				errs <- err
			})
		}
		close(start)
		wg.Wait()
		for range attempts {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		var last pb.ChatHistoryRequest
		for range attempts {
			msg, err := protocol.ReadControlMessage(&conn.b)
			if err != nil {
				t.Fatal(err)
			}
			if msg.ChatHistoryReq == nil || msg.ChatHistoryReq.RequestID <= last.RequestID {
				t.Fatal("history attempts were sent out of identity order")
			}
			last = *msg.ChatHistoryReq
		}
		if g.historyRequestID != last.RequestID || g.historyChannelID != last.ChannelID {
			t.Fatal("pending history attempt differs from final wire request")
		}
	}
}

func TestHistoryLegacyWireAndSendFailure(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	conn := &recordingConn{}
	g.control = &ControlClient{conn: conn}
	e.generation = g
	e.state = StateConnecting
	if !e.publishConnectedGeneration(g, &pb.AuthResponse{}) {
		t.Fatal("legacy connection failed")
	}
	id, err := e.LoadChatHistory(9, 0)
	if err != nil || id != 0 {
		t.Fatal("legacy request not available")
	}
	raw := conn.b.Bytes()
	if bytes.Contains(raw, []byte("request_id")) {
		t.Fatal("new field sent to legacy server")
	}
	if _, err := protocol.ReadControlMessage(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	e.callbackQueueRunning = true
	called := false
	e.OnChatHistory = func(pb.ChatHistoryResponse) { called = true }
	e.handleEventGeneration(g, &pb.ControlMessage{ChatHistoryResp: &pb.ChatHistoryResponse{ChannelID: 9}})
	e.runCallbackQueue()
	if !called {
		t.Fatal("legacy history suppressed")
	}
	g.closeResources()
	closed, peer := net.Pipe()
	_ = peer.Close()
	g = newConnectionGeneration()
	g.control = &ControlClient{conn: closed}
	g.historyRequestIDs = true
	e.generation = g
	if _, err := e.LoadChatHistory(9, 0); err == nil || g.historyRequestID != 0 {
		t.Fatal("failed send retained attempt")
	}
	g.closeResources()
}
