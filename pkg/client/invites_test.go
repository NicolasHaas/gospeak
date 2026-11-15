package client

import (
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestInviteCallbacksAreRequestAndGenerationOwned(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "list", true: "revoke"}[revoke], func(t *testing.T) {
			e := NewEngine()
			g := newConnectionGeneration()
			defer g.closeResources()
			conn, peer := net.Pipe()
			defer peer.Close()
			g.control = &ControlClient{conn: conn}
			e.generation, e.state = g, StateConnected
			calls := make(chan string, 8)
			send := func() error {
				if revoke {
					return e.RevokeInvite(7, func(response pb.RevokeInviteResponse) { calls <- response.Error })
				}
				return e.ListInvites(0, func(response pb.ListInvitesResponse) { calls <- response.Error })
			}
			response := &pb.ControlMessage{ListInvitesResp: &pb.ListInvitesResponse{Error: "permission denied"}}
			if revoke {
				response = &pb.ControlMessage{RevokeInviteResp: &pb.RevokeInviteResponse{Error: "permission denied"}}
			}
			flush := func() {
				t.Helper()
				done := make(chan struct{})
				e.invokeCallback(func() { close(done) })
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("callback stalled")
				}
			}
			e.handleEventGeneration(g, response)
			flush()
			if len(calls) != 0 {
				t.Fatal("unsolicited callback")
			}
			request := func() {
				t.Helper()
				sent := make(chan error, 1)
				go func() { sent <- send() }()
				_ = peer.SetReadDeadline(time.Now().Add(time.Second))
				msg, err := protocol.ReadControlMessage(peer)
				if err != nil {
					t.Fatal(err)
				}
				if (msg.RevokeInviteReq != nil) != revoke || (msg.ListInvitesReq != nil) == revoke {
					t.Fatal("wrong request")
				}
				if err := <-sent; err != nil {
					t.Fatal(err)
				}
			}
			request()
			if err := send(); err == nil {
				t.Fatal("duplicate in-flight request accepted")
			}
			e.handleEventGeneration(g, response)
			flush()
			if len(calls) != 1 || <-calls != "permission denied" {
				t.Fatal("error completion missing")
			}
			request() // A denial completes the operation and permits retry.
			blocked, release := make(chan struct{}), make(chan struct{})
			e.invokeCallback(func() { close(blocked); <-release })
			<-blocked
			e.handleEventGeneration(g, response)
			replacement := newConnectionGeneration()
			defer replacement.closeResources()
			e.mu.Lock()
			e.generation = replacement
			e.mu.Unlock()
			close(release)
			flush()
			e.handleEventGeneration(g, response)
			flush()
			if len(calls) != 0 {
				t.Fatal("old generation updated replacement UI")
			}
			g.mu.Lock()
			pending := g.pendingResponses[callbackInvites]
			g.mu.Unlock()
			if pending != 0 {
				t.Fatal("completed response still pending")
			}
		})
	}
}

func TestInviteRequestValidationAndFailedSend(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	defer g.closeResources()
	conn, peer := net.Pipe()
	_ = peer.Close()
	g.control = &ControlClient{conn: conn}
	e.generation, e.state = g, StateConnected
	if err := e.ListInvites(-1, func(pb.ListInvitesResponse) {}); err == nil {
		t.Fatal("negative cursor accepted")
	}
	if err := e.RevokeInvite(0, func(pb.RevokeInviteResponse) {}); err == nil {
		t.Fatal("zero ID accepted")
	}
	if err := e.ListInvites(0, func(pb.ListInvitesResponse) { t.Error("failed send callback") }); err == nil {
		t.Fatal("closed send succeeded")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pendingResponses[callbackInvites] != 0 || g.inviteResponse != nil {
		t.Fatal("failed send left pending callback")
	}
}
