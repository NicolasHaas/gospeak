package client

import (
	"context"
	"errors"
	"image"
	"net"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type lifecycleCapturer struct {
	closed atomic.Bool
}

func (*lifecycleCapturer) Start() error                { return nil }
func (*lifecycleCapturer) ReadFrame() ([]int16, error) { return nil, errors.New("stopped") }
func (*lifecycleCapturer) Stop() error                 { return nil }
func (c *lifecycleCapturer) Close() error {
	c.closed.Store(true)
	return nil
}

type lifecyclePlayer struct {
	stopped atomic.Bool
}

func (*lifecyclePlayer) Start() error             { return nil }
func (*lifecyclePlayer) WriteFrame([]int16) error { return nil }
func (p *lifecyclePlayer) Stop() error {
	p.stopped.Store(true)
	return nil
}

type lifecycleEncoder struct{}

func (*lifecycleEncoder) Encode([]int16) ([]byte, error) { return nil, nil }

func setTestGeneration(e *Engine) *connectionGeneration {
	g := newConnectionGeneration()
	g.control = &ControlClient{}
	e.generation = g
	e.state = StateConnected
	return g
}

func TestStaleGenerationCannotDisconnectReplacement(t *testing.T) {
	e := NewEngine()
	old := newConnectionGeneration()
	replacement := newConnectionGeneration()

	e.mu.Lock()
	e.generation = replacement
	e.state = StateConnected
	e.channels = []pb.ChannelInfo{{ID: 1, Name: "replacement"}}
	e.mu.Unlock()

	e.handleDisconnectGeneration(old, "stale connection lost")
	e.handleEventGeneration(old, &pb.ControlMessage{
		ServerStateEvent: &pb.ServerStateEvent{
			Channels: []pb.ChannelInfo{{ID: 2, Name: "stale"}},
		},
	})

	if got := e.GetState(); got != StateConnected {
		t.Fatalf("state = %v, want connected", got)
	}
	e.mu.RLock()
	gotGeneration := e.generation
	e.mu.RUnlock()
	if gotGeneration != replacement {
		t.Fatal("stale disconnect replaced the active generation")
	}
	channels := e.GetChannels()
	if len(channels) != 1 || channels[0].ID != 1 {
		t.Fatalf("stale event changed replacement channels: %#v", channels)
	}
}

func TestDelayedAudioInitializationClosesResourcesAfterDisconnect(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.mu.Lock()
	e.generation = g
	e.state = StateConnected
	e.mu.Unlock()

	started := make(chan struct{})
	release := make(chan struct{})
	capture := &lifecycleCapturer{}
	playback := &lifecyclePlayer{}
	e.initAudioFn = func() (*audioResources, error) {
		close(started)
		<-release
		return &audioResources{capture: capture, playback: playback, encoder: &lifecycleEncoder{}}, nil
	}
	e.startAudio(g)
	<-started

	disconnected := make(chan struct{})
	go func() {
		e.Disconnect()
		close(disconnected)
	}()
	<-g.ctx.Done()

	select {
	case <-disconnected:
		t.Fatal("Disconnect returned before generation audio initialization exited")
	default:
	}
	close(release)

	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("disconnect did not wait for delayed audio initialization")
	}
	if !capture.closed.Load() {
		t.Fatal("late capture resource was not closed")
	}
	if !playback.stopped.Load() {
		t.Fatal("late playback resource was not stopped")
	}
}

func TestGenerationRejectsWorkersAfterCancellation(t *testing.T) {
	g := newConnectionGeneration()
	g.cancel()

	if g.run(func(context.Context) {
		t.Error("canceled generation started a worker")
	}) {
		t.Fatal("canceled generation accepted a worker")
	}
	g.wg.Wait()
}

func TestCallbackQueueSerializesCallbacksWithoutBlockingProducer(t *testing.T) {
	e := NewEngine()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	order := make(chan int, 2)

	e.invokeCallback(func() {
		close(firstStarted)
		<-releaseFirst
		order <- 1
	})
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first callback did not start")
	}

	e.invokeCallback(func() { order <- 2 })
	select {
	case got := <-order:
		t.Fatalf("callback %d ran before the first callback was released", got)
	default:
	}
	close(releaseFirst)

	for want := 1; want <= 2; want++ {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("callback order = %d, want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("callback %d did not run", want)
		}
	}
}

