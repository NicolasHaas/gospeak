package server

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestControlMessageLimiterUsesMessageCostsAndRefills(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newControlMessageLimiter(5, 1, func() time.Time { return now })

	if decision := limiter.Allow(&pb.ControlMessage{ChatMsg: &pb.ChatMessage{}}); !decision.Allowed || decision.Cost != 2 {
		t.Fatalf("chat decision = %#v, want allowed cost 2", decision)
	}
	if decision := limiter.Allow(&pb.ControlMessage{CreateChannelReq: &pb.CreateChannelRequest{}}); decision.Allowed || decision.Reason != "expensive" {
		t.Fatalf("expensive decision = %#v, want expensive rejection", decision)
	}

	now = now.Add(2 * time.Second)
	if decision := limiter.Allow(&pb.ControlMessage{CreateChannelReq: &pb.CreateChannelRequest{}}); !decision.Allowed {
		t.Fatalf("decision after refill = %#v, want allowed", decision)
	}
}

func TestControlMessageLimiterRejectsMutationFloodWithoutAffectingAnotherSession(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	first := newControlMessageLimiter(2, 1, func() time.Time { return now })
	second := newControlMessageLimiter(2, 1, func() time.Time { return now })
	message := &pb.ControlMessage{Ping: &pb.Ping{}}

	firstDecision := first.Allow(message)
	secondDecision := first.Allow(message)
	if !firstDecision.Allowed || !secondDecision.Allowed {
		t.Fatal("first session exhausted before configured burst")
	}
	if decision := first.Allow(message); decision.Allowed || decision.Reason != "mutation" {
		t.Fatalf("third first-session decision = %#v, want mutation rejection", decision)
	}
	if decision := second.Allow(message); !decision.Allowed {
		t.Fatalf("second-session decision = %#v, want independent allowance", decision)
	}
}

func TestControlBudgetRejectionsAreClassifiedExactlyOnce(t *testing.T) {
	tests := []struct {
		reason        string
		wantMutation  int64
		wantChat      int64
		wantExpensive int64
	}{
		{reason: "mutation", wantMutation: 1},
		{reason: "chat", wantChat: 1},
		{reason: "expensive", wantExpensive: 1},
	}
	for _, tc := range tests {
		t.Run(tc.reason, func(t *testing.T) {
			srv := New(DefaultConfig(), Dependencies{})
			srv.recordControlBudgetRejection("192.0.2.1:1234", "session", controlBudgetDecision{
				Reason: tc.reason,
				Usage:  60,
				Limit:  60,
			})
			if got := srv.metrics.ControlSessionMutationRejections.Load(); got != tc.wantMutation {
				t.Fatalf("mutation rejections = %d, want %d", got, tc.wantMutation)
			}
			if got := srv.metrics.ControlSessionChatRejections.Load(); got != tc.wantChat {
				t.Fatalf("chat rejections = %d, want %d", got, tc.wantChat)
			}
			if got := srv.metrics.ControlSessionExpensiveRejections.Load(); got != tc.wantExpensive {
				t.Fatalf("expensive rejections = %d, want %d", got, tc.wantExpensive)
			}
		})
	}
}

func TestControlMessageBudgetDisconnectsFloodingSession(t *testing.T) {
	srv, st, _ := newTestServer(t)
	srv.cfg.AllowNoToken = true
	srv.cfg.ControlMessageBurst = 2
	srv.cfg.ControlMessagesPerSec = 1
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	done := make(chan struct{})
	go func() {
		srv.handleControlConn(newControlHandler(srv, st), serverConn, st)
		close(done)
	}()

	if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{
		AuthRequest: &pb.AuthRequest{Username: "budget-user"},
	}); err != nil {
		t.Fatalf("write auth request: %v", err)
	}
	response, err := protocol.ReadControlMessage(clientConn)
	if err != nil || response.AuthResponse == nil {
		t.Fatalf("auth response = %#v, error = %v", response, err)
	}
	for i := range 2 {
		if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{Ping: &pb.Ping{Timestamp: int64(i)}}); err != nil {
			t.Fatalf("write allowed ping %d: %v", i, err)
		}
		response, err = protocol.ReadControlMessage(clientConn)
		if err != nil || response.Pong == nil {
			t.Fatalf("allowed ping %d response = %#v, error = %v", i, response, err)
		}
	}
	if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{Ping: &pb.Ping{Timestamp: 3}}); err != nil {
		t.Fatalf("write rejected ping: %v", err)
	}
	response, err = protocol.ReadControlMessage(clientConn)
	if err != nil || response.ErrorResponse == nil || response.ErrorResponse.Code != 8 {
		t.Fatalf("flood response = %#v, error = %v", response, err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("control handler did not disconnect rate-limited session")
	}
	if got := srv.metrics.ControlSessionMutationRejections.Load(); got != 1 {
		t.Fatalf("control mutation rejections = %d, want 1", got)
	}
	if got := srv.metrics.ControlSessionBudgetHighWater.Load(); got != 2 {
		t.Fatalf("control budget high-water = %d, want 2", got)
	}
	if snapshot := srv.controlBudgetSnapshot(); snapshot.activeSessions != 0 || snapshot.activeUsers != 0 {
		t.Fatalf("active control budgets after disconnect = %#v, want no active budgets", snapshot)
	}
}

