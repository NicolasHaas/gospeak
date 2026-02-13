package audio

import (
	"log/slog"
	"sync"

	"github.com/gordonklaus/portaudio"
)

var (
	preInitOnce sync.Once
	preInitDone chan struct{} = make(chan struct{})
)

// PreInitAudio starts PortAudio initialization in the background.
// Call this early (e.g. at app startup) so the slow Windows device
// enumeration happens while the user types in the connect form.
// NewCaptureDevice will wait for it to finish before proceeding.
func PreInitAudio() {
	preInitOnce.Do(func() {
		go func() {
			slog.Debug("pre-initializing PortAudio...")
			if err := portaudio.Initialize(); err != nil {
				slog.Error("pre-init portaudio failed", "err", err)
			}
			slog.Debug("PortAudio pre-init complete")
			close(preInitDone)
		}()
	})
}

// WaitPreInit blocks until the background PreInitAudio completes.
// If PreInitAudio was never called, it triggers it now (blocking).
func WaitPreInit() {
	PreInitAudio() // ensure the init goroutine has been launched
	<-preInitDone
}

// DeviceEntry holds basic info about an audio device.
type DeviceEntry struct {
	Name       string
	MaxInputs  int
	MaxOutputs int
	IsDefault  bool
}

// ListInputDevices returns all available audio input devices.
func ListInputDevices() ([]DeviceEntry, error) {
	if err := portaudio.Initialize(); err != nil {
		return nil, err
	}
	defer func() { _ = portaudio.Terminate() }()

	defaultIn, _ := portaudio.DefaultInputDevice()
	devices, err := portaudio.Devices()
	if err != nil {
		return nil, err
	}

	var result []DeviceEntry
	for _, d := range devices {
		if d.MaxInputChannels > 0 {
			entry := DeviceEntry{
				Name:       d.Name,
				MaxInputs:  d.MaxInputChannels,
				MaxOutputs: d.MaxOutputChannels,
			}
			if defaultIn != nil && d.Name == defaultIn.Name {
				entry.IsDefault = true
			}
			result = append(result, entry)
		}
	}
	return result, nil
}

// ListOutputDevices returns all available audio output devices.
func ListOutputDevices() ([]DeviceEntry, error) {
	if err := portaudio.Initialize(); err != nil {
		return nil, err
	}
	defer func() { _ = portaudio.Terminate() }()

	defaultOut, _ := portaudio.DefaultOutputDevice()
	devices, err := portaudio.Devices()
	if err != nil {
		return nil, err
	}

	var result []DeviceEntry
	for _, d := range devices {
		if d.MaxOutputChannels > 0 {
			entry := DeviceEntry{
				Name:       d.Name,
				MaxInputs:  d.MaxInputChannels,
				MaxOutputs: d.MaxOutputChannels,
			}
			if defaultOut != nil && d.Name == defaultOut.Name {
				entry.IsDefault = true
			}
			result = append(result, entry)
		}
	}
	return result, nil
}

// FindDevice returns the *portaudio.DeviceInfo matching by name, or nil.
func FindDevice(name string) *portaudio.DeviceInfo {
	devices, err := portaudio.Devices()
	if err != nil {
		return nil
	}
	for _, d := range devices {
		if d.Name == name {
			return d
		}
	}
	return nil
}
