package client

import (
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/audio"
)

func TestPlayoutNativePLCLossAndRecovery(t *testing.T) {
	enc, err := audio.NewEncoder()
	if err != nil {
		t.Fatal(err)
	}
	dec, err := audio.NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]int16, voiceFrameSamples)
	for i := range frame {
		frame[i] = int16((i%48)*200 - 4800)
	}
	seed, err := enc.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := enc.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeJitterClock{now: time.Unix(0, 0)}
	jb := newTestJitterBuffer(clock)
	jb.Push(1, seed)
	jb.Push(5, recovery)
	e := NewEngine()
	e.now = clock.Now
	e.decoders[1] = dec
	e.jitterBufs[1] = jb
	e.speakerLastSeen[1] = clock.Now()
	player := &recordingPlayer{}
	clock.Advance(defaultJitterDelay)
	for tick := 1; tick <= 5; tick++ {
		e.playJitterFrames(player)
		if len(player.frames) != tick {
			t.Fatalf("tick %d: playback writes=%d, want %d (seed, three losses, recovery)", tick, len(player.frames), tick)
		}
		if got := len(player.frames[tick-1]); got != voiceFrameSamples {
			t.Fatalf("tick %d: samples=%d, want %d", tick, got, voiceFrameSamples)
		}
		clock.Advance(voiceFrameDuration)
	}
	e.playJitterFrames(player)
	if len(player.frames) != 5 {
		t.Fatal("idle speaker emitted another frame")
	}
}
