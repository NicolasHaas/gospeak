package server

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/model"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestModerationReserveWireRecoveryAndLocalLimits(t *testing.T) {
	for _, scope := range []string{"kick", "ban", "account", "session"} {
		t.Run(scope, func(t *testing.T) {
			srv, st, handler := newTestServer(t)
			srv.controlGlobalBudget = newControlMessageLimiter(300, 0, time.Now)
			var clock atomic.Int64
			clock.Store(time.Unix(1700000000, 0).UnixNano())
			srv.controlModerationBudget.now = func() time.Time { return time.Unix(0, clock.Load()) }
			srv.controlModerationBudget.updated = srv.controlModerationBudget.now()
			role := model.RoleAdmin
			if scope == "kick" {
				role = model.RoleModerator
			}
			user, err := st.NonTx().CreateUser("reserve-actor", role)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.NonTx().UpdateUserPersonalToken(user.ID, crypto.HashToken("reserve-token"), time.Now()); err != nil {
				t.Fatal(err)
			}
			actor := connectPublicationClient(t, srv, handler, st)
			auth, err := actor.Authenticate("reserve-token", user.Username)
			if err != nil {
				t.Fatal(err)
			}
			events := make(chan *pb.ControlMessage, 2)
			actor.SetEventHandler(func(m *pb.ControlMessage) { events <- m })
			actor.StartReceiving()
			t.Cleanup(func() {
				_ = actor.Close()
				select {
				case <-actor.Done():
				case <-time.After(time.Second):
					t.Error("actor receive loop did not exit")
				}
			})
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
			if err := actor.Send(&pb.ControlMessage{Ping: &pb.Ping{}}); err != nil {
				t.Fatal(err)
			}
			if m := read(); m.Pong == nil {
				t.Fatalf("readiness Ping: %#v", m)
			}
			// Pong proves both local limiters are installed. Freeze refill under their locks.
			srv.controlBudgetMu.RLock()
			sessionLimiter := srv.controlBudgets[auth.SessionID]
			srv.controlBudgetMu.RUnlock()
			accountLimiter, _, _, ok := srv.controlUserBudgets.Acquire(user.ID)
			if !ok {
				t.Fatal("account budget missing")
			}
			defer srv.controlUserBudgets.Release(user.ID)
			for _, limiter := range []*controlMessageLimiter{accountLimiter, sessionLimiter, srv.controlGlobalBudget} {
				limiter.mu.Lock()
				limiter.refill = 0
				if limiter == srv.controlGlobalBudget || scope == "account" && limiter == accountLimiter || scope == "session" && limiter == sessionLimiter {
					limiter.tokens = 0
				}
				limiter.mu.Unlock()
			}
			target, err := st.NonTx().CreateUser("reserve-target", model.RoleUser)
			if err != nil {
				t.Fatal(err)
			}
			session := mustCreateSession(t, srv.sessions, target.ID, target.Username, target.Role)
			conn := registerTestConn(handler, session.ID)
			request := &pb.ControlMessage{KickUserReq: &pb.KickUserRequest{UserID: target.ID}}
			if scope == "ban" {
				request = &pb.ControlMessage{BanUserReq: &pb.BanUserRequest{UserID: target.ID}}
			}
			localDenial := scope == "account" || scope == "session"
			if !localDenial {
				srv.controlModerationBudget.mu.Lock()
				srv.controlModerationBudget.tokens = 0
				srv.controlModerationBudget.mu.Unlock()
			}
			accountBefore, sessionBefore := accountLimiter.usage(), sessionLimiter.usage()
			if err := actor.Send(request); err != nil {
				t.Fatal(err)
			}
			if m := read(); m.ErrorResponse == nil || m.ErrorResponse.Code != 8 {
				t.Fatalf("exhaustion response: %#v", m)
			}
			select {
			case <-conn.closed:
				t.Fatal("refused request moderated target")
			default:
			}
			if localDenial {
				if srv.controlModerationBudget.usage() != 0 {
					t.Fatal("local denial consumed reserve")
				}
				select {
				case <-actor.Done():
				case <-time.After(time.Second):
					t.Fatal("local exhaustion was not terminal")
				}
				return
			}
			// The error response is a charging barrier; retry on the same TLS connection.
			clock.Add(int64(time.Second))
			if err := actor.Send(request); err != nil {
				t.Fatal(err)
			}
			requireClosed(t, conn)
			if scope == "ban" {
				banned, err := st.NonTx().IsUserBanned(target.ID)
				if err != nil || !banned {
					t.Fatalf("ban not persisted: %t %v", banned, err)
				}
			}
			if accountLimiter.usage()-accountBefore != 2*controlExpensiveCost || sessionLimiter.usage()-sessionBefore != 2*controlExpensiveCost {
				t.Fatal("refused and admitted moderation must both charge account and session")
			}
		})
	}
}
