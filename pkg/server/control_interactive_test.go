package server

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

// Reuses the review diagnostic's account -> session -> global fake-clock gate.
// Unlike the unselected account-fairness proposal, bulk may now be refused.
func TestInteractiveControlsSurviveBulkPressure(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  *pb.ControlMessage
	}{
		{"join", &pb.ControlMessage{JoinChannelRequest: &pb.JoinChannelRequest{}}},
		{"chat", &pb.ControlMessage{ChatMsg: &pb.ChatMessage{}}},
		{"mute", &pb.ControlMessage{UserStateUpdate: &pb.UserStateUpdate{Muted: true}}},
		{"deafen", &pb.ControlMessage{UserStateUpdate: &pb.UserStateUpdate{Deafened: true}}},
		{"leave", &pb.ControlMessage{LeaveChannelRequest: &pb.LeaveChannelRequest{}}},
		{"ping", &pb.ControlMessage{Ping: &pb.Ping{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1700000000, 0)
			clock := func() time.Time { return now }
			srv := New(DefaultConfig(), Dependencies{})
			defer srv.cancel()
			srv.controlGlobalBudget.now = clock
			srv.controlGlobalBudget.updated = now
			list := &pb.ControlMessage{ChannelListRequest: &pb.ChannelListRequest{}}
			admitted := 0
			for i := 0; i < 5; i++ {
				account := newControlMessageLimiter(60, 20, clock)
				session := newControlMessageLimiter(60, 20, clock)
				for j := 0; j < 12; j++ {
					if !account.Allow(list).Allowed || !session.Allow(list).Allowed {
						t.Fatal("diagnostic exceeded local budgets")
					}
					if srv.allowGlobalControl(0, list, 0).Allowed {
						admitted++
					}
				}
			}
			if admitted == 0 {
				t.Fatal("bulk functionality lost")
			}
			account := newControlMessageLimiter(60, 20, clock)
			session := newControlMessageLimiter(60, 20, clock)
			if !account.Allow(tc.msg).Allowed || !session.Allow(tc.msg).Allowed {
				t.Fatal("protected request exceeded local budget")
			}
			if !srv.allowGlobalControl(0, tc.msg, 0).Allowed {
				t.Fatal("bulk pressure denied protected request after local gates admitted")
			}
		})
	}
}

func TestInteractivePartitionConfiguredCeiling(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	largeReserve := maxInt / 5
	if int64(maxInt) > 1<<53 {
		largeReserve = 0
	}
	for _, tc := range []struct{ burst, refill, reserveBurst, reserveRefill int }{
		{300, 100, 60, 20}, {31, 11, 6, 2}, {25, 5, 5, 1},
		{24, 4, 5, 1}, {10, 2, 5, 1}, {9, 100, 0, 0}, {5, 1, 0, 0},
		{300, 1, 0, 0}, {1, 1, 0, 0}, {0, 0, 60, 20}, {-1, -1, 60, 20},
		{maxInt, maxInt, largeReserve, largeReserve},
	} {
		t.Run(fmt.Sprintf("%d/%d", tc.burst, tc.refill), func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.ControlGlobalBurst, cfg.ControlGlobalMessagesPerSec = tc.burst, tc.refill
			srv := New(cfg, Dependencies{})
			defer srv.cancel()
			shared, reserve := srv.controlGlobalBudget, srv.controlInteractivePool
			if reserve.capacity != tc.reserveBurst || reserve.refill != float64(tc.reserveRefill) {
				t.Fatalf("reserve = %d/%g", reserve.capacity, reserve.refill)
			}
			if shared.capacity != srv.cfg.ControlGlobalBurst-reserve.capacity ||
				shared.refill != float64(srv.cfg.ControlGlobalMessagesPerSec-tc.reserveRefill) {
				t.Fatal("partition changed configured total")
			}
			if shared.tokens+reserve.tokens > float64(srv.cfg.ControlGlobalBurst) ||
				shared.refill+reserve.refill > float64(srv.cfg.ControlGlobalMessagesPerSec) {
				t.Fatal("float rounding expanded the old ordinary envelope")
			}
			if shared.capacity < 5 || shared.refill <= 0 || reserve.capacity != 0 && reserve.capacity < 5 {
				t.Fatal("partition disabled cost-five operations")
			}
			if !shared.Allow(&pb.ControlMessage{ChannelListRequest: &pb.ChannelListRequest{}}).Allowed {
				t.Fatal("custom budget cannot admit ordinary cost-five request")
			}
			if srv.controlModerationBudget.capacity != 20 || srv.controlModerationBudget.refill != 5 ||
				srv.controlUserBudgets.maxEntries != 4096 || srv.cfg.MaxSessions != 1024 {
				t.Fatal("unrelated capacity changed")
			}
		})
	}
}

