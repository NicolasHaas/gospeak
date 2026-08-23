package client

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"sync"
	"testing"
	"time"

	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type deadlineRecordingConn struct {
	recordingConn
	deadlineMu sync.Mutex
	deadlines  []time.Time
}

func waitForValue[T any](t *testing.T, values <-chan T, message string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal(message)
		var zero T
		return zero
	}
}

func (c *deadlineRecordingConn) SetWriteDeadline(deadline time.Time) error {
	c.deadlineMu.Lock()
	c.deadlines = append(c.deadlines, deadline)
	c.deadlineMu.Unlock()
	return nil
}

func TestScreenClientSendBoundsWrite(t *testing.T) {
	conn := &deadlineRecordingConn{}
	client := &ScreenClient{conn: conn}
	started := time.Now()
	if err := client.Send(&protocol.ScreenPacket{SessionID: 1, SeqNum: 1, Payload: []byte("frame")}); err != nil {
		t.Fatal(err)
	}
	conn.deadlineMu.Lock()
	deadlines := append([]time.Time(nil), conn.deadlines...)
	conn.deadlineMu.Unlock()
	if len(deadlines) != 2 {
		t.Fatalf("write deadline calls = %d, want 2", len(deadlines))
	}
	if !deadlines[0].After(started) || deadlines[0].After(started.Add(connectTimeout+time.Second)) {
		t.Fatalf("bounded screen write deadline = %v, started at %v", deadlines[0], started)
	}
	if !deadlines[1].IsZero() {
		t.Fatalf("cleared screen write deadline = %v, want zero", deadlines[1])
	}
}

