package server

import (
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestControlLocalDefaultParity(t *testing.T) {
	for _, cfg := range []Config{DefaultConfig(), {}, {ControlMessageBurst: -1, ControlMessagesPerSec: -1}} {
		srv := New(cfg, Dependencies{})
		if srv.cfg.ControlMessageBurst != 60 || srv.cfg.ControlMessagesPerSec != 10 ||
			srv.controlUserBudgets.capacity != 60 || srv.controlUserBudgets.refill != 10 {
			t.Errorf("local defaults = %d/%d, account = %d/%d; want 60/10", srv.cfg.ControlMessageBurst, srv.cfg.ControlMessagesPerSec, srv.controlUserBudgets.capacity, srv.controlUserBudgets.refill)
		}
		srv.cancel()
	}
	cfg := DefaultConfig()
	cfg.ControlMessageBurst, cfg.ControlMessagesPerSec = 73, 17
	srv := New(cfg, Dependencies{})
	defer srv.cancel()
	if srv.cfg.ControlMessageBurst != 73 || srv.cfg.ControlMessagesPerSec != 17 || srv.controlUserBudgets.refill != 17 {
		t.Fatal("explicit local configuration was overwritten")
	}
}

func TestControlLocalStartupAndEightSessionPacing(t *testing.T) {
	cfg := DefaultConfig()
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	manager := newControlUserBudgetManager(cfg.ControlMessageBurst, cfg.ControlMessagesPerSec, clock)
	join := &pb.ControlMessage{JoinChannelRequest: &pb.JoinChannelRequest{ChannelID: 1}}
	history := &pb.ControlMessage{ChatHistoryReq: &pb.ChatHistoryRequest{ChannelID: 1}}
	state := &pb.ControlMessage{UserStateUpdate: &pb.UserStateUpdate{Muted: true}}
	ping := &pb.ControlMessage{Ping: &pb.Ping{}}
	chat := &pb.ControlMessage{ChatMsg: &pb.ChatMessage{ChannelID: 1, Text: "hello"}}
	// A single client auto-joins from AuthResponse's channel list; UI text
	// selection requests history. Include mute/deafen and an early heartbeat.
	startup := newControlMessageLimiter(cfg.ControlMessageBurst, cfg.ControlMessagesPerSec, clock)
	for _, msg := range []*pb.ControlMessage{join, history, state, state, ping, chat} {
		if !startup.Allow(msg).Allowed {
			t.Fatal("ordinary single-session startup exhausted local burst")
		}
	}
	var accounts [8]*controlMessageLimiter
	var sessions [8]*controlMessageLimiter
	for i := range accounts {
		var ok bool
		accounts[i], _, _, ok = manager.Acquire(1)
		if !ok || accounts[i] != accounts[0] {
			t.Fatal("sessions did not share one account budget")
		}
		sessions[i] = newControlMessageLimiter(cfg.ControlMessageBurst, cfg.ControlMessagesPerSec, clock)
	}
	// Eight simultaneous auto-joins cost 40; no artificial per-session account burst.
	var wg sync.WaitGroup
	for i := range accounts {
		wg.Go(func() {
			if !accounts[i].Allow(join).Allowed || !sessions[i].Allow(join).Allowed {
				t.Error("eight concurrent auto-joins exceeded retained burst")
			}
		})
	}
	wg.Wait()
	// Text selection/history costs another 40. Spread selections over two
	// seconds, then each session's chat+state+Ping (8 points) over one second.
	for i := range accounts {
		now = now.Add(250 * time.Millisecond)
		if !accounts[i].Allow(history).Allowed || !sessions[i].Allow(history).Allowed {
			t.Fatal("paced eight-session history selection exhausted local allowance")
		}
	}
	for round := 0; round < 3; round++ {
		for i := range accounts {
			now = now.Add(time.Second)
			for _, msg := range []*pb.ControlMessage{chat, state, ping} {
				if !accounts[i].Allow(msg).Allowed || !sessions[i].Allow(msg).Allowed {
					t.Fatal("paced multi-session activity exhausted local allowance")
				}
			}
		}
	}
	for range accounts {
		manager.Release(1)
	}
}

func TestControlLocalTightenedRefillBoundary(t *testing.T) {
	cfg := DefaultConfig()
	now := time.Unix(1700000000, 0)
	manager := newControlUserBudgetManager(cfg.ControlMessageBurst, cfg.ControlMessagesPerSec, func() time.Time { return now })
	account, _, _, _ := manager.Acquire(1)
	join := &pb.ControlMessage{JoinChannelRequest: &pb.JoinChannelRequest{}}
	for range 12 {
		if !account.Allow(join).Allowed {
			t.Fatal("burst was reduced")
		}
	}
	manager.Release(1)
	accountAgain, _, _, _ := manager.Acquire(1)
	if accountAgain != account || accountAgain.Allow(join).Allowed {
		t.Fatal("reconnect restored exhausted account burst")
	}
	now = now.Add(499 * time.Millisecond)
	if account.Allow(join).Allowed {
		t.Fatal("cost-five operation admitted before 500ms refill")
	}
	now = now.Add(time.Millisecond)
	if !account.Allow(join).Allowed || account.Allow(join).Allowed {
		t.Fatal("exact 500ms refill did not admit precisely one cost-five operation")
	}
	manager.Release(1)
}

func TestControlLocalDefaultWireStartupAndTerminal(t *testing.T) {
	srv, st, handler := newTestServer(t)
	srv.cfg.AllowNoToken = true
	// Explicit publication, as in the state tests, avoids timer-based assertions.
	handler.statePublisher.Do(func() {})
	now := time.Unix(1700000000, 0)
	srv.controlUserBudgets.now = func() time.Time { return now }
	channel := model.NewChannel()
	if err := st.NonTx().CreateChannel(channel); err != nil {
		t.Fatal(err)
	}
	server, peer := newControlPipe()
	done := make(chan struct{})
	go func() { defer close(done); srv.handleControlConn(handler, server, st) }()
	t.Cleanup(func() {
		_ = peer.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("control fixture did not exit")
		}
	})
	if err := peer.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	send := func(msg *pb.ControlMessage) *pb.ControlMessage {
		t.Helper()
		if err := protocol.WriteControlMessage(peer, msg); err != nil {
			t.Fatal(err)
		}
		response, err := protocol.ReadControlMessage(peer)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	auth := send(&pb.ControlMessage{AuthRequest: &pb.AuthRequest{Username: "tuned-startup", MediaCiphers: []string{"aes128"}}})
	if auth.AuthResponse == nil {
		t.Fatalf("authentication: %#v", auth)
	}
	ping := &pb.ControlMessage{Ping: &pb.Ping{}}
	if response := send(ping); response.Pong == nil {
		t.Fatal("initial Ping failed")
	}
	// Pong is a barrier after installation. Freeze time, not refill parameters,
	// and inspect the real per-connection limiter built by handleControlConn.
	srv.controlBudgetMu.RLock()
	local := srv.controlBudgets[auth.AuthResponse.SessionID]
	srv.controlBudgetMu.RUnlock()
	local.mu.Lock()
	local.now, local.updated = func() time.Time { return now }, now
	if local.capacity != 60 || local.refill != 10 {
		t.Errorf("wire local = %d/%g, want 60/10", local.capacity, local.refill)
	}
	local.mu.Unlock()
	if response := send(&pb.ControlMessage{JoinChannelRequest: &pb.JoinChannelRequest{ChannelID: channel.ID}}); response.ChannelJoinResponse == nil || !response.ChannelJoinResponse.Success {
		t.Fatal("startup auto-join failed")
	}
	if response := send(&pb.ControlMessage{ChatHistoryReq: &pb.ChatHistoryRequest{ChannelID: channel.ID, Limit: 40}}); response.ChatHistoryResp == nil {
		t.Fatal("startup text selection/history failed")
	}
	// State has no direct ACK; the following Pong fences it on the same stream.
	if err := protocol.WriteControlMessage(peer, &pb.ControlMessage{UserStateUpdate: &pb.UserStateUpdate{Muted: true}}); err != nil {
		t.Fatal(err)
	}
	if response := send(ping); response.Pong == nil {
		t.Fatal("startup state/Ping failed")
	}
	snapshot, ok := srv.sessions.GetSnapshot(auth.AuthResponse.SessionID)
	if !ok || !snapshot.Muted {
		t.Fatal("startup mute was not applied")
	}
	// Startup spent 17. Invalid joins still cost five before handler validation.
	invalid := &pb.ControlMessage{JoinChannelRequest: &pb.JoinChannelRequest{ChannelID: -1}}
	for range 8 {
		if response := send(invalid); response.ChannelJoinResponse == nil || response.ChannelJoinResponse.Success {
			t.Fatal("invalid join was not charged/validated")
		}
	}
	for range 3 {
		if response := send(ping); response.Pong == nil {
			t.Fatal("retained burst exhausted early")
		}
	}
	globalUsage, reserveUsage := srv.controlGlobalBudget.usage(), srv.controlInteractivePool.usage()
	if response := send(invalid); response.ErrorResponse == nil || response.ErrorResponse.Code != 8 {
		t.Fatal("account exhaustion did not return code 8")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("account exhaustion was not terminal")
	}
	if srv.metrics.ControlUserExpensiveRejections.Load() != 1 || srv.metrics.ControlGlobalExpensiveRejections.Load() != 0 {
		t.Fatal("local terminal refusal was misclassified")
	}
	if srv.controlGlobalBudget.usage() > globalUsage || srv.controlInteractivePool.usage() > reserveUsage {
		t.Fatal("local terminal refusal consumed global credit")
	}
}
