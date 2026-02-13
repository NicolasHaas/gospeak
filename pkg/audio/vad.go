package audio

import (
	"math"
	"sync"
)

// VAD implements Voice Activity Detection using RMS energy analysis.
type VAD struct {
	mu        sync.RWMutex
	threshold float64 // RMS threshold for voice detection
	holdTime  int     // frames to keep transmitting after voice stops
	holdCount int     // current hold counter

	// Pre-buffer: ring buffer of recent frames to avoid clipping word starts
	preBuffer  [][]int16
	preBufSize int
	preBufIdx  int

	active bool // current voice activity state
}

// NewVAD creates a new Voice Activity Detector.
// threshold: RMS energy threshold (typical: 300-1000 for int16 PCM)
// holdFrames: number of frames to keep active after voice stops (e.g., 15 = 300ms at 20ms/frame)
// preBufferFrames: number of frames to pre-buffer (e.g., 3 = 60ms)
func NewVAD(threshold float64, holdFrames, preBufferFrames int) *VAD {
	return &VAD{
		threshold:  threshold,
		holdTime:   holdFrames,
		preBufSize: preBufferFrames,
		preBuffer:  make([][]int16, preBufferFrames),
	}
}

// Process analyzes a PCM frame and returns true if voice is detected.
// Also stores the frame in the pre-buffer.
func (v *VAD) Process(pcm []int16) bool {
	rms := computeRMS(pcm)

	v.mu.Lock()
	defer v.mu.Unlock()

	// Store in pre-buffer (ring buffer)
	if v.preBufSize > 0 {
		frameCopy := make([]int16, len(pcm))
		copy(frameCopy, pcm)
		v.preBuffer[v.preBufIdx%v.preBufSize] = frameCopy
		v.preBufIdx++
	}

	if rms > v.threshold {
		v.holdCount = v.holdTime
		v.active = true
		return true
	}

	if v.holdCount > 0 {
		v.holdCount--
		return true
	}

	v.active = false
	return false
}

// IsActive returns the current voice activity state without processing.
func (v *VAD) IsActive() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.active
}

// PreBufferedFrames returns the pre-buffered frames (for prepending when voice starts).
// Returns frames in chronological order.
func (v *VAD) PreBufferedFrames() [][]int16 {
	v.mu.RLock()
	defer v.mu.RUnlock()

	var frames [][]int16
	count := v.preBufSize
	if v.preBufIdx < count {
		count = v.preBufIdx
	}

	start := v.preBufIdx - count
	for i := start; i < v.preBufIdx; i++ {
		frame := v.preBuffer[i%v.preBufSize]
		if frame != nil {
			frames = append(frames, frame)
		}
	}
	return frames
}

// SetThreshold updates the VAD threshold.
func (v *VAD) SetThreshold(threshold float64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.threshold = threshold
}

// GetRMS computes the RMS of a frame without updating internal state. Useful for VU meters.
func GetRMS(pcm []int16) float64 {
	return computeRMS(pcm)
}

// computeRMS calculates the Root Mean Square of a PCM frame.
func computeRMS(pcm []int16) float64 {
	if len(pcm) == 0 {
		return 0
	}
	var sum float64
	for _, s := range pcm {
		f := float64(s)
		sum += f * f
	}
	return math.Sqrt(sum / float64(len(pcm)))
}
