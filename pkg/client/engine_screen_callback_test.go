package client

import (
	"image"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

// Observe the waiter, rather than relying on a sleep to put an event behind Send.
func waitForScreenGate(t *testing.T, method string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stacks := make([]byte, 1<<20)
		n := runtime.Stack(stacks, true)
		for stack := range strings.SplitSeq(string(stacks[:n]), "\n\n") {
			if strings.Contains(stack, "(*Engine)."+method+"(") && strings.Contains(stack, "sync.Mutex.Lock") {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatalf("%s did not wait on the screen send gate", method)
}

func TestScreenSendDoesNotBlockMicrophoneCallbacks(t *testing.T) {
	for _, operation := range []string{"active", "inactive", "removed", "joined", "error", "stop", "clear"} {
		t.Run(operation, func(t *testing.T) {
			e := NewEngine()
			g := newConnectionGeneration()
			defer g.cancel()
			e.generation, e.state, e.sessionID, e.channelID = g, StateConnected, 10, 1
			e.mediaCipher = "aes128"
			g.control = &ControlClient{conn: &recordingConn{}}
			g.voice = &VoiceClient{}
			share := &pb.ScreenShareEvent{Active: true, SessionID: 10, ChannelID: 1, MediaCipher: "aes128", EncryptionKey: make([]byte, 16)}
			e.handleEventGeneration(g, &pb.ControlMessage{ScreenShareEvent: share})
			e.captureScreenFn = func(int) (image.Image, error) { return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil }
			e.encodeScreenFn = func(image.Image, int, int) ([]byte, int32, int32, error) { return []byte("frame"), 1, 1, nil }
			local, peer := net.Pipe()
			conn := &voiceBlockedWriteConn{Conn: local, entered: make(chan struct{})}
			g.screen = &ScreenClient{conn: conn}
			sent, handled, mic := make(chan struct{}), make(chan struct{}), make(chan struct{})
			go func() { defer close(sent); _ = e.sendScreenShareFrame(g.ctx, g, 0) }()
			t.Cleanup(func() {
				_ = local.Close()
				_ = peer.Close()
				waitForSignal(t, sent, "screen sender did not exit")
				waitForSignal(t, handled, "screen operation did not exit")
				waitForSignal(t, mic, "microphone producer did not exit")
			})
			waitForSignal(t, conn.entered, "screen write did not block")
			method := "handleEventGeneration"
			switch operation {
			case "stop":
				method = "StopScreenShare"
			case "clear":
				method = "clearScreenShareStateGeneration"
			}
			go func() {
				defer close(handled)
				switch operation {
				case "active":
					e.handleEventGeneration(g, &pb.ControlMessage{ScreenShareEvent: share})
				case "inactive":
					e.handleEventGeneration(g, &pb.ControlMessage{ScreenShareEvent: &pb.ScreenShareEvent{SessionID: 10, ChannelID: 1}})
				case "removed":
					e.handleEventGeneration(g, &pb.ControlMessage{ServerStateEvent: &pb.ServerStateEvent{}})
				case "joined":
					g.mu.Lock()
					g.pendingJoins[2] = 1
					g.mu.Unlock()
					e.handleEventGeneration(g, &pb.ControlMessage{ChannelJoinResponse: &pb.ChannelJoinResponse{ChannelID: 2, Success: true}})
				case "error":
					e.handleEventGeneration(g, &pb.ControlMessage{ErrorResponse: &pb.ErrorResponse{Code: 40, Message: "screen unavailable"}})
				case "stop":
					_ = e.StopScreenShare()
				case "clear":
					e.clearScreenShareStateGeneration(g)
				}
			}()
			waitForScreenGate(t, method)
			levels, activity := make(chan float64, 3), make(chan bool, 3)
			e.OnRMSLevel = func(level float64) { levels <- level }
			e.OnVoiceActivity = func(active bool) { activity <- active }
			encoder := &volumeEncoder{}
			g.audio = &audioResources{
				capture: &volumeCapturer{sequenceCapturer: sequenceCapturer{frame: []int16{1000}}, engine: e, generation: g, volumes: []float64{100, 100, 100}},
				encoder: encoder,
			}
			go func() {
				defer close(mic)
				e.captureLoop(g)
			}()
			// Only producer progress matters to the microphone capture loop.
			waitForSignal(t, mic, "screen network wait blocked microphone callback enqueue")
			if len(encoder.frames) != 3 {
				t.Fatal("microphone did not reach encoding while the screen send was blocked")
			}
		})
	}
}

func TestWaitingScreenEventRequalifiesGeneration(t *testing.T) {
	for _, transition := range []string{"cancel", "replace", "ordered"} {
		t.Run(transition, func(t *testing.T) {
			e := NewEngine()
			g := newConnectionGeneration()
			defer g.cancel()
			e.generation, e.state, e.channelID = g, StateConnected, 1
			e.mediaCipher = "aes128"
			old := &pb.ScreenShareEvent{Active: true, SessionID: 20, ChannelID: 1}
			e.activeScreenShare = old
			got := make(chan *pb.ScreenShareEvent, 2)
			e.OnScreenShareEvent = func(event *pb.ScreenShareEvent) { got <- event }
			blocked, release := make(chan struct{}), make(chan struct{})
			releaseCallbacks := sync.OnceFunc(func() { close(release) })
			e.invokeCallback(func() { close(blocked); <-release })
			waitForSignal(t, blocked, "callback runner did not block")
			e.screenSendMu.Lock()
			releaseGate := sync.OnceFunc(e.screenSendMu.Unlock)
			handled := make(chan struct{})
			t.Cleanup(func() { releaseGate(); releaseCallbacks(); waitForSignal(t, handled, "late event did not exit") })
			late := &pb.ScreenShareEvent{Active: true, SessionID: 30, ChannelID: 1, MediaCipher: "aes128"}
			go func() { defer close(handled); e.handleEventGeneration(g, &pb.ControlMessage{ScreenShareEvent: late}) }()
			waitForScreenGate(t, "handleEventGeneration")
			// Invalidation must remain possible while the event waits for a send.
			invalidated := make(chan struct{})
			go func() {
				defer close(invalidated)
				switch transition {
				case "cancel":
					g.mu.Lock()
					g.cancel()
					g.mu.Unlock()
				case "replace":
					e.callbackMu.Lock()
					e.mu.Lock()
					e.generation = newConnectionGeneration()
					e.mu.Unlock()
					e.callbackMu.Unlock()
				case "ordered":
					e.invokeGenerationCallback(g, func() { got <- old })
				}
			}()
			select {
			case <-invalidated:
			case <-time.After(time.Second):
				releaseGate()
				<-invalidated
				t.Fatal("waiting screen event held callback invalidation gate")
			}
			releaseGate()
			waitForSignal(t, handled, "late event did not finish")
			e.screenMu.Lock()
			active := e.activeScreenShare
			e.screenMu.Unlock()
			if transition != "ordered" && active != old {
				t.Fatal("late screen event changed invalidated generation state")
			}
			drained := make(chan struct{})
			e.invokeCallback(func() { close(drained) })
			releaseCallbacks()
			waitForSignal(t, drained, "callbacks did not drain")
			if transition == "ordered" {
				if waitForValue(t, got, "accepted event lost") != old || waitForValue(t, got, "late event lost") != late {
					t.Fatal("late event overtook an already accepted callback")
				}
			} else if len(got) != 0 {
				t.Fatal("late screen callback survived invalidation")
			}
		})
	}
}
