package protocol

import (
	"bytes"
	"testing"

	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestModerationMessagesRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		msg   *pb.ControlMessage
		check func(*testing.T, *pb.ControlMessage)
	}{
		{
			name: "ban request with selected IP session",
			msg:  &pb.ControlMessage{BanUserReq: &pb.BanUserRequest{UserID: 42, Reason: "test", DurationSeconds: 3600, IPBanSessionID: 7}},
			check: func(t *testing.T, got *pb.ControlMessage) {
				if got.BanUserReq == nil || got.BanUserReq.IPBanSessionID != 7 {
					t.Fatalf("round trip = %#v", got)
				}
			},
		},
		{
			name: "list bans page request",
			msg:  &pb.ControlMessage{ListBansReq: &pb.ListBansRequest{AfterID: 20, Limit: 100}},
			check: func(t *testing.T, got *pb.ControlMessage) {
				if got.ListBansReq == nil || got.ListBansReq.AfterID != 20 || got.ListBansReq.Limit != 100 {
					t.Fatalf("round trip = %#v", got)
				}
			},
		},
		{
			name: "list bans response",
			msg:  &pb.ControlMessage{ListBansResp: &pb.ListBansResponse{Bans: []pb.BanInfo{{ID: 3, UserID: 7, Username: "banned-user", BannedBy: 1}}, HasMore: true}},
			check: func(t *testing.T, got *pb.ControlMessage) {
				if got.ListBansResp == nil || len(got.ListBansResp.Bans) != 1 || got.ListBansResp.Bans[0].Username != "banned-user" || !got.ListBansResp.HasMore {
					t.Fatalf("round trip = %#v", got)
				}
			},
		},
		{
			name: "unban request",
			msg:  &pb.ControlMessage{UnbanReq: &pb.UnbanRequest{BanID: 9}},
			check: func(t *testing.T, got *pb.ControlMessage) {
				if got.UnbanReq == nil || got.UnbanReq.BanID != 9 {
					t.Fatalf("round trip = %#v", got)
				}
			},
		},
		{
			name: "session identity",
			msg:  &pb.ControlMessage{ChannelListResponse: &pb.ChannelListResponse{Channels: []pb.ChannelInfo{{ID: 1, Users: []pb.UserInfo{{SessionID: 11, ID: 2}}}}}},
			check: func(t *testing.T, got *pb.ControlMessage) {
				if got.ChannelListResponse == nil || got.ChannelListResponse.Channels[0].Users[0].SessionID != 11 {
					t.Fatalf("round trip = %#v", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wire bytes.Buffer
			if err := WriteControlMessage(&wire, tt.msg); err != nil {
				t.Fatalf("WriteControlMessage: %v", err)
			}
			got, err := ReadControlMessage(&wire)
			if err != nil {
				t.Fatalf("ReadControlMessage: %v", err)
			}
			tt.check(t, got)
		})
	}
}
