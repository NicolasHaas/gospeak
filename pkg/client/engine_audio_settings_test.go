package client

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/audio"
)

type volumeCapturer struct {
	sequenceCapturer
	engine     *Engine
	generation *connectionGeneration
	volumes    []float64
}

func (c *volumeCapturer) ReadFrame() ([]int16, error) {
	if len(c.volumes) == 0 {
		c.generation.cancel()
		return nil, errors.New("finished")
	}
	c.engine.SetAudioVolumes(c.volumes[0], 100)
	c.volumes = c.volumes[1:]
	return c.sequenceCapturer.ReadFrame()
}

type volumeEncoder struct{ frames [][]int16 }

func (e *volumeEncoder) Encode(pcm []int16) ([]byte, error) {
	e.frames = append(e.frames, append([]int16(nil), pcm...))
	return nil, errors.New("skip network write")
}

func TestCaptureVolumeAppliesLiveBeforeMeterVADAndEncoding(t *testing.T) {
	for _, volumes := range [][]float64{{0}, {100, 50, 0}} {
		e := NewEngine()
		g := setTestGeneration(e)
		e.channelID = 1
		encoder := &volumeEncoder{}
		g.voice = &VoiceClient{}
		g.audio = &audioResources{
			capture: &volumeCapturer{sequenceCapturer: sequenceCapturer{frame: []int16{1000}}, engine: e, generation: g, volumes: volumes},
			encoder: encoder,
		}
		levels := make(chan float64, 3)
		e.OnRMSLevel = func(level float64) { levels <- level }
		e.captureLoop(g)
		if len(volumes) == 1 {
			if len(encoder.frames) != 0 || e.vad.IsActive() {
				t.Fatal("zero input activated VAD or encoded speech")
			}
		} else {
			if len(encoder.frames) != 3 {
				t.Fatalf("encoded %d frames", len(encoder.frames))
			}
			for i, want := range []int16{1000, 500, 0} {
				if encoder.frames[i][0] != want {
					t.Fatalf("encoded frame %d: %v", i, encoder.frames[i])
				}
			}
		}
		for level := -1.0; level != 0; {
			select {
			case level = <-levels:
			case <-time.After(time.Second):
				t.Fatal("meter did not report scaled zero")
			}
		}
	}
}

type switchingCapturer struct {
	lifecycleCapturer
	once    sync.Once
	read    chan struct{}
	release <-chan struct{}
}

func (c *switchingCapturer) ReadFrame() ([]int16, error) {
	c.once.Do(func() { close(c.read) })
	if c.release != nil {
		<-c.release
	}
	return nil, errors.New("fake read failure")
}

func TestApplyAudioDevicesWaitsForCaptureIOOnly(t *testing.T) {
	e := NewEngine()
	g := setTestGeneration(e)
	g.voice = &VoiceClient{}
	release := make(chan struct{})
	old := &switchingCapturer{read: make(chan struct{}), release: release}
	next := &switchingCapturer{read: make(chan struct{})}
	r := &audioResources{capture: old, playback: &lifecyclePlayer{}, encoder: &lifecycleEncoder{}}
	g.audio = r
	g.run(func(_ context.Context) { e.captureLoop(g) })
	waitForSignal(t, old.read, "capture worker not reading")
	opened := make(chan struct{})
	e.newCaptureFn = func(string) (audio.Capturer, error) { close(opened); return next, nil }
	// An output write can remain blocked while an input-only switch completes.
	r.playbackMu.RLock()
	result := make(chan error, 1)
	go func() { result <- e.ApplyAudioDevices("next", "") }()
	waitForSignal(t, opened, "replacement not opened")
	if old.closed.Load() {
		t.Fatal("closed input during ReadFrame")
	}
	close(release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("input switch waited for unchanged output")
	}
	r.playbackMu.RUnlock()
	waitForSignal(t, next.read, "existing worker did not read replacement after old read failure")
	g.cancel()
	g.wg.Wait()
	r.close()
}

func TestApplyAudioDevicesBothChangesAreAtomic(t *testing.T) {
	e := NewEngine()
	g := setTestGeneration(e)
	oldCapture, oldPlayback := &lifecycleCapturer{}, &lifecyclePlayer{}
	g.audio = &audioResources{capture: oldCapture, playback: oldPlayback, encoder: &lifecycleEncoder{}}
	next := &lifecycleCapturer{}
	want := errors.New("output start failed")
	e.newCaptureFn = func(string) (audio.Capturer, error) { return next, nil }
	e.newPlaybackFn = func(string) (audio.Player, error) { return &configuredPlayer{startErr: want}, nil }
	if err := e.ApplyAudioDevices("next", "bad"); !errors.Is(err, want) {
		t.Fatalf("Apply error: %v", err)
	}
	if !next.closed.Load() || oldCapture.closed.Load() || oldPlayback.stopped.Load() || g.audio.capture != oldCapture {
		t.Fatal("partial switch leaked or retired working audio")
	}
	g.audio.close()
}