func TestCallbackQueueContinuesAfterPanic(t *testing.T) {
	e := NewEngine()
	continued := make(chan struct{})
	e.invokeCallback(func() { panic("test callback panic") })
	e.invokeCallback(func() { close(continued) })
	select {
	case <-continued:
	case <-time.After(time.Second):
		t.Fatal("callback queue stopped after panic")
	}
}

func TestReliableCallbackQueueHasHardLimit(t *testing.T) {
	e := NewEngine()
	started := make(chan struct{})
	release := make(chan struct{})
	e.invokeCallback(func() {
		close(started)
		<-release
	})
	<-started

	for range maxReliableCallbackQueue {
		if !e.enqueueCallback(func() {}, true) {
			t.Fatal("reliable callback dropped before hard limit")
		}
	}
	if e.enqueueCallback(func() {}, true) {
		t.Fatal("reliable callback queue exceeded hard limit")
	}
	e.callbackQueueMu.Lock()
	queued := len(e.callbackQueue)
	e.callbackQueueMu.Unlock()
	if queued != maxReliableCallbackQueue {
		t.Fatalf("reliable callback queue length = %d, want %d", queued, maxReliableCallbackQueue)
	}
	close(release)
}

func TestGenerationCallbackQueueIsBounded(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.mu.Lock()
	e.generation = g
	e.state = StateConnected
	e.mu.Unlock()

	started := make(chan struct{})
	release := make(chan struct{})
	e.invokeCallback(func() {
		close(started)
		<-release
	})
	<-started
	for range maxCallbackQueue * 2 {
		e.invokeGenerationCallback(g, func() {})
	}
	e.callbackQueueMu.Lock()
	queued := len(e.callbackQueue)
	e.callbackQueueMu.Unlock()
	if queued > maxCallbackQueue {
		t.Fatalf("generation callback queue length = %d, want <= %d", queued, maxCallbackQueue)
	}
	close(release)
}

func TestLatestGenerationCallbackIsCoalesced(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.mu.Lock()
	e.generation = g
	e.state = StateConnected
	e.mu.Unlock()

	started := make(chan struct{})
	release := make(chan struct{})
	e.invokeCallback(func() {
		close(started)
		<-release
	})
	<-started
	got := make(chan int, 1)
	for value := range 100 {
		value := value
		e.invokeLatestGenerationCallback(g, callbackRMS, func() { got <- value })
	}
	e.callbackQueueMu.Lock()
	queued := len(e.callbackQueue)
	e.callbackQueueMu.Unlock()
	if queued != 1 {
		t.Fatalf("coalesced callback queue length = %d, want 1", queued)
	}
	close(release)
	select {
	case value := <-got:
		if value != 99 {
			t.Fatalf("coalesced callback value = %d, want 99", value)
		}
	case <-time.After(time.Second):
		t.Fatal("coalesced callback did not run")
	}
}

func TestGenerationCallbacksLinearizeWithReplacement(t *testing.T) {
	e := NewEngine()
	generationA := newConnectionGeneration()
	generationB := newConnectionGeneration()
	e.mu.Lock()
	e.generation = generationA
	e.state = StateConnected
	e.mu.Unlock()

	blockerStarted := make(chan struct{})
	releaseBlocker := make(chan struct{})
	e.invokeCallback(func() {
		close(blockerStarted)
		<-releaseBlocker
	})
	<-blockerStarted

	beforeReplacement := make(chan struct{}, 1)
	afterReplacement := make(chan struct{}, 1)
	e.invokeGenerationCallback(generationA, func() { beforeReplacement <- struct{}{} })
	e.callbackMu.Lock()
	e.mu.Lock()
	e.generation = generationB
	e.mu.Unlock()
	e.callbackMu.Unlock()
	e.invokeGenerationCallback(generationA, func() { afterReplacement <- struct{}{} })
	drained := make(chan struct{})
	e.invokeCallback(func() { close(drained) })
	close(releaseBlocker)
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("callback queue did not drain")
	}

	select {
	case <-beforeReplacement:
	default:
		t.Fatal("callback queued before replacement was dropped")
	}
	select {
	case <-afterReplacement:
		t.Fatal("generation callback queued after replacement")
	default:
	}
}