func TestControlMessageLimiterRecordsAdmissionHighWaterBeforeRefill(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	metrics := NewMetrics()
	limiter := newControlMessageLimiter(5, 5, func() time.Time { return now })
	limiter.highWater = &metrics.ControlSessionBudgetHighWater

	if decision := limiter.Allow(&pb.ControlMessage{CreateChannelReq: &pb.CreateChannelRequest{}}); !decision.Allowed {
		t.Fatalf("expensive decision = %#v, want allowed", decision)
	}
	now = now.Add(time.Second)
	if usage := limiter.usage(); usage != 0 {
		t.Fatalf("usage after refill = %d, want 0", usage)
	}
	if highWater := metrics.ControlSessionBudgetHighWater.Load(); highWater != 5 {
		t.Fatalf("high-water after refill = %d, want 5", highWater)
	}
}

func TestControlMessageCostCoversEveryInboundRequest(t *testing.T) {
	tests := []struct {
		name   string
		msg    *pb.ControlMessage
		reason string
		cost   int
	}{
		{"join", &pb.ControlMessage{JoinChannelRequest: &pb.JoinChannelRequest{}}, "expensive", 5},
		{"leave", &pb.ControlMessage{LeaveChannelRequest: &pb.LeaveChannelRequest{}}, "expensive", 5},
		{"channel list", &pb.ControlMessage{ChannelListRequest: &pb.ChannelListRequest{}}, "expensive", 5},
		{"user state", &pb.ControlMessage{UserStateUpdate: &pb.UserStateUpdate{}}, "expensive", 5},
		{"create channel", &pb.ControlMessage{CreateChannelReq: &pb.CreateChannelRequest{}}, "expensive", 5},
		{"delete channel", &pb.ControlMessage{DeleteChannelReq: &pb.DeleteChannelRequest{}}, "expensive", 5},
		{"create token", &pb.ControlMessage{CreateTokenReq: &pb.CreateTokenRequest{}}, "expensive", 5},
		{"kick", &pb.ControlMessage{KickUserReq: &pb.KickUserRequest{}}, "expensive", 5},
		{"ban", &pb.ControlMessage{BanUserReq: &pb.BanUserRequest{}}, "expensive", 5},
		{"chat", &pb.ControlMessage{ChatMsg: &pb.ChatMessage{}}, "chat", 2},
		{"screen start", &pb.ControlMessage{ScreenShareStartReq: &pb.ScreenShareStartRequest{}}, "expensive", 5},
		{"screen stop", &pb.ControlMessage{ScreenShareStopReq: &pb.ScreenShareStopRequest{}}, "expensive", 5},
		{"screen subscribe", &pb.ControlMessage{ScreenShareSubReq: &pb.ScreenShareSubscribeRequest{}}, "expensive", 5},
		{"screen share", &pb.ControlMessage{ScreenShareShareReq: &pb.ScreenShareShareRequest{}}, "expensive", 5},
		{"screen unsubscribe", &pb.ControlMessage{ScreenShareUnsubReq: &pb.ScreenShareUnsubscribeRequest{}}, "mutation", 1},
		{"set role", &pb.ControlMessage{SetUserRoleReq: &pb.SetUserRoleRequest{}}, "expensive", 5},
		{"export", &pb.ControlMessage{ExportDataReq: &pb.ExportDataRequest{}}, "expensive", 5},
		{"import", &pb.ControlMessage{ImportChannelsReq: &pb.ImportChannelsRequest{}}, "expensive", 5},
		{"ping", &pb.ControlMessage{Ping: &pb.Ping{}}, "mutation", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, cost := controlMessageCost(tc.msg)
			if reason != tc.reason || cost != tc.cost {
				t.Fatalf("cost = (%q, %d), want (%q, %d)", reason, cost, tc.reason, tc.cost)
			}
		})
	}
}

