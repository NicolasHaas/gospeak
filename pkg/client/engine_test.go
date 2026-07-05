package client

import (
	"errors"
	"image"
	"testing"
	"time"

	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestResolveAdvertisedAddr(t *testing.T) {
	tests := []struct {
		name           string
		controlAddr    string
		advertisedAddr string
		defaultPort    string
		want           string
		wantErr        bool
	}{
		{"empty advertised uses control host", "example.com:9600", "", "9603", "example.com:9603", false},
		{"wildcard host uses control host", "127.0.0.1:9600", "0.0.0.0:9603", "9603", "127.0.0.1:9603", false},
		{"explicit host preserved", "127.0.0.1:9600", "10.0.0.2:9603", "9603", "10.0.0.2:9603", false},
		{"bad control address", "bad", "", "9603", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAdvertisedAddr(tt.controlAddr, tt.advertisedAddr, tt.defaultPort)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveAdvertisedAddr() err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("resolveAdvertisedAddr() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClearScreenShareStateResetsCallbacks(t *testing.T) {
	e := NewEngine()
	e.activeScreenShare = &pb.ScreenShareEvent{Active: true, SessionID: 10, ChannelID: 1}
	e.screenSharePending = true

	var eventCalled bool
	var frameCleared bool
	e.OnScreenShareEvent = func(event *pb.ScreenShareEvent) {
		if event == nil {
			eventCalled = true
		}
	}
	e.OnScreenFrame = func(_ image.Image) {
		frameCleared = true
	}

	e.clearScreenShareState()

	if e.activeScreenShare != nil {
		t.Fatalf("activeScreenShare = %v, want nil", e.activeScreenShare)
	}
	if e.screenSharePending {
		t.Fatalf("screenSharePending = true, want false")
	}
	if !eventCalled {
		t.Fatalf("OnScreenShareEvent(nil) was not called")
	}
	if !frameCleared {
		t.Fatalf("OnScreenFrame(nil) was not called")
	}
}

func TestStartScreenShareRejectsWhenAnotherShareIsActive(t *testing.T) {
	e := NewEngine()
	e.control = &ControlClient{}
	e.channelID = 1
	e.sessionID = 10
	e.activeScreenShare = &pb.ScreenShareEvent{Active: true, SessionID: 20, ChannelID: 1}

	err := e.StartScreenShare(0)
	if err == nil {
		t.Fatalf("StartScreenShare() err = nil, want error")
	}
	if err.Error() != "another user is already sharing their screen in this channel" {
		t.Fatalf("StartScreenShare() err = %q, want %q", err.Error(), "another user is already sharing their screen in this channel")
	}
}

func TestStartScreenShareRejectsWhenAlreadyPending(t *testing.T) {
	e := NewEngine()
	e.control = &ControlClient{}
	e.channelID = 1
	e.screenSharePending = true

	err := e.StartScreenShare(0)
	if err == nil {
		t.Fatalf("StartScreenShare() err = nil, want error")
	}
	if err.Error() != "screen share already active" {
		t.Fatalf("StartScreenShare() err = %q, want %q", err.Error(), "screen share already active")
	}
}

func TestStartScreenShareTimesOutPreparingCapture(t *testing.T) {
	e := NewEngine()
	e.control = &ControlClient{}
	e.channelID = 1
	e.screenCaptureWait = 10 * time.Millisecond
	e.captureScreenFn = func(displayIndex int) (image.Image, error) {
		select {}
	}

	err := e.StartScreenShare(0)
	if err == nil {
		t.Fatalf("StartScreenShare() err = nil, want error")
	}
	if !errors.Is(err, errScreenShareCaptureTimedOut) {
		t.Fatalf("StartScreenShare() err = %v, want screen capture timeout", err)
	}
	if e.screenSharePending {
		t.Fatalf("screenSharePending = true, want false")
	}
}

func TestWatchScreenShareStartClearsPending(t *testing.T) {
	e := NewEngine()
	e.screenSharePending = true
	e.screenShareAttempt = 1

	errCh := make(chan error, 1)
	e.OnError = func(err error) {
		errCh <- err
	}

	go e.watchScreenShareStart(1, 10*time.Millisecond)

	select {
	case err := <-errCh:
		if !errors.Is(err, errScreenShareStartTimedOut) {
			t.Fatalf("OnError() = %v, want start-timeout error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchScreenShareStart() did not report timeout")
	}

	if e.screenSharePending {
		t.Fatalf("screenSharePending = true, want false")
	}
}