func TestConnectingNotificationSurvivesImmediateCancellation(t *testing.T) {
	e := NewEngine()
	blockerStarted := make(chan struct{})
	releaseBlocker := make(chan struct{})
	e.invokeCallback(func() {
		close(blockerStarted)
		<-releaseBlocker
	})
	<-blockerStarted

	states := make(chan State, 1)
	e.OnStateChange = func(state State) { states <- state }
	g := newConnectionGeneration()
	if !e.publishConnectingGeneration(g) {
		t.Fatal("failed to publish connecting generation")
	}
	g.mu.Lock()
	g.cancel()
	g.mu.Unlock()
	close(releaseBlocker)

	select {
	case state := <-states:
		if state != StateConnecting {
			t.Fatalf("state notification = %v, want %v", state, StateConnecting)
		}
	case <-time.After(time.Second):
		t.Fatal("StateConnecting notification was dropped after cancellation")
	}
}

func TestCanceledGenerationCannotPublishConnected(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.mu.Lock()
	e.generation = g
	e.state = StateConnecting
	e.mu.Unlock()
	called := make(chan struct{}, 1)
	e.OnStateChange = func(State) { called <- struct{}{} }
	g.mu.Lock()
	g.cancel()
	g.mu.Unlock()

	if e.publishConnectedGeneration(g, &pb.AuthResponse{SessionID: 42, Username: "stale"}) {
		t.Fatal("canceled generation published StateConnected")
	}
	e.mu.RLock()
	state, sessionID := e.state, e.sessionID
	e.mu.RUnlock()
	if state != StateConnecting || sessionID != 0 {
		t.Fatalf("canceled generation mutated state: state=%v session=%d", state, sessionID)
	}
	select {
	case <-called:
		t.Fatal("canceled generation enqueued StateConnected callback")
	default:
	}
}

func TestStateNotificationsAreNotDroppedAcrossReplacement(t *testing.T) {
	e := NewEngine()
	generationA := newConnectionGeneration()
	generationB := newConnectionGeneration()
	e.mu.Lock()
	e.generation = generationA
	e.state = StateConnecting
	e.mu.Unlock()

	blockerStarted := make(chan struct{})
	releaseBlocker := make(chan struct{})
	e.invokeCallback(func() {
		close(blockerStarted)
		<-releaseBlocker
	})
	<-blockerStarted

	states := make(chan State, 4)
	e.OnStateChange = func(state State) { states <- state }
	e.notifyGenerationState(generationA, StateConnecting)
	e.mu.Lock()
	e.state = StateConnected
	e.mu.Unlock()
	e.notifyGenerationState(generationA, StateConnected)

	e.callbackMu.Lock()
	generationA.mu.Lock()
	generationA.cancel()
	generationA.mu.Unlock()
	e.mu.Lock()
	e.generation = nil
	e.state = StateDisconnected
	e.mu.Unlock()
	e.invokeCallback(func() { e.OnStateChange(StateDisconnected) })
	e.mu.Lock()
	e.generation = generationB
	e.state = StateConnecting
	e.mu.Unlock()
	e.enqueueReliableGenerationCallbackLocked(generationB, func() { e.OnStateChange(StateConnecting) })
	e.callbackMu.Unlock()
	close(releaseBlocker)

	want := []State{StateConnecting, StateConnected, StateDisconnected, StateConnecting}
	for i, expected := range want {
		select {
		case got := <-states:
			if got != expected {
				t.Fatalf("state notification %d = %v, want %v", i, got, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("state notification %d was dropped", i)
		}
	}
}

func TestTrackedReceiverIsJoinedAfterCancellation(t *testing.T) {
	g := newConnectionGeneration()
	started := make(chan struct{})
	receiverDone := make(chan struct{})
	if !g.startTrackedReceiver(func() { close(started) }, func() { <-receiverDone }) {
		t.Fatal("failed to admit receiver")
	}
	<-started
	g.mu.Lock()
	g.cancel()
	g.mu.Unlock()

	joined := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(joined)
	}()
	select {
	case <-joined:
		t.Fatal("generation completed before receiver exited")
	case <-time.After(20 * time.Millisecond):
	}
	close(receiverDone)
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("generation did not join receiver")
	}
}

