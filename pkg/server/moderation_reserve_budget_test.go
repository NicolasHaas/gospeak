package server

import (
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestModerationReserveTrustedRolesAndBoundaries(t *testing.T) {
	srv := New(DefaultConfig(), Dependencies{})
	now := time.Unix(1700000000, 0)
	srv.controlGlobalBudget = newControlMessageLimiter(controlExpensiveCost, 0, time.Now)
	srv.controlModerationBudget.now = func() time.Time { return now }
	srv.controlModerationBudget.updated = now
	admin := mustCreateSession(t, srv.sessions, 1, "reserve-admin", model.RoleAdmin)
	mod := mustCreateSession(t, srv.sessions, 2, "reserve-mod", model.RoleModerator)
	user := mustCreateSession(t, srv.sessions, 3, "reserve-user", model.RoleUser)
	kick := &pb.ControlMessage{KickUserReq: &pb.KickUserRequest{}}
	ban := &pb.ControlMessage{BanUserReq: &pb.BanUserRequest{}}
	if !srv.allowGlobalControl(admin.ID, kick, 0).Allowed || srv.controlModerationBudget.usage() != 0 {
		t.Fatal("healthy ordinary budget did not take precedence")
	}
	for _, tc := range []struct {
		id  uint32
		msg *pb.ControlMessage
	}{
		{admin.ID, &pb.ControlMessage{Ping: &pb.Ping{}}},
		{admin.ID, &pb.ControlMessage{ChannelListRequest: &pb.ChannelListRequest{}}},
		{admin.ID, &pb.ControlMessage{ChatMsg: &pb.ChatMessage{}}},
		{admin.ID, &pb.ControlMessage{UnbanReq: &pb.UnbanRequest{}}},
		{user.ID, kick}, {user.ID, ban}, {mod.ID, ban}, {0, kick},
	} {
		if srv.allowGlobalControl(tc.id, tc.msg, 0).Allowed {
			t.Fatal("ordinary/unprivileged request used reserve")
		}
	}
	if srv.controlModerationBudget.usage() != 0 {
		t.Fatal("unqualified requests consumed reserve")
	}
	// Invalid targets still pay: qualification authorizes the role, not the target.
	for _, tc := range []struct {
		id  uint32
		msg *pb.ControlMessage
	}{{admin.ID, kick}, {admin.ID, ban}, {mod.ID, kick}, {admin.ID, ban}} {
		if !srv.allowGlobalControl(tc.id, tc.msg, 0).Allowed {
			t.Fatal("authorized moderation refused before four-request burst")
		}
	}
	srv.sessions.Remove(mod.ID)
	mod = mustCreateSession(t, srv.sessions, 2, "reconnected-mod", model.RoleModerator)
	if srv.allowGlobalControl(mod.ID, kick, 0).Allowed {
		t.Fatal("reconnecting restored the shared reserve")
	}
	now = now.Add(time.Second - time.Nanosecond)
	if srv.allowGlobalControl(admin.ID, kick, 0).Allowed {
		t.Fatal("reserve refilled early")
	}
	now = now.Add(time.Nanosecond)
	if !srv.allowGlobalControl(admin.ID, ban, 0).Allowed || srv.allowGlobalControl(mod.ID, kick, 0).Allowed {
		t.Fatal("one-second refill must admit exactly one request")
	}
	now = now.Add(time.Hour)
	if srv.allowGlobalControl(admin.ID, ban, controlModerationBurst*controlBytesPerCost+1).Allowed || srv.controlModerationBudget.usage() != 0 {
		t.Fatal("oversized denial must not consume reserve")
	}
	srv.sessions.UpdateRoleByUserID(admin.UserID, model.RoleUser)
	if srv.allowGlobalControl(admin.ID, kick, 0).Allowed || srv.controlModerationBudget.usage() != 0 {
		t.Fatal("revoked role retained reserve access")
	}
	srv.sessions.Remove(mod.ID)
	if srv.allowGlobalControl(mod.ID, kick, 0).Allowed {
		t.Fatal("removed session retained reserve access")
	}
	srv.sessions.UpdateRoleByUserID(admin.UserID, model.RoleAdmin)
	if !srv.allowGlobalControl(admin.ID, ban, controlModerationBurst*controlBytesPerCost).Allowed || srv.controlModerationBudget.usage() != controlModerationBurst {
		t.Fatal("exact reserve byte boundary was not admitted and fully charged")
	}
}
