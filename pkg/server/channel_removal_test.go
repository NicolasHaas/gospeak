package server

import (
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestChannelRemovalPublishesStateAfterJoinACK(t *testing.T) {
	for _, sweep := range []bool{false, true} {
		name := "explicit delete"
		if sweep {
			name = "orphan sweep"
		}
		t.Run(name, func(t *testing.T) {
			srv, st, handler := newTestServer(t)
			handler.statePublisher.Do(func() {}) // publish at explicit barriers, not timer ticks
			channel := &model.Channel{Name: "removed", IsTemp: sweep, ParentID: 999}
			if err := st.NonTx().CreateChannel(channel); err != nil {
				t.Fatal(err)
			}
			member := mustCreateSession(t, srv.sessions, 1, "member", model.RoleAdmin)
			server, peer := net.Pipe()
			defer peer.Close()
			conn := handler.setConn(member.ID, server)
			if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			// The ACK can still be queued when a different connection deletes the channel.
			srv.handleJoinChannel(handler, member.ID, &pb.JoinChannelRequest{ChannelID: channel.ID}, st, conn)
			if _, err := srv.screenShare.Start(channel.ID, member.ID, member.UserID, member.Username, 640, 480); err != nil {
				t.Fatal(err)
			}
			if sweep {
				srv.sweepTempChannels(handler, st)
			} else {
				srv.handleDeleteChannel(member.ID, &pb.DeleteChannelRequest{ChannelID: channel.ID}, st, &bufferConn{}, handler)
			}
			srv.flushServerState(st, handler)
			ack, err := protocol.ReadControlMessage(peer)
			if err != nil || ack.ChannelJoinResponse == nil || !ack.ChannelJoinResponse.Success {
				t.Fatalf("first frame must be the join ACK: %+v, %v", ack, err)
			}
			state, err := protocol.ReadControlMessage(peer)
			if err != nil || state.ServerStateEvent == nil {
				t.Fatalf("eviction must publish state after ACK: %+v, %v", state, err)
			}
			for _, listed := range state.ServerStateEvent.Channels {
				if listed.ID == channel.ID {
					t.Fatal("deleted channel remained in authoritative snapshot")
				}
			}
			snapshot, ok := srv.sessions.GetSnapshot(member.ID)
			if !ok || snapshot.ChannelID != 0 || srv.channels.ChannelOf(member.ID) != 0 {
				t.Fatal("channel removal retained server membership")
			}
			if _, active := srv.screenShare.ActiveForChannel(channel.ID); active {
				t.Fatal("channel removal retained screen share")
			}
		})
	}
}