func TestConcurrentDisconnectWaitsForInProgressTeardown(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.mu.Lock()
	e.generation = g
	e.state = StateConnected
	e.mu.Unlock()

	workerStarted := make(chan struct{})
	releaseWorker := make(chan struct{})
	if !g.run(func(context.Context) {
		close(workerStarted)
		<-releaseWorker
	}) {
		t.Fatal("failed to start generation worker")
	}
	<-workerStarted
	firstReturned := make(chan struct{})
	go func() {
		e.Disconnect()
		close(firstReturned)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		e.mu.RLock()
		invalidated := e.generation == nil && e.disconnecting == g
		e.mu.RUnlock()
		if invalidated {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("generation was not invalidated during teardown")
		}
		time.Sleep(time.Millisecond)
	}

	secondReturned := make(chan struct{})
	go func() {
		e.Disconnect()
		close(secondReturned)
	}()
	select {
	case <-secondReturned:
		t.Fatal("concurrent Disconnect returned before teardown completed")
	case <-time.After(20 * time.Millisecond):
	}
	connectReturned := make(chan error, 1)
	go func() {
		connectReturned <- e.Connect("127.0.0.1:1", "127.0.0.1:1", "token", "user", "")
	}()
	select {
	case <-connectReturned:
		t.Fatal("Connect passed the disconnect barrier before teardown completed")
	case <-time.After(20 * time.Millisecond):
	}
	e.mu.RLock()
	replacementStarted := e.generation != nil
	e.mu.RUnlock()
	if replacementStarted {
		t.Fatal("replacement generation started before disconnect completion")
	}

	close(releaseWorker)
	for i, returned := range []<-chan struct{}{firstReturned, secondReturned} {
		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Fatalf("Disconnect caller %d did not return after teardown", i+1)
		}
	}
	select {
	case <-connectReturned:
	case <-time.After(time.Second):
		t.Fatal("Connect remained blocked after disconnect completion")
	}
}

func TestDisconnectWaitsForTeardownWithoutWaitingForCallbackQueue(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.mu.Lock()
	e.generation = g
	e.state = StateConnected
	e.mu.Unlock()

	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	e.invokeCallback(func() {
		close(callbackStarted)
		<-releaseCallback
	})
	<-callbackStarted

	disconnected := make(chan struct{})
	go func() {
		e.Disconnect()
		close(disconnected)
	}()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("Disconnect waited for an unrelated UI callback")
	}
	if e.GetState() != StateDisconnected {
		t.Fatalf("state = %v, want disconnected after Disconnect returned", e.GetState())
	}
	close(releaseCallback)
}

func TestGenerationDisconnectFromStateCallbackDoesNotSelfDeadlock(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.mu.Lock()
	e.generation = g
	e.state = StateConnected
	e.mu.Unlock()

	callbackReturned := make(chan struct{})
	e.OnStateChange = func(state State) {
		if state != StateConnected {
			return
		}
		e.Disconnect()
		close(callbackReturned)
	}
	e.notifyStateChange(StateConnected)

	select {
	case <-callbackReturned:
	case <-time.After(time.Second):
		t.Fatal("state callback deadlocked while disconnecting")
	}
	select {
	case <-g.done:
	case <-time.After(time.Second):
		t.Fatal("state-callback disconnect did not complete")
	}
}