func TestApplyAudioDevicesOutputOnly(t *testing.T) {
	e := NewEngine()
	g := setTestGeneration(e)
	capture, old, next := &lifecycleCapturer{}, &lifecyclePlayer{}, &lifecyclePlayer{}
	r := &audioResources{capture: capture, playback: old, encoder: &lifecycleEncoder{}}
	g.audio = r
	e.newCaptureFn = func(string) (audio.Capturer, error) { t.Fatal("unchanged input reopened"); return nil, nil }
	e.newPlaybackFn = func(string) (audio.Player, error) { return next, nil }
	r.captureMu.RLock()
	done := make(chan error, 1)
	go func() { done <- e.ApplyAudioDevices("", "next") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("output switch waited for unchanged input")
	}
	r.captureMu.RUnlock()
	if capture.closed.Load() || !old.stopped.Load() || r.playback != next {
		t.Fatal("wrong output ownership")
	}
	g.audio.close()
}

func TestApplyAudioDevicesDuringInitialization(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.generation, e.state = g, StateConnected
	initialized, release := make(chan struct{}), make(chan struct{})
	old, next, playback := &lifecycleCapturer{}, &lifecycleCapturer{}, &lifecyclePlayer{}
	e.initAudioFn = func() (*audioResources, error) {
		close(initialized)
		<-release
		return &audioResources{capture: old, playback: playback, encoder: &lifecycleEncoder{}}, nil
	}
	e.newCaptureFn = func(string) (audio.Capturer, error) { return next, nil }
	e.startAudio(g)
	waitForSignal(t, initialized, "initialization not reached")
	done := make(chan error, 1)
	go func() { done <- e.ApplyAudioDevices("next", "") }()
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Apply/init race hung")
	}
	g.wg.Wait()
	if g.audio.capture != next || !old.closed.Load() || playback.stopped.Load() {
		t.Fatal("initialization/Apply lost ownership")
	}
	e.Disconnect()
}

func TestApplyAudioDevicesLive(t *testing.T) {
	e := NewEngine()
	g := setTestGeneration(e)
	oldCapture, oldPlayback := &lifecycleCapturer{}, &lifecyclePlayer{}
	g.audio = &audioResources{capture: oldCapture, playback: oldPlayback, encoder: &lifecycleEncoder{}}
	e.channelID, e.muted, e.deafened = 7, true, true
	next := &lifecycleCapturer{}
	e.newCaptureFn = func(name string) (audio.Capturer, error) {
		if name != "new mic" {
			t.Fatalf("unexpected input %q", name)
		}
		return next, nil
	}
	e.newPlaybackFn = func(string) (audio.Player, error) { t.Fatal("unchanged output reopened"); return nil, nil }
	if err := e.ApplyAudioDevices("new mic", ""); err != nil {
		t.Fatal(err)
	}
	if g.audio.capture != next || !oldCapture.closed.Load() || oldPlayback.stopped.Load() {
		t.Fatal("wrong stream replaced")
	}
	if e.generation != g || e.state != StateConnected || e.channelID != 7 || !e.muted || !e.deafened {
		t.Fatal("connection state changed")
	}
	if err := e.ApplyAudioDevices("new mic", ""); err != nil {
		t.Fatal(err)
	}
	g.audio.close()
}

func TestApplyAudioDevicesFailureRetainsStreams(t *testing.T) {
	e := NewEngine()
	g := setTestGeneration(e)
	capture, playback := &lifecycleCapturer{}, &lifecyclePlayer{}
	g.audio = &audioResources{capture: capture, playback: playback, encoder: &lifecycleEncoder{}}
	want := errors.New("PortAudio unsupported sample rate")
	e.newPlaybackFn = func(string) (audio.Player, error) { return &configuredPlayer{startErr: want}, nil }
	err := e.ApplyAudioDevices("", "bad output")
	if !errors.Is(err, want) {
		t.Fatalf("underlying error lost: %v", err)
	}
	if g.audio.capture != capture || g.audio.playback != playback || capture.closed.Load() || playback.stopped.Load() || e.audioOutput != "" {
		t.Fatal("failed switch changed working audio")
	}
	g.audio.close()
}

