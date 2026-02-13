package client

import (
	"sync"
)

const (
	jitterBufSize  = 5  // frames (~100ms at 20ms/frame)
	maxJitterDelay = 10 // max frames to wait before considering packet lost
)

// JitterBuffer orders incoming voice packets and handles packet loss.
type JitterBuffer struct {
	mu      sync.Mutex
	frames  map[uint32][]byte // seqNum -> encrypted payload
	nextSeq uint32
	ready   bool
}

// NewJitterBuffer creates a new jitter buffer.
func NewJitterBuffer() *JitterBuffer {
	return &JitterBuffer{
		frames: make(map[uint32][]byte),
	}
}

// Push adds a packet to the jitter buffer.
func (jb *JitterBuffer) Push(seqNum uint32, payload []byte) {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	if !jb.ready {
		jb.nextSeq = seqNum
		jb.ready = true
	}

	// Store the frame
	data := make([]byte, len(payload))
	copy(data, payload)
	jb.frames[seqNum] = data

	// Limit buffer size to prevent memory growth
	if len(jb.frames) > jitterBufSize*3 {
		jb.cleanup()
	}
}

// Pop returns the next frame in sequence order.
// Returns (payload, seqNum, ok). If the next frame isn't available,
// checks if we should skip it (lost packet).
func (jb *JitterBuffer) Pop() ([]byte, uint32, bool) {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	if !jb.ready {
		return nil, 0, false
	}

	// Check if we have the next expected frame
	if frame, ok := jb.frames[jb.nextSeq]; ok {
		seq := jb.nextSeq
		delete(jb.frames, jb.nextSeq)
		jb.nextSeq++
		return frame, seq, true
	}

	// Check if we've waited too long (packet lost)
	// Look ahead to see if later packets exist
	for i := uint32(1); i <= maxJitterDelay; i++ {
		if _, ok := jb.frames[jb.nextSeq+i]; ok {
			// Later packet exists, the current one is likely lost
			seq := jb.nextSeq
			jb.nextSeq++
			return nil, seq, true // nil payload = packet lost, use PLC
		}
	}

	return nil, 0, false // not enough data yet
}

// Reset clears the jitter buffer.
func (jb *JitterBuffer) Reset() {
	jb.mu.Lock()
	defer jb.mu.Unlock()
	jb.frames = make(map[uint32][]byte)
	jb.ready = false
}

func (jb *JitterBuffer) cleanup() {
	// Remove frames that are too old
	for seq := range jb.frames {
		if seqDiff(seq, jb.nextSeq) > jitterBufSize*3 {
			delete(jb.frames, seq)
		}
	}
}

// seqDiff computes the distance between two sequence numbers,
// handling uint32 wraparound.
func seqDiff(a, b uint32) uint32 {
	if a >= b {
		return a - b
	}
	return b - a
}