func TestGenerationDisconnectThenConnectFromCallbackDoesNotDeadlock(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.mu.Lock()
	e.generation = g
	e.state = StateConnected
	e.mu.Unlock()

	callbackReturned := make(chan struct{})
	connectResult := make(chan error, 1)
	e.OnError = func(error) {
		e.Disconnect()
		connectResult <- e.Connect("unused", "unused", "", "", "")
		close(callbackReturned)
	}

	if !g.run(func(context.Context) {
		e.handleEventGeneration(g, &pb.ControlMessage{ErrorResponse: &pb.ErrorResponse{Message: "test"}})
	}) {
		t.Fatal("failed to start callback worker")
	}

	select {
	case <-callbackReturned:
	case <-time.After(time.Second):
		t.Fatal("callback deadlocked while disconnecting and reconnecting")
	}
	if err := <-connectResult; err == nil {
		t.Fatal("callback reentrant connect unexpectedly succeeded")
	}
	select {
	case <-g.done:
	case <-time.After(time.Second):
		t.Fatal("disconnect did not finish after callback returned")
	}
}

func TestGenerationDisconnectFromCallbackDoesNotSelfDeadlock(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.mu.Lock()
	e.generation = g
	e.state = StateConnected
	e.mu.Unlock()

	callbackReturned := make(chan struct{})
	e.OnError = func(error) {
		e.Disconnect()
		close(callbackReturned)
	}
	if !g.run(func(context.Context) {
		e.handleEventGeneration(g, &pb.ControlMessage{
			ErrorResponse: &pb.ErrorResponse{Code: 1, Message: "test"},
		})
	}) {
		t.Fatal("live generation rejected callback worker")
	}

	select {
	case <-callbackReturned:
	case <-time.After(time.Second):
		t.Fatal("generation callback deadlocked while disconnecting")
	}
	select {
	case <-g.done:
	case <-time.After(time.Second):
		t.Fatal("callback-triggered disconnect did not complete")
	}
}

