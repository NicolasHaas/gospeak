// Package audio provides audio capture, playback, Opus encoding/decoding, and VAD.
package audio

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/gordonklaus/portaudio"
)

// CaptureDevice captures PCM audio from an input device.
type CaptureDevice struct {
	stream     *portaudio.Stream
	sampleRate float64
	frameSize  int
	buffer     []int16
	deviceName string // empty = default
	mu         sync.Mutex
	running    bool
}

// NewCaptureDevice creates a new audio capture device.
// frameSize is the number of samples per frame (e.g., 960 for 20ms at 48kHz).
// deviceName may be empty to use the system default.
func NewCaptureDevice(sampleRate float64, frameSize int, deviceName ...string) (*CaptureDevice, error) {
	// Wait for the background PreInitAudio to finish (blocks until ready)
	WaitPreInit()

	dn := ""
	if len(deviceName) > 0 {
		dn = deviceName[0]
	}
	return &CaptureDevice{
		sampleRate: sampleRate,
		frameSize:  frameSize,
		buffer:     make([]int16, frameSize),
		deviceName: dn,
	}, nil
}

// Start begins audio capture. Call ReadFrame() to get captured audio.
func (c *CaptureDevice) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Find input device
	var defaultInput *portaudio.DeviceInfo
	if c.deviceName != "" {
		defaultInput = FindDevice(c.deviceName)
	}
	if defaultInput == nil {
		var err error
		defaultInput, err = portaudio.DefaultInputDevice()
		if err != nil {
			return fmt.Errorf("audio: no input device: %w", err)
		}
	}

	// Build input-only stream parameters
	params := portaudio.LowLatencyParameters(defaultInput, nil)
	params.Input.Channels = 1
	params.Output.Device = nil
	params.Output.Channels = 0
	params.SampleRate = c.sampleRate
	params.FramesPerBuffer = c.frameSize

	stream, err := portaudio.OpenStream(params, c.buffer)
	if err != nil {
		return fmt.Errorf("audio: open capture stream: %w", err)
	}

	if err := stream.Start(); err != nil {
		_ = stream.Close()
		return fmt.Errorf("audio: start capture: %w", err)
	}

	c.stream = stream
	c.running = true
	slog.Debug("audio capture started", "device", defaultInput.Name, "rate", c.sampleRate)
	return nil
}

// ReadFrame reads one frame of PCM audio. Blocks until a frame is available.
// Returns a copy of the frame buffer.
func (c *CaptureDevice) ReadFrame() ([]int16, error) {
	if err := c.stream.Read(); err != nil {
		return nil, fmt.Errorf("audio: read frame: %w", err)
	}
	frame := make([]int16, len(c.buffer))
	copy(frame, c.buffer)
	return frame, nil
}

// Stop stops audio capture.
func (c *CaptureDevice) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running {
		return nil
	}
	c.running = false

	if c.stream != nil {
		_ = c.stream.Stop()
		_ = c.stream.Close()
	}
	return nil
}

// Close releases all audio resources.
func (c *CaptureDevice) Close() error {
	_ = c.Stop()
	return portaudio.Terminate()
}