func TestServerRaisesTooSmallControlBurstToLargestMessageCost(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ControlMessageBurst = 1
	cfg.ControlGlobalBurst = 1
	srv := New(cfg, Dependencies{})
	if srv.cfg.ControlMessageBurst != controlExpensiveCost {
		t.Fatalf("effective control burst = %d, want %d", srv.cfg.ControlMessageBurst, controlExpensiveCost)
	}
	if srv.cfg.ControlGlobalBurst != controlExpensiveCost {
		t.Fatalf("effective global control burst = %d, want %d", srv.cfg.ControlGlobalBurst, controlExpensiveCost)
	}
}

func TestGlobalControlBudgetBoundsAggregateUsers(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	srv := New(DefaultConfig(), Dependencies{})
	srv.controlGlobalBudget = newControlMessageLimiter(controlExpensiveCost, 1, func() time.Time { return now })
	srv.controlGlobalBudget.highWater = &srv.metrics.ControlGlobalBudgetHighWater
	message := &pb.ControlMessage{UserStateUpdate: &pb.UserStateUpdate{}}

	first := srv.controlGlobalBudget.Allow(message)
	if !first.Allowed {
		t.Fatalf("first global decision = %#v, want allowed", first)
	}
	second := srv.controlGlobalBudget.Allow(message)
	if second.Allowed || second.Reason != "expensive" {
		t.Fatalf("second global decision = %#v, want expensive rejection", second)
	}
	srv.recordControlBudgetRejection("192.0.2.10:4000", "global", second)
	if got := srv.metrics.ControlGlobalExpensiveRejections.Load(); got != 1 {
		t.Fatalf("global expensive rejections = %d, want 1", got)
	}
	if got := srv.metrics.ControlGlobalBudgetHighWater.Load(); got != controlExpensiveCost {
		t.Fatalf("global high-water = %d, want %d", got, controlExpensiveCost)
	}
}

func TestGlobalControlBudgetDisconnectsAcrossDistinctUsers(t *testing.T) {
	srv, st, _ := newTestServer(t)
	srv.cfg.AllowNoToken = true
	srv.controlGlobalBudget = newControlMessageLimiter(1, 1, func() time.Time { return time.Unix(1_700_000_000, 0) })
	srv.controlGlobalBudget.highWater = &srv.metrics.ControlGlobalBudgetHighWater

	connect := func(username string) (net.Conn, <-chan struct{}) {
		serverConn, clientConn := net.Pipe()
		done := make(chan struct{})
		go func() {
			srv.handleControlConn(newControlHandler(srv, st), serverConn, st)
			close(done)
		}()
		if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{Username: username}}); err != nil {
			t.Fatalf("write %s auth: %v", username, err)
		}
		response, err := protocol.ReadControlMessage(clientConn)
		if err != nil || response.AuthResponse == nil {
			t.Fatalf("%s auth response = %#v, error = %v", username, response, err)
		}
		return clientConn, done
	}

	first, firstDone := connect("global-first")
	second, secondDone := connect("global-second")
	defer func() { _ = first.Close() }()
	defer func() { _ = second.Close() }()

	if err := protocol.WriteControlMessage(first, &pb.ControlMessage{Ping: &pb.Ping{Timestamp: 1}}); err != nil {
		t.Fatalf("write first ping: %v", err)
	}
	if response, err := protocol.ReadControlMessage(first); err != nil || response.Pong == nil {
		t.Fatalf("first ping response = %#v, error = %v", response, err)
	}
	if err := protocol.WriteControlMessage(second, &pb.ControlMessage{Ping: &pb.Ping{Timestamp: 2}}); err != nil {
		t.Fatalf("write second ping: %v", err)
	}
	response, err := protocol.ReadControlMessage(second)
	if err != nil || response.ErrorResponse == nil || response.ErrorResponse.Code != 8 {
		t.Fatalf("global rejection response = %#v, error = %v", response, err)
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("globally rate-limited session was not disconnected")
	}
	if got := srv.metrics.ControlGlobalMutationRejections.Load(); got != 1 {
		t.Fatalf("global mutation rejections = %d, want 1", got)
	}
	_ = first.Close()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first global-budget session did not close")
	}
}