func TestGenerationDisconnectFromWorkerDoesNotSelfDeadlock(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.mu.Lock()
	e.generation = g
	e.state = StateConnected
	e.mu.Unlock()

	requested := make(chan struct{})
	g.run(func(ctx context.Context) {
		e.requestDisconnect(g, "worker connection lost")
		close(requested)
	})

	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("generation worker deadlocked requesting disconnect")
	}

	deadline := time.After(time.Second)
	for e.GetState() != StateDisconnected {
		select {
		case <-deadline:
			t.Fatal("requested disconnect did not complete")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

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

func TestSelectAutoJoinChannelHonorsScope(t *testing.T) {
	channels := []pb.ChannelInfo{{ID: 1, Name: "first"}, {ID: 2, Name: "scoped"}}

	got, ok := selectAutoJoinChannel(channels, 2)
	if !ok || got.ID != 2 {
		t.Fatalf("selectAutoJoinChannel(scope=2) = (%#v, %t), want channel 2", got, ok)
	}
	got, ok = selectAutoJoinChannel(channels, 0)
	if !ok || got.ID != 1 {
		t.Fatalf("selectAutoJoinChannel(scope=0) = (%#v, %t), want first channel", got, ok)
	}
	if _, ok := selectAutoJoinChannel(channels, 3); ok {
		t.Fatal("selectAutoJoinChannel accepted a missing scoped channel")
	}
}

func TestClearScreenShareStateResetsCallbacks(t *testing.T) {
	e := NewEngine()
	e.screenMu.Lock()
	e.activeScreenShare = &pb.ScreenShareEvent{Active: true, SessionID: 10, ChannelID: 1}
	e.screenSharePending = true
	e.screenMu.Unlock()

	eventCalled := make(chan struct{}, 1)
	frameCleared := make(chan struct{}, 1)
	e.OnScreenShareEvent = func(event *pb.ScreenShareEvent) {
		if event == nil {
			eventCalled <- struct{}{}
		}
	}
	e.OnScreenFrame = func(_ image.Image) {
		frameCleared <- struct{}{}
	}

	e.clearScreenShareState()

	e.screenMu.Lock()
	active, pending := e.activeScreenShare, e.screenSharePending
	e.screenMu.Unlock()
	if active != nil {
		t.Fatalf("activeScreenShare = %v, want nil", active)
	}
	if pending {
		t.Fatalf("screenSharePending = true, want false")
	}
	select {
	case <-eventCalled:
	case <-time.After(time.Second):
		t.Fatal("OnScreenShareEvent(nil) was not called")
	}
	select {
	case <-frameCleared:
	case <-time.After(time.Second):
		t.Fatal("OnScreenFrame(nil) was not called")
	}
}

func TestStaleGenerationCannotMutateUserState(t *testing.T) {
	e := NewEngine()
	stale := newConnectionGeneration()
	current := newConnectionGeneration()
	e.mu.Lock()
	e.generation = current
	e.state = StateConnected
	e.muted = false
	e.deafened = true
	e.mu.Unlock()

	if _, updated := e.updateMutedGeneration(stale, true); updated {
		t.Fatal("stale generation updated mute state")
	}
	if _, _, updated := e.updateDeafenedGeneration(stale, false); updated {
		t.Fatal("stale generation updated deafen state")
	}
	e.mu.RLock()
	muted, deafened := e.muted, e.deafened
	e.mu.RUnlock()
	if muted || !deafened {
		t.Fatalf("replacement user state mutated: muted=%v deafened=%v", muted, deafened)
	}
}

func TestStartScreenShareDoesNotPublishAfterDisconnect(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	g.mu.Lock()
	g.control = &ControlClient{conn: clientConn, done: make(chan struct{})}
	g.mu.Unlock()
	e.mu.Lock()
	e.generation = g
	e.state = StateConnected
	e.channelID = 1
	e.sessionID = 10
	e.mu.Unlock()

	captureStarted := make(chan struct{})
	releaseCapture := make(chan struct{})
	e.captureScreenFn = func(int) (image.Image, error) {
		close(captureStarted)
		<-releaseCapture
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
	}
	startResult := make(chan error, 1)
	go func() { startResult <- e.StartScreenShare(0) }()
	<-captureStarted

	disconnected := make(chan struct{})
	go func() {
		e.Disconnect()
		close(disconnected)
	}()
	select {
	case err := <-startResult:
		if err == nil {
			t.Fatal("StartScreenShare succeeded after disconnect")
		}
	case <-time.After(time.Second):
		t.Fatal("StartScreenShare did not cancel with its generation")
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("Disconnect did not wait for StartScreenShare")
	}
	close(releaseCapture)
	select {
	case screenCaptureSlot <- struct{}{}:
		<-screenCaptureSlot
	case <-time.After(time.Second):
		t.Fatal("canceled screen capture helper did not exit")
	}
	e.screenMu.Lock()
	pending := e.screenSharePending
	e.screenMu.Unlock()
	if pending {
		t.Fatal("stale StartScreenShare published pending state")
	}
}

func TestStartScreenShareRejectsWhenAnotherShareIsActive(t *testing.T) {
	e := NewEngine()
	setTestGeneration(e)
	e.channelID = 1
	e.sessionID = 10
	e.screenMu.Lock()
	e.activeScreenShare = &pb.ScreenShareEvent{Active: true, SessionID: 20, ChannelID: 1}
	e.screenMu.Unlock()

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
	setTestGeneration(e)
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
	setTestGeneration(e)
	e.channelID = 1
	e.screenCaptureWait = 10 * time.Millisecond
	releaseCapture := make(chan struct{})
	e.captureScreenFn = func(displayIndex int) (image.Image, error) {
		<-releaseCapture
		return nil, errors.New("released")
	}

	err := e.StartScreenShare(0)
	close(releaseCapture)
	select {
	case screenCaptureSlot <- struct{}{}:
		<-screenCaptureSlot
	case <-time.After(time.Second):
		t.Fatal("timed-out screen capture helper did not exit")
	}
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
	g := newConnectionGeneration()
	e.generation = g
	e.state = StateConnected
	e.screenSharePending = true
	e.screenShareAttempt = 1

	errCh := make(chan error, 1)
	e.OnError = func(err error) {
		errCh <- err
	}

	go e.watchScreenShareStart(g, 1, 10*time.Millisecond)

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
