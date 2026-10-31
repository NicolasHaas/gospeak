package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestChannelIntegrityHandlers(t *testing.T) {
	srv, st, handler := newTestServer(t)
	parent := &model.Channel{Name: "parent"}
	child := &model.Channel{Name: "child"}
	if err := st.NonTx().CreateChannel(parent); err != nil {
		t.Fatal(err)
	}
	child.ParentID = parent.ID
	if err := st.NonTx().CreateChannel(child); err != nil {
		t.Fatal(err)
	}
	for _, role := range []model.Role{model.RoleAdmin, model.RoleModerator, model.RoleUser} {
		actor := mustCreateSession(t, srv.sessions, int64(role)+1, fmt.Sprint(role), role)
		for _, operation := range []string{"create", "token", "delete", "missing delete"} {
			conn := &bufferConn{}
			switch operation {
			case "create":
				srv.handleCreateChannel(actor.ID, &pb.CreateChannelRequest{Name: "invalid", ParentID: 999}, st, conn, handler)
			case "token":
				srv.handleCreateToken(actor.ID, &pb.CreateTokenRequest{Role: "user", ChannelScope: -1}, st, conn)
			case "delete":
				srv.handleDeleteChannel(actor.ID, &pb.DeleteChannelRequest{ChannelID: parent.ID}, st, conn, handler)
			case "missing delete":
				srv.handleDeleteChannel(actor.ID, &pb.DeleteChannelRequest{ChannelID: 999}, st, conn, handler)
			}
			msg, err := protocol.ReadControlMessage(conn)
			if err != nil || msg.ErrorResponse == nil {
				t.Fatalf("%s/%v: missing refusal: %v %v", operation, role, msg, err)
			}
			code := int32(30)
			if role == model.RoleAdmin {
				code = 31
			}
			if msg.ErrorResponse.Code != code {
				t.Fatalf("%s/%v: code %d, want %d", operation, role, msg.ErrorResponse.Code, code)
			}
		}
	}
	if srv.metrics.ChannelsDeleted.Load() != 0 || srv.metrics.ChannelsCreated.Load() != 0 || srv.metrics.TokensCreated.Load() != 0 {
		t.Fatal("refusals counted as successful changes")
	}
	if got, err := st.NonTx().GetChannel(parent.ID); err != nil || got == nil {
		t.Fatalf("parent lost: %v %v", got, err)
	}
}

func TestTemporaryChannelSweepProtectsDurableReferences(t *testing.T) {
	for _, ref := range []string{"token", "account", "child"} {
		t.Run(ref, func(t *testing.T) {
			srv, st, handler := newTestServer(t)
			parent := &model.Channel{Name: "parent"}
			if err := st.NonTx().CreateChannel(parent); err != nil {
				t.Fatal(err)
			}
			ch := &model.Channel{Name: "temporary", ParentID: parent.ID, IsTemp: true}
			if err := st.NonTx().CreateChannel(ch); err != nil {
				t.Fatal(err)
			}
			switch ref {
			case "token", "account":
				if err := st.NonTx().CreateToken("invite", model.RoleUser, ch.ID, 0, 1, time.Time{}); err != nil {
					t.Fatal(err)
				}
				if ref == "account" {
					tx, err := st.Tx(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					user, err := tx.ProvisionUser("invite", "scoped", "personal", time.Now(), false)
					if err != nil || user.ChannelScope != ch.ID {
						t.Fatalf("provision: %v %v", user, err)
					}
					if err := tx.Commit(); err != nil {
						t.Fatal(err)
					}
					// Isolate the account guard and exercise legacy orphan cleanup too.
					db := st.(*datastore.ProviderFactory).DB
					if _, err := db.ExecContext(context.Background(), "DELETE FROM tokens WHERE hash = ?", "invite"); err != nil {
						t.Fatal(err)
					}
					if _, err := db.ExecContext(context.Background(), "UPDATE channels SET parent_id = 999 WHERE id = ?", ch.ID); err != nil {
						t.Fatal(err)
					}
				}
			case "child":
				if err := ImportChannelsFromYAML([]byte("channels:\n- name: parent\n  channels:\n  - name: temporary\n    channels:\n    - name: durable-child\n"), st); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now()
			srv.tempChannels.now = func() time.Time { return now }
			srv.sweepTempChannels(handler, st)
			now = now.Add(tempChannelEmptyGrace)
			for i := 0; i < 2; i++ {
				delay := srv.sweepTempChannels(handler, st)
				if delay < tempChannelSweepRetry {
					t.Errorf("refusal schedules busy retry: %v", delay)
				}
				if got, err := st.NonTx().GetChannel(ch.ID); err != nil || got == nil {
					t.Fatalf("referenced temporary channel lost: %v %v", got, err)
				}
			}
			if srv.metrics.ChannelsDeleted.Load() != 0 {
				t.Fatal("refusal counted as deletion")
			}
			// An account's scope remains usable after the janitor's refused deletion.
			if ref == "account" {
				user, err := st.NonTx().GetUserByPersonalTokenHash("personal")
				if err != nil || user == nil || user.ChannelScope != ch.ID {
					t.Fatalf("personal login scope: %v %v", user, err)
				}
			}
		})
	}
}
