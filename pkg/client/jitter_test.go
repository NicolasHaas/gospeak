package client

import (
	"bytes"
	"testing"
	"time"
)

type fakeJitterClock struct {
	now time.Time
}

func (c *fakeJitterClock) Now() time.Time { return c.now }

func (c *fakeJitterClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestJitterBuffer(clock *fakeJitterClock) *JitterBuffer {
	return newJitterBuffer(clock, defaultJitterDelay, voiceFrameDuration, maxJitterFrames)
}

func TestJitterBufferWaitsForReorderedPacket(t *testing.T) {
	clock := &fakeJitterClock{now: time.Unix(0, 0)}
	jb := newTestJitterBuffer(clock)

	jb.Push(11, []byte("eleven"))
	jb.Push(10, []byte("ten"))
	if _, _, ok := jb.Pop(); ok {
		t.Fatal("Pop() before playout deadline succeeded")
	}

	clock.Advance(defaultJitterDelay)
	assertJitterFrame(t, jb, 10, []byte("ten"))
	clock.Advance(voiceFrameDuration)
	assertJitterFrame(t, jb, 11, []byte("eleven"))
}

func TestJitterBufferEmitsPLCAfterLossDeadline(t *testing.T) {
	clock := &fakeJitterClock{now: time.Unix(0, 0)}
	jb := newTestJitterBuffer(clock)

	jb.Push(20, []byte("twenty"))
	jb.Push(22, []byte("twenty-two"))
	clock.Advance(defaultJitterDelay)
	assertJitterFrame(t, jb, 20, []byte("twenty"))

	clock.Advance(voiceFrameDuration)
	assertJitterFrame(t, jb, 21, nil)
	clock.Advance(voiceFrameDuration)
	assertJitterFrame(t, jb, 22, []byte("twenty-two"))
}

func TestJitterBufferDropsDuplicateAndLatePackets(t *testing.T) {
	clock := &fakeJitterClock{now: time.Unix(0, 0)}
	jb := newTestJitterBuffer(clock)

	jb.Push(30, []byte("first"))
	jb.Push(30, []byte("duplicate"))
	clock.Advance(defaultJitterDelay)
	assertJitterFrame(t, jb, 30, []byte("first"))

	clock.Advance(voiceFrameDuration)
	if _, _, ok := jb.Pop(); ok {
		t.Fatal("idle buffer unexpectedly emitted PLC")
	}
	jb.Push(30, []byte("late"))
	if got := jb.buffered(); got != 0 {
		t.Fatalf("buffered frames after late packet = %d, want 0", got)
	}
}

func TestJitterBufferHandlesSequenceWrap(t *testing.T) {
	clock := &fakeJitterClock{now: time.Unix(0, 0)}
	jb := newTestJitterBuffer(clock)

	jb.Push(^uint32(0), []byte("max"))
	jb.Push(0, []byte("zero"))
	clock.Advance(defaultJitterDelay)
	assertJitterFrame(t, jb, ^uint32(0), []byte("max"))
	clock.Advance(voiceFrameDuration)
	assertJitterFrame(t, jb, 0, []byte("zero"))
}

func TestJitterBufferBoundsAndResyncsLargeJump(t *testing.T) {
	clock := &fakeJitterClock{now: time.Unix(0, 0)}
	jb := newTestJitterBuffer(clock)

	jb.Push(1, []byte("old"))
	jb.Push(1000, []byte("new"))
	if got := jb.buffered(); got > maxJitterFrames {
		t.Fatalf("buffered frames = %d, want at most %d", got, maxJitterFrames)
	}

	clock.Advance(defaultJitterDelay)
	assertJitterFrame(t, jb, 1000, []byte("new"))
}

func assertJitterFrame(t *testing.T, jb *JitterBuffer, wantSeq uint32, wantPayload []byte) {
	t.Helper()
	payload, seq, ok := jb.Pop()
	if !ok {
		t.Fatalf("Pop() for sequence %d was not ready", wantSeq)
	}
	if seq != wantSeq {
		t.Fatalf("Pop() sequence = %d, want %d", seq, wantSeq)
	}
	if !bytes.Equal(payload, wantPayload) {
		t.Fatalf("Pop() payload = %q, want %q", payload, wantPayload)
	}
}
