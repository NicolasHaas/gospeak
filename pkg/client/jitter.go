package client

import (
	"sync"
	"time"
)

const (
	voiceFrameDuration = 20 * time.Millisecond
	defaultJitterDelay = voiceFrameDuration
	maxJitterFrames    = 15
)

type jitterClock interface {
	Now() time.Time
}

type realJitterClock struct{}

func (realJitterClock) Now() time.Time { return time.Now() }

// JitterBuffer orders incoming voice packets and releases at most one frame on
// each 20 ms playout deadline. A nil payload indicates packet loss and asks the
// decoder to perform packet-loss concealment.
type JitterBuffer struct {
	mu sync.Mutex

	clock         jitterClock
	playoutDelay  time.Duration
	frameDuration time.Duration
	maxFrames     uint32
	frames        map[uint32][]byte
	nextSeq       uint32
	nextPlayout   time.Time
	ready         bool
	playing       bool
	started       bool
}

// NewJitterBuffer creates a bounded jitter buffer with a 20 ms playout delay.
func NewJitterBuffer() *JitterBuffer {
	return newJitterBuffer(realJitterClock{}, defaultJitterDelay, voiceFrameDuration, maxJitterFrames)
}

func newJitterBuffer(clock jitterClock, playoutDelay, frameDuration time.Duration, maxFrames uint32) *JitterBuffer {
	return &JitterBuffer{
		clock:         clock,
		playoutDelay:  playoutDelay,
		frameDuration: frameDuration,
		maxFrames:     maxFrames,
		frames:        make(map[uint32][]byte),
	}
}

// Push adds a packet. Duplicate and already-played packets are ignored. A jump
// beyond the bounded reordering window starts a fresh playout window instead
// of growing the buffer or emitting a long run of PLC frames.
func (jb *JitterBuffer) Push(seqNum uint32, payload []byte) {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	now := jb.clock.Now()
	if !jb.ready {
		jb.startAt(seqNum, now)
	} else if len(jb.frames) == 0 && !jb.playing {
		jb.nextPlayout = now.Add(jb.playoutDelay)
	}

	distance := seqNum - jb.nextSeq
	switch {
	case sequenceBefore(seqNum, jb.nextSeq):
		if jb.started || uint32(jb.nextSeq-seqNum) >= jb.maxFrames {
			return
		}
		// Before playout starts, permit a bounded packet to move the initial
		// cursor backwards. This is the normal N+1-before-N reorder case.
		jb.nextSeq = seqNum
	case distance >= jb.maxFrames:
		jb.startAt(seqNum, now)
	}

	if _, duplicate := jb.frames[seqNum]; duplicate {
		return
	}
	jb.frames[seqNum] = append([]byte(nil), payload...)
}

// Pop returns the frame due at the current playout deadline. It never advances
// merely because a later packet arrived; loss is declared only when the fixed
// deadline for the missing sequence has elapsed.
func (jb *JitterBuffer) Pop() ([]byte, uint32, bool) {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	if !jb.ready || jb.clock.Now().Before(jb.nextPlayout) {
		return nil, 0, false
	}

	seq := jb.nextSeq
	if frame, ok := jb.frames[seq]; ok {
		delete(jb.frames, seq)
		jb.advance()
		return frame, seq, true
	}

	if !jb.hasFutureFrame() {
		// The speaker may have stopped due to VAD. Do not synthesize silence
		// forever; the next packet starts a fresh jitter window.
		jb.playing = false
		return nil, 0, false
	}

	jb.advance()
	return nil, seq, true
}

// Reset clears the jitter buffer.
func (jb *JitterBuffer) Reset() {
	jb.mu.Lock()
	defer jb.mu.Unlock()
	jb.frames = make(map[uint32][]byte)
	jb.ready = false
	jb.playing = false
	jb.started = false
	jb.nextPlayout = time.Time{}
}

func (jb *JitterBuffer) startAt(seqNum uint32, now time.Time) {
	clear(jb.frames)
	jb.nextSeq = seqNum
	jb.nextPlayout = now.Add(jb.playoutDelay)
	jb.ready = true
	jb.playing = false
	jb.started = false
}

func (jb *JitterBuffer) advance() {
	jb.nextSeq++
	jb.nextPlayout = jb.nextPlayout.Add(jb.frameDuration)
	jb.playing = true
	jb.started = true
}

func (jb *JitterBuffer) hasFutureFrame() bool {
	for seq := range jb.frames {
		if sequenceBefore(jb.nextSeq, seq) {
			return true
		}
	}
	return false
}

// sequenceBefore compares uint32 sequence numbers within the bounded jitter
// window. A distance of exactly half the sequence space is intentionally not
// ordered; valid buffered distances are far smaller.
func sequenceBefore(a, b uint32) bool {
	distance := b - a
	return distance != 0 && distance < uint32(1)<<31
}

func (jb *JitterBuffer) buffered() int {
	jb.mu.Lock()
	defer jb.mu.Unlock()
	return len(jb.frames)
}