func TestControlUserBudgetSurvivesReconnectUntilRefill(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	manager := newControlUserBudgetManager(2, 1, func() time.Time { return now })
	first, _, _, ok := manager.Acquire(42)
	if !ok {
		t.Fatal("first Acquire rejected")
	}
	message := &pb.ControlMessage{Ping: &pb.Ping{}}
	firstDecision := first.Allow(message)
	secondDecision := first.Allow(message)
	if !firstDecision.Allowed || !secondDecision.Allowed {
		t.Fatal("initial burst rejected")
	}
	manager.Release(42)

	reconnected, _, _, ok := manager.Acquire(42)
	if !ok {
		t.Fatal("reconnect Acquire rejected")
	}
	if decision := reconnected.Allow(message); decision.Allowed {
		t.Fatalf("reconnect restored depleted user burst: %#v", decision)
	}
	manager.Release(42)

	now = now.Add(2 * time.Second)
	refilled, _, _, ok := manager.Acquire(42)
	if !ok || !refilled.Allow(message).Allowed {
		t.Fatal("refilled user budget was not reusable")
	}
	manager.Release(42)
}

func TestControlUserBudgetIsSharedAcrossConcurrentSessions(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	manager := newControlUserBudgetManager(2, 1, func() time.Time { return now })
	first, _, _, ok := manager.Acquire(7)
	if !ok {
		t.Fatal("first Acquire rejected")
	}
	second, _, _, ok := manager.Acquire(7)
	if !ok || first != second {
		t.Fatal("same user did not receive the shared limiter")
	}
	message := &pb.ControlMessage{Ping: &pb.Ping{}}
	if !first.Allow(message).Allowed || !second.Allow(message).Allowed {
		t.Fatal("shared burst rejected too early")
	}
	if decision := first.Allow(message); decision.Allowed {
		t.Fatalf("concurrent sessions exceeded aggregate user budget: %#v", decision)
	}
	manager.Release(7)
	manager.Release(7)
}

func TestControlUserBudgetTrackerFailsClosedAndPrunesRefilledEntries(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	manager := newControlUserBudgetManager(1, 1, func() time.Time { return now })
	manager.maxEntries = 1
	first, _, _, ok := manager.Acquire(1)
	if !ok || !first.Allow(&pb.ControlMessage{Ping: &pb.Ping{}}).Allowed {
		t.Fatal("first user budget was not admitted and charged")
	}
	manager.Release(1)
	if _, current, limit, ok := manager.Acquire(2); ok || current != 1 || limit != 1 {
		t.Fatalf("tracker admission = (current %d, limit %d, allowed %t), want (1, 1, false)", current, limit, ok)
	}
	now = now.Add(time.Second)
	if _, _, _, ok := manager.Acquire(2); !ok {
		t.Fatal("refilled inactive entry was not pruned")
	}
	manager.Release(2)
}

func TestControlUserBudgetTrackerRejectionRollsBackOpenProvisioning(t *testing.T) {
	srv, st, _ := newTestServer(t)
	srv.cfg.AllowNoToken = true
	now := time.Unix(1_700_000_000, 0)
	srv.controlUserBudgets = newControlUserBudgetManager(1, 1, func() time.Time { return now })
	srv.controlUserBudgets.maxEntries = 1
	occupied, _, _, ok := srv.controlUserBudgets.Acquire(999)
	if !ok || !occupied.Allow(&pb.ControlMessage{Ping: &pb.Ping{}}).Allowed {
		t.Fatal("could not occupy control user budget tracker")
	}
	srv.controlUserBudgets.Release(999)

	response := authenticateControl(t, srv, st, "must-roll-back", "")
	if response.ErrorResponse == nil {
		t.Fatalf("tracker-capacity response = %#v, want error", response)
	}
	if user, err := st.NonTx().GetUserByUsername("must-roll-back"); err != nil || user != nil {
		t.Fatalf("tracker-rejected user persisted: user=%#v err=%v", user, err)
	}
	if got := srv.metrics.ControlUserTrackerRejections.Load(); got != 1 {
		t.Fatalf("control user tracker rejections = %d, want 1", got)
	}
	if usage := srv.authLimiter.usageSnapshot(); usage.activeSources != 0 || usage.maxUsage != 0 {
		t.Fatalf("valid tracker-rejected authentication consumed failed-auth budget: %#v", usage)
	}
}

