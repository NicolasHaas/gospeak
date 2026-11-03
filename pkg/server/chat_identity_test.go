package server

import (
	"bytes"
	"math"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestAuthAdvertisesHistoryRequestIDs(t *testing.T) {
	srv, st, path := newBootstrapTestServer(t)
	t.Cleanup(srv.Shutdown)
	if err := srv.ensureAdminToken(st); err != nil {
		t.Fatal(err)
	}
	response, err := authenticateBootstrap(t, srv, st, "history-admin", readBootstrapToken(t, path))
	if err != nil || !response.HistoryRequestIDs {
		t.Fatalf("history capability absent: response=%v err=%v", response != nil, err)
	}
}

func TestHistoryIdentityEchoLegacyAndValidation(t *testing.T) {
	srv, st, handler := newTestServer(t)
	channel := model.NewChannel()
	if err := st.NonTx().CreateChannel(channel); err != nil {
		t.Fatal(err)
	}
	other := model.NewChannel()
	other.Name = "foreign"
	if err := st.NonTx().CreateChannel(other); err != nil {
		t.Fatal(err)
	}
	session := mustCreateScopedSession(t, srv.sessions, 1, "reader", model.RoleUser, channel.ID)
	message := &model.Message{ChannelID: channel.ID, SenderID: 1, SenderName: "reader", Body: "stored"}
	if err := st.NonTx().CreateMessage(message); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{0, 1, math.MaxInt64} {
		for _, limit := range []int{0, 500} {
			srv.cfg.ChatHistoryLimit = limit
			conn := &bufferConn{}
			srv.handleMessage(handler, session.ID, &pb.ControlMessage{ChatHistoryReq: &pb.ChatHistoryRequest{RequestID: id, ChannelID: channel.ID}}, st, conn)
			if id == 0 && bytes.Contains(conn.buffer.Bytes(), []byte("request_id")) {
				t.Fatal("new field sent to legacy client")
			}
			result, err := protocol.ReadControlMessage(conn)
			if err != nil || result.ChatHistoryResp == nil || result.ChatHistoryResp.RequestID != id {
				t.Fatalf("identity=%d limit=%d result=%+v err=%v", id, limit, result, err)
			}
			if limit == 0 && len(result.ChatHistoryResp.Messages) != 0 || limit > 0 && len(result.ChatHistoryResp.Messages) != 1 {
				t.Fatal("history policy changed")
			}
		}
	}
	for _, req := range []pb.ChatHistoryRequest{{RequestID: -1, ChannelID: channel.ID}, {RequestID: 1, ChannelID: other.ID}, {RequestID: 1, ChannelID: channel.ID, Limit: chatPageLimit + 1}} {
		conn := &bufferConn{}
		srv.handleMessage(handler, session.ID, &pb.ControlMessage{ChatHistoryReq: &req}, st, conn)
		result, err := protocol.ReadControlMessage(conn)
		if err != nil || result.ErrorResponse == nil {
			t.Fatalf("invalid/scoped request accepted: %+v", req)
		}
	}
}
