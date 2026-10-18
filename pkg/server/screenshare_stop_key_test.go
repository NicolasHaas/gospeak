package server

import (
	"reflect"
	"testing"
	"time"

	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestScreenShareManager_StopEventsHideEncryptionKey(t *testing.T) {
	for _, path := range []string{"stop", "leave", "expire"} {
		t.Run(path, func(t *testing.T) {
			mgr := NewScreenShareManager()
			started, err := mgr.Start(1, 10, 20, "alice", 800, 600)
			if err != nil {
				t.Fatal(err)
			}
			if len(started.EncryptionKey) == 0 {
				t.Fatal("start event has no key")
			}
			if _, err := mgr.Subscribe(1, 30); err == nil {
				t.Fatal("unauthorized viewer subscribed")
			}
			shared, _, err := mgr.ShareWithViewers(10, []uint32{30})
			if err != nil || !reflect.DeepEqual(shared, started) {
				t.Fatalf("authorized event changed: %v", err)
			}
			if _, err := mgr.Subscribe(1, 30); err != nil {
				t.Fatal(err)
			}

			var stopped *pb.ScreenShareEvent
			switch path {
			case "stop":
				stopped, _ = mgr.StopBySession(10)
			case "leave":
				stopped, _ = mgr.LeaveChannel(10, 1)
			case "expire":
				mgr.lastFrameAt[10] = time.Now().Add(-time.Minute)
				expired := mgr.ExpireInactive(time.Second)
				if len(expired) != 1 {
					t.Fatalf("expired events = %d, want 1", len(expired))
				}
				stopped = expired[0]
			}
			want := *started
			want.Active = false
			want.Viewers = 0
			want.EncryptionKey = nil
			if !reflect.DeepEqual(stopped, &want) {
				t.Fatal("stop event must preserve share metadata without the key or viewers")
			}
			if len(started.EncryptionKey) == 0 || len(shared.EncryptionKey) == 0 {
				t.Fatal("stop mutated an already issued start or authorized event")
			}
		})
	}
}
