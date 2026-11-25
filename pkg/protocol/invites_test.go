package protocol

import (
	"bytes"
	"math"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestInviteMetadataPageFitsControlBudget(t *testing.T) {
	invites := make([]pb.InviteInfo, 100)
	for i := range invites {
		invites[i] = pb.InviteInfo{ID: math.MaxInt64, Role: "moderator", ChannelScope: math.MaxInt64, CreatedBy: math.MaxInt64, MaxUses: math.MaxInt64, UseCount: math.MaxInt64, ExpiresAt: math.MaxInt64, CreatedAt: math.MaxInt64}
	}
	var frame bytes.Buffer
	if err := WriteControlMessage(&frame, &pb.ControlMessage{ListInvitesResp: &pb.ListInvitesResponse{Invites: invites, HasMore: true}}); err != nil {
		t.Fatal(err)
	}
	if frame.Len() > MaxControlMessage {
		t.Fatal("bounded invite page exceeds frame budget")
	}
}
