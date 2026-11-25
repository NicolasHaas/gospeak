package server

import (
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type stopShareOnCloseConn struct {
	nopConn
	stop func()
}

func (c *stopShareOnCloseConn) Close() error {
	c.stop()
	return nil
}

func TestScreenShareHandlersSkipMissingPublicEvent(t *testing.T) {
	for _, action := range []string{"start", "subscribe"} {
		t.Run(action, func(t *testing.T) {
			srv, _, handler := newTestServerWithConfig(t, func(cfg *Config) { cfg.EnableScreenShare = true })
			sharer := mustCreateSession(t, srv.sessions, 1, "sharer", model.RoleUser)
			actor := sharer
			if action == "subscribe" {
				actor = mustCreateSession(t, srv.sessions, 2, "viewer", model.RoleUser)
				if _, err := srv.screenShare.Start(1, sharer.ID, sharer.UserID, sharer.Username, 100, 100); err != nil {
					t.Fatal(err)
				}
				if _, _, err := srv.screenShare.ShareWithViewers(sharer.ID, []uint32{actor.ID}); err != nil {
					t.Fatal(err)
				}
			}
			observer := mustCreateSession(t, srv.sessions, 3, "observer", model.RoleUser)
			srv.sessions.SetChannel(actor.ID, 1)
			srv.channels.Join(actor.ID, 1)
			// Queue overflow closes the actor and stops the share synchronously
			// after Start/Subscribe succeeds, before the public snapshot is read.
			client := newControlClient(actor.ID, &stopShareOnCloseConn{stop: func() {
				srv.screenShare.StopBySession(sharer.ID)
				srv.channels.Join(observer.ID, 1)
			}})
			handler.connMap[actor.ID] = client
			for range controlSendQueueSize {
				if err := client.send(&pb.ControlMessage{ScreenShareEvent: &pb.ScreenShareEvent{ChannelID: 1}}); err != nil {
					t.Fatal(err)
				}
			}
			recipient := newControlClient(observer.ID, &nopConn{})
			handler.connMap[observer.ID] = recipient

			if action == "start" {
				srv.handleScreenShareStart(handler, actor.ID, &pb.ScreenShareStartRequest{Width: 100, Height: 100}, &bufferConn{})
			} else {
				srv.handleScreenShareSubscribe(handler, actor.ID, &pb.ScreenShareSubscribeRequest{ChannelID: 1}, &bufferConn{})
			}
			if _, ok := srv.screenShare.PublicEvent(1); ok {
				t.Fatal("share was not stopped before the public lookup")
			}
			select {
			case queued := <-recipient.sendQueue:
				t.Fatalf("unexpected broadcast after share stopped: %#v", queued.message)
			default:
			}
		})
	}
}
