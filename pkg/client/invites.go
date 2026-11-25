package client

import (
	"fmt"

	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

// ListInvites requests one bounded metadata page. Only one invite operation may be in flight.
func (e *Engine) ListInvites(afterID int64, callback func(pb.ListInvitesResponse)) error {
	if afterID < 0 || callback == nil {
		return fmt.Errorf("invalid invite request")
	}
	return e.sendInviteRequest(&pb.ControlMessage{ListInvitesReq: &pb.ListInvitesRequest{AfterID: afterID, Limit: 100}}, func(msg *pb.ControlMessage) {
		if msg.ListInvitesResp == nil {
			callback(pb.ListInvitesResponse{Error: "unexpected invite response"})
			return
		}
		callback(*msg.ListInvitesResp)
	})
}

func (e *Engine) RevokeInvite(id int64, callback func(pb.RevokeInviteResponse)) error {
	if id <= 0 || callback == nil {
		return fmt.Errorf("invalid invite request")
	}
	return e.sendInviteRequest(&pb.ControlMessage{RevokeInviteReq: &pb.RevokeInviteRequest{InviteID: id}}, func(msg *pb.ControlMessage) {
		if msg.RevokeInviteResp == nil {
			callback(pb.RevokeInviteResponse{Error: "unexpected invite response"})
			return
		}
		callback(*msg.RevokeInviteResp)
	})
}

func (e *Engine) sendInviteRequest(msg *pb.ControlMessage, callback func(*pb.ControlMessage)) error {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()
	g.mu.Lock()
	if g.ctx.Err() != nil || g.pendingResponses[callbackInvites] != 0 {
		g.mu.Unlock()
		return fmt.Errorf("invite operation unavailable or already pending")
	}
	g.inviteResponse = callback
	g.pendingResponses[callbackInvites] = 1
	g.mu.Unlock()
	if err := ctrl.Send(msg); err != nil {
		g.mu.Lock()
		delete(g.pendingResponses, callbackInvites)
		g.inviteResponse = nil
		g.mu.Unlock()
		return err
	}
	return nil
}

func (e *Engine) handleInviteResponse(g *connectionGeneration, msg *pb.ControlMessage) {
	g.mu.Lock()
	callback := g.inviteResponse
	pending := g.pendingResponses[callbackInvites] != 0
	delete(g.pendingResponses, callbackInvites)
	g.inviteResponse = nil
	g.mu.Unlock()
	if !pending || callback == nil {
		return
	}
	if !e.enqueueQualifiedGenerationCallbackLocked(g, func() {
		e.mu.RLock()
		current := e.generation == g
		e.mu.RUnlock()
		if current && g.ctx.Err() == nil {
			callback(msg)
		}
	}, true) {
		go e.requestDisconnect(g, "invite callback backlog full")
	}
}