func TestInteractiveClassExhaustionRefillAndBytes(t *testing.T) {
	now := time.Unix(1700000000, 0)
	srv := New(DefaultConfig(), Dependencies{})
	defer srv.cancel()
	srv.controlGlobalBudget, srv.controlInteractivePool = newGlobalControlBudgets(300, 100, func() time.Time { return now })
	ping := &pb.ControlMessage{Ping: &pb.Ping{}}
	list := &pb.ControlMessage{ChannelListRequest: &pb.ChannelListRequest{}}
	// Protected traffic borrows shared first, without touching reserve.
	for i := 0; i < 240; i++ {
		if !srv.allowGlobalControl(1, ping, 0).Allowed || srv.controlInteractivePool.usage() != 0 {
			t.Fatal("shared-first burst policy changed")
		}
	}
	for _, msg := range []*pb.ControlMessage{
		list, {ChatHistoryReq: &pb.ChatHistoryRequest{}}, {CreateChannelReq: &pb.CreateChannelRequest{}},
		{ListInvitesReq: &pb.ListInvitesRequest{}}, {ScreenShareStartReq: &pb.ScreenShareStartRequest{}},
		{KickUserReq: &pb.KickUserRequest{}}, {BanUserReq: &pb.BanUserRequest{}},
	} {
		if srv.allowGlobalControl(0, msg, 0).Allowed || srv.controlInteractivePool.usage() != 0 {
			t.Fatal("unprotected request spent reserve")
		}
	}
	// One byte above 16 KiB pays two, not Ping's semantic one.
	if decision := srv.allowGlobalControl(2, ping, controlBytesPerCost+1); !decision.Allowed || decision.Cost != 2 {
		t.Fatalf("byte surcharge lost: %#v", decision)
	}
	// Invalid protected content pays before handler validation; max cost, not sum.
	join := &pb.ControlMessage{JoinChannelRequest: &pb.JoinChannelRequest{ChannelID: -1}}
	if decision := srv.allowGlobalControl(2, join, 5*controlBytesPerCost); !decision.Allowed || decision.Cost != 5 {
		t.Fatalf("invalid join byte boundary: %#v", decision)
	}
	if srv.allowGlobalControl(2, ping, 61*controlBytesPerCost).Allowed || srv.controlInteractivePool.usage() != 7 {
		t.Fatal("oversized refusal spent or bypassed reserve")
	}
	if !srv.allowGlobalControl(2, ping, 53*controlBytesPerCost).Allowed {
		t.Fatal("exact remaining reserve boundary refused")
	}
	// Different IDs share the class: the owner did not select account fairness.
	if srv.allowGlobalControl(99, ping, 0).Allowed || srv.allowGlobalControl(99, list, 0).Allowed {
		t.Fatal("collectively exhausted class admitted work")
	}
	now = now.Add(250*time.Millisecond - time.Nanosecond)
	for i := 0; i < 3; i++ {
		if !srv.allowGlobalControl(99, list, 0).Allowed {
			t.Fatal("shared refill lost")
		}
	}
	if srv.allowGlobalControl(99, join, 0).Allowed {
		t.Fatal("cost-five refill admitted early")
	}
	now = now.Add(time.Nanosecond)
	if !srv.allowGlobalControl(99, list, 0).Allowed || !srv.allowGlobalControl(99, join, 0).Allowed ||
		srv.allowGlobalControl(100, ping, 0).Allowed {
		t.Fatal("250ms refill must admit exactly 20 shared plus 5 protected cost")
	}
	if srv.controlModerationBudget.usage() != 0 {
		t.Fatal("interactive used moderation")
	}
}