func TestControlMessageLimiterChargesEncodedBytes(t *testing.T) {
	limiter := newControlMessageLimiter(32, 1, func() time.Time { return time.Unix(1_700_000_000, 0) })
	message := &pb.ControlMessage{Ping: &pb.Ping{}}
	decision := limiter.AllowSized(message, 2*controlBytesPerCost+1)
	if !decision.Allowed || decision.Reason != "bytes" || decision.Cost != 3 {
		t.Fatalf("byte-sized decision = %#v, want allowed bytes cost 3", decision)
	}
	decision = limiter.AllowSized(message, protocol.MaxControlMessage)
	if decision.Allowed || decision.Reason != "bytes" || decision.Cost != protocol.MaxControlMessage/controlBytesPerCost {
		t.Fatalf("maximum frame decision = %#v", decision)
	}
}

func TestControlUserBudgetCannotBeResetByReauthentication(t *testing.T) {
	srv, st, _ := newTestServer(t)
	srv.cfg.AllowNoToken = true
	srv.cfg.ControlMessageBurst = 2
	srv.cfg.ControlMessagesPerSec = 1
	srv.controlUserBudgets = newControlUserBudgetManager(2, 1, func() time.Time {
		return time.Unix(1_700_000_000, 0)
	})

	connect := func(token string) (net.Conn, <-chan struct{}, *pb.AuthResponse) {
		t.Helper()
		serverConn, clientConn := net.Pipe()
		done := make(chan struct{})
		go func() {
			srv.handleControlConn(newControlHandler(srv, st), serverConn, st)
			close(done)
		}()
		if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{
			Username: "reconnect-budget-user",
			Token:    token,
		}}); err != nil {
			t.Fatalf("write auth request: %v", err)
		}
		response, err := protocol.ReadControlMessage(clientConn)
		if err != nil || response.AuthResponse == nil {
			t.Fatalf("auth response = %#v, error = %v", response, err)
		}
		return clientConn, done, response.AuthResponse
	}

	firstConn, firstDone, firstAuth := connect("")
	if firstAuth.AutoToken == "" {
		t.Fatal("open-mode authentication did not return a personal token")
	}
	for i := range 2 {
		if err := protocol.WriteControlMessage(firstConn, &pb.ControlMessage{Ping: &pb.Ping{Timestamp: int64(i)}}); err != nil {
			t.Fatalf("write initial ping %d: %v", i, err)
		}
		response, err := protocol.ReadControlMessage(firstConn)
		if err != nil || response.Pong == nil {
			t.Fatalf("initial ping %d response = %#v, error = %v", i, response, err)
		}
	}
	_ = firstConn.Close()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first connection did not close")
	}

	secondConn, secondDone, _ := connect(firstAuth.AutoToken)
	if err := protocol.WriteControlMessage(secondConn, &pb.ControlMessage{Ping: &pb.Ping{Timestamp: 3}}); err != nil {
		t.Fatalf("write reconnect ping: %v", err)
	}
	response, err := protocol.ReadControlMessage(secondConn)
	if err != nil || response.ErrorResponse == nil || response.ErrorResponse.Code != 8 {
		t.Fatalf("reconnect response = %#v, error = %v", response, err)
	}
	_ = secondConn.Close()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("reconnected session was not disconnected")
	}
	if got := srv.metrics.ControlUserMutationRejections.Load(); got != 1 {
		t.Fatalf("user-scope mutation rejections = %d, want 1", got)
	}
}

func TestControlCapacityMetricsAreExported(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ControlMessageBurst = 9
	cfg.ControlMessagesPerSec = 3
	cfg.ControlGlobalBurst = 15
	cfg.ControlGlobalMessagesPerSec = 4
	srv := New(cfg, Dependencies{})
	srv.metrics.ControlUserChatRejections.Add(2)
	srv.metrics.ControlUserByteRejections.Add(1)
	srv.metrics.ControlGlobalExpensiveRejections.Add(4)
	srv.metrics.ControlInvalidMessages.Add(3)

	recorder := httptest.NewRecorder()
	srv.handleMetrics(recorder, httptest.NewRequestWithContext(context.Background(), "GET", "/metrics", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		"gospeak_control_budget_limit 9",
		"gospeak_control_budget_refill_per_second 3",
		"gospeak_control_budget_global_limit 15",
		"gospeak_control_budget_global_refill_per_second 4",
		`gospeak_control_budget_rejections_total{scope="user",reason="chat"} 2`,
		`gospeak_control_budget_rejections_total{scope="user",reason="bytes"} 1`,
		`gospeak_control_budget_rejections_total{scope="global",reason="expensive"} 4`,
		"gospeak_control_budget_bytes_per_cost 16384",
		"gospeak_control_budget_user_tracker_limit 4096",
		"gospeak_control_invalid_messages_total 3",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}
