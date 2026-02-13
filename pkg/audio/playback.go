package audio

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/gordonklaus/portaudio"
)

// PlaybackDevice plays PCM audio to an output device.
type PlaybackDevice struct {
	stream     *portaudio.Stream
	sampleRate float64
	frameSize  int
	buffer     []int16
	deviceName string // empty = default
	mu         sync.Mutex
	running    bool
}

// NewPlaybackDevice creates a new audio playback device.
// deviceName may be empty to use the system default.
func NewPlaybackDevice(sampleRate float64, frameSize int, deviceName ...string) (*PlaybackDevice, error) {
	dn := ""
	if len(deviceName) > 0 {
		dn = deviceName[0]
	}
	return &PlaybackDevice{
		sampleRate: sampleRate,
		frameSize:  frameSize,
		buffer:     make([]int16, frameSize),
		deviceName: dn,
	}, nil
}

// Start begins audio playback.
func (p *PlaybackDevice) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var defaultOutput *portaudio.DeviceInfo
	if p.deviceName != "" {
		defaultOutput = FindDevice(p.deviceName)
	}
	if defaultOutput == nil {
		var err error
		defaultOutput, err = portaudio.DefaultOutputDevice()
		if err != nil {
			return fmt.Errorf("audio: no output device: %w", err)
		}
	}

	// Build output-only stream parameters
	params := portaudio.LowLatencyParameters(nil, defaultOutput)
	params.Output.Channels = 1
	params.Input.Device = nil
	params.Input.Channels = 0
	params.SampleRate = p.sampleRate
	params.FramesPerBuffer = p.frameSize

	stream, err := portaudio.OpenStream(params, p.buffer)
	if err != nil {
		return fmt.Errorf("audio: open playback stream: %w", err)
	}

	if err := stream.Start(); err != nil {
		_ = stream.Close()
		return fmt.Errorf("audio: start playback: %w", err)
	}

	p.stream = stream
	p.running = true
	slog.Debug("audio playback started", "device", defaultOutput.Name, "rate", p.sampleRate)
	return nil
}

// WriteFrame writes one frame of PCM audio to the output. Blocks until written.
func (p *PlaybackDevice) WriteFrame(frame []int16) error {
	if len(frame) != len(p.buffer) {
		return fmt.Errorf("audio: frame size mismatch: got %d, want %d", len(frame), len(p.buffer))
	}
	copy(p.buffer, frame)
	if err := p.stream.Write(); err != nil {
		return fmt.Errorf("audio: write frame: %w", err)
	}
	return nil
}

// Stop stops audio playback.
func (p *PlaybackDevice) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return nil
	}
	p.running = false

	if p.stream != nil {
		_ = p.stream.Stop()
		_ = p.stream.Close()
	}
	return nil
}

// MixFrames mixes multiple PCM frames into one by summing with clipping prevention.
func MixFrames(frames [][]int16, frameSize int) []int16 {
	if len(frames) == 0 {
		return make([]int16, frameSize)
	}
	if len(frames) == 1 {
		return frames[0]
	}

	mixed := make([]int16, frameSize)
	for i := 0; i < frameSize; i++ {
		var sum int32
		for _, frame := range frames {
			if i < len(frame) {
				sum += int32(frame[i])
			}
		}
		// Clamp to int16 range
		if sum > 32767 {
			sum = 32767
		} else if sum < -32768 {
			sum = -32768
		}
		mixed[i] = int16(sum) //nolint:gosec // sum is clamped to int16 range above
	}
	return mixed
}