func TestSendScreenShareFrameRejectsExhaustedSequence(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	conn := &recordingConn{}
	g.screen = &ScreenClient{conn: conn}
	e.generation = g
	e.state = StateConnected
	e.sessionID = 10

	cipher, err := gospeakCrypto.NewVoiceCipher(make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	e.screenCipher = cipher
	e.screenSeqNum = math.MaxUint32
	captureCalled := false
	e.captureScreenFn = func(int) (image.Image, error) {
		captureCalled = true
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
	}

	err = e.sendScreenShareFrame(g.ctx, g, 0)
	if !errors.Is(err, ErrScreenSequenceExhausted) {
		t.Fatalf("sendScreenShareFrame() error = %v, want %v", err, ErrScreenSequenceExhausted)
	}
	if captureCalled {
		t.Fatal("screen capture ran after sequence exhaustion")
	}
	if conn.b.Len() != 0 {
		t.Fatalf("screen bytes written after sequence exhaustion = %d, want 0", conn.b.Len())
	}
	if e.screenSeqNum != math.MaxUint32 {
		t.Fatalf("screen sequence = %d, want %d", e.screenSeqNum, uint32(math.MaxUint32))
	}
}

func TestSendScreenShareFrameDiscardsStaleCaptureAfterKeyRotation(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	conn := &recordingConn{}
	g.screen = &ScreenClient{conn: conn}
	e.generation = g
	e.state = StateConnected
	e.sessionID = 10
	e.channelID = 1
	oldEvent := &pb.ScreenShareEvent{Active: true, SessionID: 10, ChannelID: 1, EncryptionKey: bytes.Repeat([]byte{1}, 16)}
	e.handleScreenShareEvent(g, oldEvent)
	e.captureScreenFn = func(int) (image.Image, error) {
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
	}
	e.encodeScreenFn = func(image.Image, int, int) ([]byte, int32, int32, error) {
		return []byte("frame"), 1, 1, nil
	}
	publishReached := make(chan struct{})
	releasePublish := make(chan struct{})
	e.beforeScreenPublish = func() {
		close(publishReached)
		<-releasePublish
	}

	result := make(chan error, 1)
	go func() { result <- e.sendScreenShareFrame(context.Background(), g, 0) }()
	waitForSignal(t, publishReached, "screen sender did not reach key-rotation publication boundary")
	newEvent := &pb.ScreenShareEvent{Active: true, SessionID: 10, ChannelID: 1, EncryptionKey: bytes.Repeat([]byte{2}, 16)}
	e.handleScreenShareEvent(g, newEvent)
	close(releasePublish)

	if err := waitForValue(t, result, "screen sender did not return after publication state changed"); !errors.Is(err, errScreenShareChanged) {
		t.Fatalf("sendScreenShareFrame() error = %v, want %v", err, errScreenShareChanged)
	}
	if conn.b.Len() != 0 {
		t.Fatalf("stale screen bytes written after key rotation = %d, want 0", conn.b.Len())
	}
}

func TestSendScreenShareFrameDiscardsCaptureAfterStop(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	conn := &recordingConn{}
	g.screen = &ScreenClient{conn: conn}
	e.sessionID = 10
	cipher, err := gospeakCrypto.NewVoiceCipher(bytes.Repeat([]byte{1}, 16))
	if err != nil {
		t.Fatal(err)
	}
	e.screenCipher = cipher
	e.captureScreenFn = func(int) (image.Image, error) {
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
	}
	e.encodeScreenFn = func(image.Image, int, int) ([]byte, int32, int32, error) {
		return []byte("frame"), 1, 1, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.screenShareCancel = cancel
	e.screenShareRunning = true
	publishReached := make(chan struct{})
	releasePublish := make(chan struct{})
	e.beforeScreenPublish = func() {
		close(publishReached)
		<-releasePublish
	}
	result := make(chan error, 1)
	go func() { result <- e.sendScreenShareFrame(ctx, g, 0) }()
	waitForSignal(t, publishReached, "screen sender did not reach stop publication boundary")
	e.stopScreenShareLoop()
	close(releasePublish)

	if err := waitForValue(t, result, "screen sender did not return after publication state changed"); !errors.Is(err, errScreenShareChanged) {
		t.Fatalf("sendScreenShareFrame() error = %v, want %v", err, errScreenShareChanged)
	}
	if conn.b.Len() != 0 {
		t.Fatalf("screen bytes written after stop = %d, want 0", conn.b.Len())
	}
}

func TestRetiredScreenLoopCannotCancelReplacement(t *testing.T) {
	e := NewEngine()
	replacementCtx, replacementCancel := context.WithCancel(context.Background())
	defer replacementCancel()
	e.screenShareLoopID = 2
	e.screenShareCancel = replacementCancel
	e.screenShareRunning = true

	e.finishScreenShareLoop(1)

	if !e.screenShareRunning {
		t.Fatal("retired loop cleared replacement running state")
	}
	select {
	case <-replacementCtx.Done():
		t.Fatal("retired loop canceled replacement context")
	default:
	}
}

func TestScreenSequenceResetsOnlyAtConnectionGenerationBoundary(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	g.screen = &ScreenClient{conn: &recordingConn{}}
	e.generation = g
	e.state = StateConnected
	e.screenShareEnabled = true
	e.screenSeqNum = 42

	e.handleScreenDisconnectGeneration(g)
	if e.screenSeqNum != 42 {
		t.Fatalf("screen socket disconnect reset sequence to %d, want 42", e.screenSeqNum)
	}
	if e.generation != g {
		t.Fatal("screen socket disconnect replaced the control generation")
	}

	e.handleDisconnectGeneration(g, "test generation teardown")
	if e.screenSeqNum != 0 {
		t.Fatalf("full generation teardown preserved sequence %d, want 0", e.screenSeqNum)
	}
	if e.generation != nil || e.state != StateDisconnected {
		t.Fatalf("full generation teardown left generation=%p state=%v", e.generation, e.state)
	}
}

func TestRetiredScreenKeyResumesSequenceAfterRotation(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	g.screen = &ScreenClient{conn: &recordingConn{}}
	e.generation = g
	e.state = StateConnected
	e.sessionID = 10
	e.channelID = 1
	e.captureScreenFn = func(int) (image.Image, error) {
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
	}
	e.encodeScreenFn = func(image.Image, int, int) ([]byte, int32, int32, error) {
		return []byte("frame"), 1, 1, nil
	}
	key1 := bytes.Repeat([]byte{1}, 16)
	key2 := bytes.Repeat([]byte{2}, 16)
	e.handleScreenShareEvent(g, &pb.ScreenShareEvent{Active: true, SessionID: 10, ChannelID: 1, EncryptionKey: key1})
	if err := e.sendScreenShareFrame(context.Background(), g, 0); err != nil {
		t.Fatal(err)
	}
	e.handleScreenShareEvent(g, &pb.ScreenShareEvent{Active: true, SessionID: 10, ChannelID: 1, EncryptionKey: key2})
	e.handleScreenShareEvent(g, &pb.ScreenShareEvent{Active: true, SessionID: 10, ChannelID: 1, EncryptionKey: key1})
	if e.screenSeqNum != 1 {
		t.Fatalf("restored screen sequence = %d, want 1", e.screenSeqNum)
	}
	if err := e.sendScreenShareFrame(context.Background(), g, 0); err != nil {
		t.Fatal(err)
	}
	if e.screenSeqNum != 2 {
		t.Fatalf("screen sequence after retired-key replay = %d, want 2", e.screenSeqNum)
	}
}

func TestRetiredScreenKeyResumesSequenceAfterInactiveEvent(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	g.screen = &ScreenClient{conn: &recordingConn{}}
	e.generation = g
	e.state = StateConnected
	e.sessionID = 10
	e.channelID = 1
	e.captureScreenFn = func(int) (image.Image, error) {
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
	}
	e.encodeScreenFn = func(image.Image, int, int) ([]byte, int32, int32, error) {
		return []byte("frame"), 1, 1, nil
	}
	key := bytes.Repeat([]byte{1}, 16)
	e.handleScreenShareEvent(g, &pb.ScreenShareEvent{Active: true, SessionID: 10, ChannelID: 1, EncryptionKey: key})
	if err := e.sendScreenShareFrame(context.Background(), g, 0); err != nil {
		t.Fatal(err)
	}
	e.handleScreenShareEvent(g, &pb.ScreenShareEvent{Active: false, SessionID: 10, ChannelID: 1})
	e.handleScreenShareEvent(g, &pb.ScreenShareEvent{Active: true, SessionID: 10, ChannelID: 1, EncryptionKey: key})
	if e.screenSeqNum != 1 {
		t.Fatalf("restored screen sequence = %d, want 1", e.screenSeqNum)
	}
	if err := e.sendScreenShareFrame(context.Background(), g, 0); err != nil {
		t.Fatal(err)
	}
	if e.screenSeqNum != 2 {
		t.Fatalf("screen sequence after inactive-key replay = %d, want 2", e.screenSeqNum)
	}
}

func TestRepeatedScreenShareKeyDoesNotResetSequence(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.generation = g
	e.state = StateConnected
	e.sessionID = 10
	e.channelID = 1
	event := &pb.ScreenShareEvent{
		Active:        true,
		SessionID:     10,
		ChannelID:     1,
		EncryptionKey: bytes.Repeat([]byte{1}, 16),
	}

	e.handleScreenShareEvent(g, event)
	e.screenSeqNum = 5
	e.handleScreenShareEvent(g, event)
	if e.screenSeqNum != 5 {
		t.Fatalf("screen sequence after repeated key = %d, want 5", e.screenSeqNum)
	}

	rotated := *event
	rotated.EncryptionKey = bytes.Repeat([]byte{2}, 16)
	e.handleScreenShareEvent(g, &rotated)
	if e.screenSeqNum != 5 {
		t.Fatalf("screen sequence after key rotation = %d, want 5", e.screenSeqNum)
	}
}

func TestDecryptScreenPacketRejectsReplayAndPreservesSameKeyState(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	e.generation = g
	e.state = StateConnected
	e.sessionID = 20
	e.channelID = 1
	key1 := bytes.Repeat([]byte{1}, 16)
	key2 := bytes.Repeat([]byte{2}, 16)
	event := func(key []byte) *pb.ScreenShareEvent {
		return &pb.ScreenShareEvent{Active: true, SessionID: 10, ChannelID: 1, EncryptionKey: key}
	}
	packet := func(key []byte, sequence uint32) *protocol.ScreenPacket {
		cipher, err := gospeakCrypto.NewVoiceCipher(key)
		if err != nil {
			t.Fatalf("NewVoiceCipher: %v", err)
		}
		pkt := &protocol.ScreenPacket{SessionID: 10, SeqNum: sequence}
		pkt.Payload = cipher.Encrypt(pkt.SessionID, pkt.SeqNum, pkt.MarshalHeader(), []byte("frame"))
		return pkt
	}

	e.handleScreenShareEvent(g, event(key1))
	if _, ok := e.decryptScreenPacket(packet(key1, 1)); !ok {
		t.Fatal("first screen packet was rejected")
	}
	if _, ok := e.decryptScreenPacket(packet(key1, 1)); ok {
		t.Fatal("duplicate screen packet was accepted")
	}

	forged := packet(key1, 1000)
	forged.Payload[0] ^= 0xff
	if _, ok := e.decryptScreenPacket(forged); ok {
		t.Fatal("forged screen packet was accepted")
	}
	if _, ok := e.decryptScreenPacket(packet(key1, 2)); !ok {
		t.Fatal("forged high sequence poisoned receive state")
	}

	e.handleScreenShareEvent(g, event(key1))
	if _, ok := e.decryptScreenPacket(packet(key1, 2)); ok {
		t.Fatal("same-key reannouncement reset receive sequence")
	}
	if _, ok := e.decryptScreenPacket(packet(key1, 3)); !ok {
		t.Fatal("fresh packet after same-key reannouncement was rejected")
	}

	e.handleScreenShareEvent(g, event(key2))
	if _, ok := e.decryptScreenPacket(packet(key2, 1)); !ok {
		t.Fatal("new key did not start a fresh receive sequence")
	}
}

func TestScreenShareLoopStopsAndReportsSequenceExhaustion(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	screenConn := &recordingConn{}
	controlConn := &recordingConn{}
	g.screen = &ScreenClient{conn: screenConn}
	g.control = &ControlClient{conn: controlConn}
	e.generation = g
	e.state = StateConnected
	e.sessionID = 10

	cipher, err := gospeakCrypto.NewVoiceCipher(make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	e.screenCipher = cipher
	e.screenSeqNum = math.MaxUint32
	errCh := make(chan error, 1)
	e.OnError = func(err error) { errCh <- err }

	e.startScreenShareLoop(g)
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrScreenSequenceExhausted) {
			t.Fatalf("OnError() = %v, want %v", err, ErrScreenSequenceExhausted)
		}
	case <-time.After(time.Second):
		t.Fatal("screen loop did not report sequence exhaustion")
	}
	if screenConn.b.Len() != 0 {
		t.Fatalf("screen bytes written after sequence exhaustion = %d, want 0", screenConn.b.Len())
	}
	msg, err := protocol.ReadControlMessage(bytes.NewReader(controlConn.b.Bytes()))
	if err != nil {
		t.Fatalf("read screen stop request: %v", err)
	}
	if msg.ScreenShareStopReq == nil {
		t.Fatalf("control message = %#v, want screen share stop request", msg)
	}

	g.cancel()
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	waitForSignal(t, done, "screen generation workers did not stop")
}
