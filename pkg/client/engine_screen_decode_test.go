package client

import (
	"image"
	"testing"

	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestDecodedScreenFrameRequiresCurrentAuthorization(t *testing.T) {
	for _, change := range []string{"none", "generation", "stop", "cipher", "share"} {
		t.Run(change, func(t *testing.T) {
			g := newConnectionGeneration()
			defer g.cancel()
			e := NewEngine()
			e.generation, e.state = g, StateConnected
			cipher, err := gospeakCrypto.NewVoiceCipher(make([]byte, 16))
			if err != nil {
				t.Fatal(err)
			}
			share := &pb.ScreenShareEvent{Active: true, SessionID: 10}
			e.screenCipher, e.activeScreenShare = cipher, share
			e.callbackQueueRunning = true
			e.OnScreenFrame = func(image.Image) {}
			switch change {
			case "generation":
				replacement := newConnectionGeneration()
				defer replacement.cancel()
				e.generation = replacement
			case "stop":
				e.activeScreenShare = nil
			case "cipher":
				e.screenCipher, err = gospeakCrypto.NewVoiceCipher(make([]byte, 16))
				if err != nil {
					t.Fatal(err)
				}
			case "share":
				e.activeScreenShare = &pb.ScreenShareEvent{Active: true, SessionID: 10}
			}
			e.publishDecodedScreenFrame(g, cipher, share, image.NewRGBA(image.Rect(0, 0, 2, 2)))
			want := 0
			if change == "none" {
				want = 1
			}
			if len(e.callbackQueue) != want {
				t.Fatalf("%s queued %d frames, want %d", change, len(e.callbackQueue), want)
			}
		})
	}
}
