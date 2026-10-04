package audio

import (
	"testing"

	"github.com/gordonklaus/portaudio"
)

func TestFindDeviceDirection(t *testing.T) {
	output := &portaudio.DeviceInfo{Name: "Headset", MaxOutputChannels: 2}
	input := &portaudio.DeviceInfo{Name: "Headset", MaxInputChannels: 1}
	devices := []*portaudio.DeviceInfo{output, input}
	if got := findDevice(devices, "Headset", true); got != input {
		t.Fatalf("input selected %v", got)
	}
	if got := findDevice(devices, "Headset", false); got != output {
		t.Fatalf("output selected %v", got)
	}
	if got := findDevice(devices, "absent", true); got != nil {
		t.Fatal("missing device matched")
	}
	if got := findDevice([]*portaudio.DeviceInfo{output}, "Headset", true); got != nil {
		t.Fatal("output-only device selected for input")
	}
}
