package client

import (
	"errors"
	"math"
	"net"
	"sync"
	"testing"
	"time"
)

type sequenceCapturer struct {
	frame []int16
}

func (*sequenceCapturer) Start() error { return nil }
func (c *sequenceCapturer) ReadFrame() ([]int16, error) {
	return append([]int16(nil), c.frame...), nil
}
func (*sequenceCapturer) Stop() error  { return nil }
func (*sequenceCapturer) Close() error { return nil }

type sequenceEncoder struct{}

func (*sequenceEncoder) Encode([]int16) ([]byte, error) { return []byte("opus"), nil }

type barrierEncoder struct {
	mu      sync.Mutex
	count   int
	ready   chan struct{}
	release chan struct{}
}

func (e *barrierEncoder) Encode([]int16) ([]byte, error) {
	e.mu.Lock()
	e.count++
	first := e.count == 1
	if first {
		close(e.ready)
	}
	e.mu.Unlock()
	if first {
		<-e.release
	}
	return []byte("opus"), nil
}

func waitForSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func exhaustedVoiceClient(t *testing.T) (*VoiceClient, *net.UDPConn) {
	t.Helper()
	receiver, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := net.DialUDP("udp", nil, receiver.LocalAddr().(*net.UDPAddr))
	if err != nil {
		_ = receiver.Close()
		t.Fatal(err)
	}
	return &VoiceClient{conn: sender, seqNum: math.MaxUint32}, receiver
}

func assertVoiceExhaustion(t *testing.T, g *connectionGeneration, errCh <-chan error, receiver *net.UDPConn) {
	t.Helper()
	select {
	case <-g.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("voice sequence exhaustion did not cancel the connection generation")
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrVoiceSequenceExhausted) {
			t.Fatalf("OnError() = %v, want %v", err, ErrVoiceSequenceExhausted)
		}
	case <-time.After(time.Second):
		t.Fatal("voice sequence exhaustion was not reported")
	}
	select {
	case <-g.done:
	case <-time.After(time.Second):
		t.Fatal("voice sequence exhaustion did not finish disconnect")
	}
	if err := receiver.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n, _, err := receiver.ReadFromUDP(make([]byte, 64)); err == nil {
		t.Fatalf("received %d voice bytes after sequence exhaustion, want none", n)
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("read after sequence exhaustion: %v", err)
	}
}

func TestCaptureLoopDisconnectsOnVoiceSequenceExhaustion(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	voice, receiver := exhaustedVoiceClient(t)
	defer receiver.Close()
	g.voice = voice
	g.audio = &audioResources{
		capture: &sequenceCapturer{frame: make([]int16, voiceFrameSamples)},
		encoder: &sequenceEncoder{},
	}
	for i := range g.audio.capture.(*sequenceCapturer).frame {
		g.audio.capture.(*sequenceCapturer).frame[i] = 1000
	}
	e.generation = g
	e.state = StateConnected
	e.channelID = 1
	errCh := make(chan error, 1)
	e.OnError = func(err error) { errCh <- err }

	e.captureLoop(g)

	assertVoiceExhaustion(t, g, errCh, receiver)
}

func TestKeepaliveDisconnectsOnVoiceSequenceExhaustion(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	voice, receiver := exhaustedVoiceClient(t)
	defer receiver.Close()
	g.voice = voice
	g.audio = &audioResources{encoder: &sequenceEncoder{}}
	e.generation = g
	e.state = StateConnected
	e.channelID = 1
	e.silenceBuf = make([]int16, voiceFrameSamples)
	errCh := make(chan error, 1)
	e.OnError = func(err error) { errCh <- err }
	timestamp := uint32(0)

	e.trySendKeepalive(g, &timestamp, false)

	assertVoiceExhaustion(t, g, errCh, receiver)
}

func TestConcurrentVoiceSequenceExhaustionReportsOnce(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	voice, receiver := exhaustedVoiceClient(t)
	defer receiver.Close()
	encoder := &barrierEncoder{ready: make(chan struct{}), release: make(chan struct{})}
	capture := &sequenceCapturer{frame: make([]int16, voiceFrameSamples)}
	for i := range capture.frame {
		capture.frame[i] = 1000
	}
	g.voice = voice
	g.audio = &audioResources{capture: capture, encoder: encoder}
	e.generation = g
	e.state = StateConnected
	e.channelID = 1
	e.silenceBuf = make([]int16, voiceFrameSamples)
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	e.OnError = func(error) {
		close(callbackStarted)
		<-releaseCallback
	}

	var callers sync.WaitGroup
	callers.Add(2)
	callersDone := make(chan struct{})
	go func() {
		defer callers.Done()
		e.captureLoop(g)
	}()
	waitForSignal(t, encoder.ready, "capture encoder did not reach concurrency barrier")
	keepaliveStarted := make(chan struct{})
	go func() {
		defer callers.Done()
		close(keepaliveStarted)
		timestamp := uint32(0)
		e.trySendKeepalive(g, &timestamp, false)
	}()
	waitForSignal(t, keepaliveStarted, "keepalive caller did not start")
	close(encoder.release)
	waitForSignal(t, callbackStarted, "voice exhaustion callback did not start")
	go func() {
		callers.Wait()
		close(callersDone)
	}()
	waitForSignal(t, callersDone, "concurrent voice callers did not return")

	e.callbackQueueMu.Lock()
	queuedCallbacks := len(e.callbackQueue)
	e.callbackQueueMu.Unlock()
	if queuedCallbacks != 0 {
		t.Fatalf("queued callbacks after concurrent exhaustion = %d, want 0", queuedCallbacks)
	}
	close(releaseCallback)
	select {
	case <-g.done:
	case <-time.After(time.Second):
		t.Fatal("concurrent voice sequence exhaustion did not finish disconnect")
	}
	if err := receiver.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n, _, err := receiver.ReadFromUDP(make([]byte, 64)); err == nil {
		t.Fatalf("received %d voice bytes after concurrent sequence exhaustion, want none", n)
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("read after concurrent sequence exhaustion: %v", err)
	}
}

func TestVoiceSequenceExhaustionUsesReliableCallbackCapacity(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.generation = g
	e.state = StateConnected
	e.OnError = func(error) {}

	e.callbackQueueMu.Lock()
	e.callbackQueue = make([]func(), maxCallbackQueue)
	e.callbackQueueRunning = true
	e.callbackQueueMu.Unlock()

	if !e.handleVoiceSendError(g, ErrVoiceSequenceExhausted) {
		t.Fatal("voice sequence exhaustion was not handled")
	}

	e.callbackQueueMu.Lock()
	queuedCallbacks := len(e.callbackQueue)
	e.callbackQueueMu.Unlock()
	if queuedCallbacks != maxCallbackQueue+1 {
		t.Fatalf("callbacks queued after voice exhaustion = %d, want %d", queuedCallbacks, maxCallbackQueue+1)
	}
	select {
	case <-g.done:
	case <-time.After(time.Second):
		t.Fatal("voice sequence exhaustion did not finish disconnect")
	}
}