func TestApplyAudioDevicesDisconnectDuringOpen(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.generation, e.state = g, StateConnected
	oldCapture, oldPlayback := &lifecycleCapturer{}, &lifecyclePlayer{}
	g.audio = &audioResources{capture: oldCapture, playback: oldPlayback, encoder: &lifecycleEncoder{}}
	opened, release := make(chan struct{}), make(chan struct{})
	next := &lifecycleCapturer{}
	e.newCaptureFn = func(string) (audio.Capturer, error) { close(opened); <-release; return next, nil }
	applied := make(chan error, 1)
	go func() { applied <- e.ApplyAudioDevices("next", "") }()
	waitForSignal(t, opened, "device open not reached")
	disconnected := make(chan struct{})
	go func() { e.Disconnect(); close(disconnected) }()
	waitForSignal(t, g.ctx.Done(), "disconnect not canceled")
	select {
	case <-disconnected:
		t.Fatal("disconnect escaped admitted Apply")
	default:
	}
	close(release)
	select {
	case err := <-applied:
		if err == nil {
			t.Fatal("canceled switch succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Apply hung")
	}
	waitForSignal(t, disconnected, "disconnect hung")
	if !next.closed.Load() || !oldCapture.closed.Load() || !oldPlayback.stopped.Load() || e.state != StateDisconnected {
		t.Fatal("resource leak or stale install")
	}
}

func TestApplyAudioDevicesRecoveryAndInitialization(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.generation, e.state = g, StateConnected
	capture, playback := &lifecycleCapturer{}, &lifecyclePlayer{}
	e.newCaptureFn = func(string) (audio.Capturer, error) { return capture, nil }
	e.newPlaybackFn = func(string) (audio.Player, error) { return playback, nil }
	e.newEncoderFn = func() (audio.AudioEncoder, error) { return &lifecycleEncoder{}, nil }
	ready := make(chan error, 1)
	e.OnAudioFailure = func(err error) { ready <- err }
	if err := e.ApplyAudioDevices("", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery not reported")
	}
	e.initAudioFn = func() (*audioResources, error) {
		t.Error("late initialization overwrote recovered audio")
		return nil, errors.New("unexpected initialization")
	}
	e.startAudio(g)
	g.wg.Wait()
	if g.audio.capture != capture || g.audio.playback != playback {
		t.Fatal("recovered audio replaced")
	}
	e.Disconnect()
}

func TestApplyAudioDevicesSerialized(t *testing.T) {
	e := NewEngine()
	g := setTestGeneration(e)
	g.audio = &audioResources{capture: &lifecycleCapturer{}, playback: &lifecyclePlayer{}, encoder: &lifecycleEncoder{}}
	first := &lifecycleCapturer{}
	second := &lifecycleCapturer{}
	opened, release := make(chan struct{}), make(chan struct{})
	e.newCaptureFn = func(name string) (audio.Capturer, error) {
		if name == "first" {
			close(opened)
			<-release
			return first, nil
		}
		if name == "second" {
			return second, nil
		}
		t.Fatalf("unexpected name %q", name)
		return nil, nil
	}
	done := make(chan error, 2)
	go func() { done <- e.ApplyAudioDevices("first", "") }()
	waitForSignal(t, opened, "first Apply not reached")
	go func() { done <- e.ApplyAudioDevices("second", "") }()
	close(release)
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent Apply hung")
		}
	}
	if g.audio.capture != second || !first.closed.Load() || second.closed.Load() {
		t.Fatal("serialized replacement lost ownership")
	}
	g.audio.close()
}

func TestAudioFallbackPreservesCause(t *testing.T) {
	e := NewEngine()
	want := errors.New("PortAudio unsupported sample rate")
	e.SetAudioDevices("missing", "missing")
	e.newCaptureFn = func(name string) (audio.Capturer, error) {
		if name != "" {
			return nil, want
		}
		return &lifecycleCapturer{}, nil
	}
	e.newPlaybackFn = func(name string) (audio.Player, error) {
		if name != "" {
			return nil, want
		}
		return &lifecyclePlayer{}, nil
	}
	e.newEncoderFn = func() (audio.AudioEncoder, error) { return &lifecycleEncoder{}, nil }
	r, err := e.initAudioDefault()
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	if len(r.warnings) != 2 {
		t.Fatalf("warnings = %v", r.warnings)
	}
	for _, warning := range r.warnings {
		if !errors.Is(warning, want) {
			t.Fatalf("fallback lost cause: %v", warning)
		}
	}
}
