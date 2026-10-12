package client

import (
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestControlResponsesRequirePendingRequest(t *testing.T) {
	cases := []struct {
		name     string
		send     func(*Engine) error
		response *pb.ControlMessage
		request  func(*pb.ControlMessage) bool
	}{
		{"token", func(e *Engine) error { return e.CreateToken("user", 1, 0) }, &pb.ControlMessage{CreateTokenResp: &pb.CreateTokenResponse{Token: "fixture"}}, func(m *pb.ControlMessage) bool { return m.CreateTokenReq != nil }},
		{"role", func(e *Engine) error { return e.SetUserRole(1, "user") }, &pb.ControlMessage{SetUserRoleResp: &pb.SetUserRoleResponse{Success: true}}, func(m *pb.ControlMessage) bool { return m.SetUserRoleReq != nil }},
		{"bans", (*Engine).ListBans, &pb.ControlMessage{ListBansResp: &pb.ListBansResponse{}}, func(m *pb.ControlMessage) bool { return m.ListBansReq != nil && m.ListBansReq.AfterID == 0 }},
		{"ban-page", func(e *Engine) error { return e.ListBansAfter(10) }, &pb.ControlMessage{ListBansResp: &pb.ListBansResponse{}}, func(m *pb.ControlMessage) bool { return m.ListBansReq != nil && m.ListBansReq.AfterID == 10 }},
		{"unban", func(e *Engine) error { return e.Unban(1) }, &pb.ControlMessage{UnbanResp: &pb.UnbanResponse{Success: true}}, func(m *pb.ControlMessage) bool { return m.UnbanReq != nil }},
		{"export", func(e *Engine) error { return e.ExportData("channels") }, &pb.ControlMessage{ExportDataResp: &pb.ExportDataResponse{Type: "channels"}}, func(m *pb.ControlMessage) bool { return m.ExportDataReq != nil }},
		{"import", func(e *Engine) error { return e.ImportChannels("channels: []") }, &pb.ControlMessage{ImportChannelsResp: &pb.ImportChannelsResponse{Success: true}}, func(m *pb.ControlMessage) bool { return m.ImportChannelsReq != nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine()
			g := newConnectionGeneration()
			conn, peer := net.Pipe()
			defer peer.Close()
			defer g.closeResources()
			g.control = &ControlClient{conn: conn}
			e.generation, e.state = g, StateConnected
			calls := make(chan struct{}, 10)
			called := func() { calls <- struct{}{} }
			e.OnTokenCreated = func(string) { called() }
			e.OnRoleChanged = func(bool, string) { called() }
			e.OnBanList = func([]pb.BanInfo, bool) { called() }
			e.OnUnban = func(bool) { called() }
			e.OnExportData = func(string, string) { called() }
			e.OnImportResult = func(bool, string) { called() }
			flush := func(want int) {
				t.Helper()
				done := make(chan struct{})
				e.invokeCallback(func() { close(done) })
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("callback queue stalled")
				}
				if len(calls) != want {
					t.Fatalf("callbacks = %d, want %d", len(calls), want)
				}
				for range want {
					<-calls
				}
			}
			e.handleEventGeneration(g, tc.response)
			flush(0)
			for range 2 {
				sent := make(chan error, 1)
				go func() { sent <- tc.send(e) }()
				_ = peer.SetReadDeadline(time.Now().Add(time.Second))
				request, err := protocol.ReadControlMessage(peer)
				if err != nil {
					t.Fatal(err)
				}
				if !tc.request(request) {
					t.Fatal("wrong request on wire")
				}
				if err := <-sent; err != nil {
					t.Fatal(err)
				}
			}
			for range 3 {
				e.handleEventGeneration(g, tc.response)
			}
			flush(2)
			// A failed send cannot authorize a later response.
			_ = peer.Close()
			if err := tc.send(e); err == nil {
				t.Fatal("send to closed peer succeeded")
			}
			e.handleEventGeneration(g, tc.response)
			flush(0)
			g.cancel()
			e.handleEventGeneration(g, tc.response)
			flush(0)
			// Replacing the generation cannot inherit old pending requests.
			e.generation = newConnectionGeneration()
			defer e.generation.cancel()
			e.handleEventGeneration(g, tc.response)
			e.handleEventGeneration(e.generation, tc.response)
			flush(0)
		})
	}
}