func TestInteractiveWireChargingAndFinalRejections(t *testing.T) {
	srv, st, handler := newTestServer(t)
	srv.cfg.AllowNoToken = true
	var clock atomic.Int64
	clock.Store(time.Unix(1700000000, 0).UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	srv.controlGlobalBudget = newControlMessageLimiter(5, 0, now)
	srv.controlGlobalBudget.tokens = 0
	srv.controlInteractivePool = newControlMessageLimiter(8, 5, now)
	srv.controlUserBudgets = newControlUserBudgetManager(60, 0, now)
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
	auth := send(&pb.ControlMessage{AuthRequest: &pb.AuthRequest{Username: "interactive-wire", MediaCiphers: []string{"aes128"}}})
	if auth.AuthResponse == nil {
		t.Fatalf("authentication: %#v", auth)
	}
	if response := send(&pb.ControlMessage{Ping: &pb.Ping{}}); response.Pong == nil {
		t.Fatal("fallback Ping failed")
	}
	// Pong is a barrier: both local limiters are installed, so freeze session refill.
	srv.controlBudgetMu.RLock()
	local := srv.controlBudgets[auth.AuthResponse.SessionID]
	srv.controlBudgetMu.RUnlock()
	local.mu.Lock()
	local.refill = 0
	local.mu.Unlock()
	snapshot, ok := srv.sessions.GetSnapshot(auth.AuthResponse.SessionID)
	if !ok {
		t.Fatal("authenticated session missing")
	}
	account, _, _, ok := srv.controlUserBudgets.Acquire(snapshot.UserID)
	if !ok {
		t.Fatal("account budget missing")
	}
	defer srv.controlUserBudgets.Release(snapshot.UserID)
	join := &pb.ControlMessage{JoinChannelRequest: &pb.JoinChannelRequest{ChannelID: -1}}
	if response := send(join); response.ChannelJoinResponse == nil || response.ChannelJoinResponse.Success {
		t.Fatalf("invalid protected join bypassed handler: %#v", response)
	}
	// A valid padded envelope crosses the 16KiB byte-cost boundary on the wire.
	payload := `{"ping":{}}` + strings.Repeat(" ", controlBytesPerCost+1-len(`{"ping":{}}`))
	if err := binary.Write(peer, binary.BigEndian, uint32(controlBytesPerCost+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if response, err := protocol.ReadControlMessage(peer); err != nil || response.Pong == nil {
		t.Fatalf("padded Ping: %#v %v", response, err)
	}
	if account.usage() != 8 || local.usage() != 8 || srv.controlInteractivePool.usage() != 8 {
		t.Fatal("successful fallback did not charge identical semantic/byte costs on all paths")
	}
	if srv.metrics.ControlGlobalExpensiveRejections.Load() != 0 || srv.metrics.ControlGlobalMutationRejections.Load() != 0 {
		t.Fatal("successful fallback counted as rejection")
	}
	for _, msg := range []*pb.ControlMessage{{ChannelListRequest: &pb.ChannelListRequest{}}, join} {
		if response := send(msg); response.ErrorResponse == nil || response.ErrorResponse.Code != 8 {
			t.Fatal("exhausted global gate did not refuse")
		}
	}
	if account.usage() != 18 || local.usage() != 18 || srv.metrics.ControlGlobalExpensiveRejections.Load() != 2 {
		t.Fatal("final refusals must charge locals and count once each")
	}
	clock.Add(int64(time.Second))
	if response := send(join); response.ChannelJoinResponse == nil {
		t.Fatal("same connection did not recover after refill")
	}
	if account.usage() != 23 || local.usage() != 23 || srv.metrics.ControlGlobalExpensiveRejections.Load() != 2 {
		t.Fatal("recovery refunded local credits or counted a rejection")
	}
}
