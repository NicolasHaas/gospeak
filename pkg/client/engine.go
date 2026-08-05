package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"log/slog"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/audio"
	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
	"github.com/NicolasHaas/gospeak/pkg/screenshare"
)

var (
	errScreenShareCaptureTimedOut = errors.New("screen capture timed out")
	errScreenShareStartTimedOut   = errors.New("screen share start timed out")
	screenCaptureSlot             = make(chan struct{}, 1)
)

const (
	defaultScreenShareCaptureTimeout = 15 * time.Second
	defaultScreenShareStartTimeout   = 10 * time.Second
	keepAliveInterval                = 5 * time.Second
	voiceFrameSamples                = 960
	speakerStateTTL                  = 30 * time.Second
	maxCallbackQueue                 = 256
	maxReliableCallbackQueue         = 1024
)

type callbackKind uint8

const (
	callbackRMS callbackKind = iota
	callbackVoiceActivity
	callbackScreenFrame
)

type latestCallbackKey struct {
	generation *connectionGeneration
	kind       callbackKind
}

// State represents the client's connection state.
type State int

const (
	StateDisconnected State = iota
	StateConnecting
	StateConnected
)

type audioResources struct {
	capture  audio.Capturer
	playback audio.Player
	encoder  audio.AudioEncoder
}

func (r *audioResources) close() {
	if r == nil {
		return
	}
	if r.playback != nil {
		_ = r.playback.Stop()
	}
	if r.capture != nil {
		_ = r.capture.Close()
	}
}

type connectionGeneration struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	done   chan struct{}

	disconnectOnce sync.Once
	doneOnce       sync.Once
	mu             sync.Mutex
	control        *ControlClient
	voice          *VoiceClient
	screen         *ScreenClient
	cipher         *gospeakCrypto.VoiceCipher
	audio          *audioResources
	keepaliveNow   chan struct{}
	pendingJoins   map[int64]uint32
}

func newConnectionGeneration() *connectionGeneration {
	ctx, cancel := context.WithCancel(context.Background())
	return &connectionGeneration{
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
		keepaliveNow: make(chan struct{}, 1),
		pendingJoins: make(map[int64]uint32),
	}
}

func (g *connectionGeneration) finish() {
	g.doneOnce.Do(func() { close(g.done) })
}

func (g *connectionGeneration) begin() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ctx.Err() != nil {
		return false
	}
	g.wg.Add(1)
	return true
}

func (g *connectionGeneration) run(fn func(context.Context)) bool {
	if !g.begin() {
		return false
	}
	go func() {
		defer g.wg.Done()
		fn(g.ctx)
	}()
	return true
}

func (g *connectionGeneration) startTrackedReceiver(start func(), wait func()) bool {
	if !g.begin() {
		return false
	}
	start()
	go func() {
		defer g.wg.Done()
		wait()
	}()
	return true
}

func (g *connectionGeneration) closeResources() {
	g.mu.Lock()
	g.cancel()
	resources := g.audio
	g.audio = nil
	control := g.control
	g.control = nil
	voice := g.voice
	g.voice = nil
	screen := g.screen
	g.screen = nil
	g.cipher = nil
	g.mu.Unlock()

	resources.close()
	if voice != nil {
		_ = voice.Close()
	}
	if screen != nil {
		_ = screen.Close()
	}
	if control != nil {
		_ = control.Close()
	}
}

// Engine is the main client engine that wires together audio, networking, and state.
type Engine struct {
	mu          sync.RWMutex
	lifecycleMu sync.Mutex
	callbackMu  sync.Mutex

	callbackQueueMu      sync.Mutex
	callbackQueue        []func()
	callbackQueueRunning bool
	latestCallbacks      map[latestCallbackKey]func()
	latestCallbackQueued map[latestCallbackKey]bool

	state     State
	sessionID uint32
	username  string
	role      string
	channelID int64
	muted     bool
	deafened  bool

	generation    *connectionGeneration
	disconnecting *connectionGeneration
	vad           audio.VoiceDetector

	// Per-speaker decoders and jitter buffers
	decoders        map[uint32]audio.AudioDecoder
	jitterBufs      map[uint32]*JitterBuffer
	speakerLastSeen map[uint32]time.Time
	decoderMu       sync.Mutex
	decoderFactory  audio.DecoderFactory
	now             func() time.Time

	channels []pb.ChannelInfo

	screenMu           sync.Mutex
	screenSharePending bool
	screenShareRunning bool
	screenShareDisplay int
	screenShareCancel  context.CancelFunc
	screenShareAttempt uint64
	screenCipher       *gospeakCrypto.VoiceCipher
	screenSeqNum       uint32
	activeScreenShare  *pb.ScreenShareEvent
	screenShareEnabled bool
	captureScreenFn    func(displayIndex int) (image.Image, error)
	encodeScreenFn     func(img image.Image, maxWidth, quality int) ([]byte, int32, int32, error)
	screenCaptureWait  time.Duration
	screenStartWait    time.Duration

	// Audio initialization function (allows platform-specific audio backends)
	initAudioFn func() (*audioResources, error)

	// Voice debug counters (reset each log interval; only used when debug is enabled)
	voiceDebugEnabled   bool
	voiceDebugMu        sync.Mutex
	voiceDebugSent      int64
	voiceDebugKeepalive int64
	voiceDebugRecv      int64
	voiceDebugSpeakers  map[uint32]struct{}

	// Keep-alive state
	lastSendTime time.Time
	silenceBuf   []int16

	// Callbacks for UI updates. Callbacks are serialized asynchronously and may
	// safely call synchronous Engine methods such as Connect and Disconnect.
	// To keep memory bounded when a callback blocks indefinitely, high-rate updates
	// are coalesced or dropped and even lifecycle notifications have a hard backlog cap.
	OnStateChange      func(state State)
	OnChannelsUpdate   func(channels []pb.ChannelInfo)
	OnError            func(err error)
	OnVoiceActivity    func(active bool)
	OnRMSLevel         func(level float64)
	OnDisconnect       func(reason string)
	OnChatMessage      func(channelID int64, sender, text string, ts int64)
	OnScreenShareEvent func(event *pb.ScreenShareEvent)
	OnScreenFrame      func(img image.Image)
	OnTokenCreated     func(token string)
	OnRoleChanged      func(success bool, message string)
	OnAutoToken        func(token string) // called when server auto-generates a token for this user
	OnExportData       func(dataType, data string)
	OnImportResult     func(success bool, message string)
}

// NewEngine creates a new client engine.
func NewEngine() *Engine {
	e := &Engine{
		state:              StateDisconnected,
		decoders:           make(map[uint32]audio.AudioDecoder),
		jitterBufs:         make(map[uint32]*JitterBuffer),
		speakerLastSeen:    make(map[uint32]time.Time),
		voiceDebugSpeakers: make(map[uint32]struct{}),
		vad:                audio.NewVAD(200, 15, 3), // threshold=200, hold=300ms, prebuf=60ms
		decoderFactory:     &defaultDecoderFactory{},
		now:                time.Now,
		encodeScreenFn:     screenshare.EncodeJPEG,
		screenCaptureWait:  defaultScreenShareCaptureTimeout,
		screenStartWait:    defaultScreenShareStartTimeout,
	}
	e.captureScreenFn = func(displayIndex int) (image.Image, error) {
		return screenshare.CaptureDisplay(displayIndex)
	}
	e.initAudioFn = e.initAudioDefault
	return e
}

// defaultDecoderFactory creates Opus decoders (the default audio backend).
type defaultDecoderFactory struct{}

func (f *defaultDecoderFactory) NewDecoder() (audio.AudioDecoder, error) {
	return audio.NewDecoder()
}

// Connect authenticates to the server and starts audio/voice pipelines.
func (e *Engine) Connect(controlAddr, voiceAddr, token, username, serverPin string) error {
	e.lifecycleMu.Lock()

	e.mu.Lock()
	if e.state != StateDisconnected {
		e.mu.Unlock()
		e.lifecycleMu.Unlock()
		return fmt.Errorf("already connected")
	}
	e.mu.Unlock()

	g := newConnectionGeneration()
	if !e.publishConnectingGeneration(g) {
		e.lifecycleMu.Unlock()
		return fmt.Errorf("connection canceled")
	}

	fail := func(err error) error {
		g.closeResources()
		g.wg.Wait()
		e.mu.Lock()
		if e.generation == g {
			e.generation = nil
			e.disconnecting = g
			e.state = StateDisconnected
		}
		e.mu.Unlock()
		e.notifyStateChange(StateDisconnected)
		e.finishGeneration(g)
		e.lifecycleMu.Unlock()
		return err
	}

	ctrl, err := NewControlClientContext(g.ctx, controlAddr, serverPin)
	if err != nil {
		return fail(err)
	}
	ctrlInstalled := false
	g.mu.Lock()
	if g.ctx.Err() == nil {
		g.control = ctrl
		ctrlInstalled = true
	}
	g.mu.Unlock()
	if !ctrlInstalled {
		_ = ctrl.Close()
		return fail(fmt.Errorf("connection canceled"))
	}
	if g.ctx.Err() != nil {
		return fail(fmt.Errorf("connection canceled"))
	}

	authResp, err := ctrl.Authenticate(token, username)
	if err != nil {
		return fail(err)
	}
	if g.ctx.Err() != nil {
		return fail(fmt.Errorf("connection canceled"))
	}

	slog.Info("authenticated", "session", authResp.SessionID, "user", authResp.Username, "role", authResp.Role)

	voice, err := NewVoiceClient(voiceAddr, authResp.SessionID, authResp.EncryptionKey, authResp.VoiceRegistrationKey)
	if err != nil {
		return fail(err)
	}
	voiceInstalled := false
	g.mu.Lock()
	if g.ctx.Err() == nil {
		g.voice = voice
		voiceInstalled = true
	}
	g.mu.Unlock()
	if !voiceInstalled {
		_ = voice.Close()
		return fail(fmt.Errorf("connection canceled"))
	}
	if g.ctx.Err() != nil {
		return fail(fmt.Errorf("connection canceled"))
	}

	cipher, err := gospeakCrypto.NewVoiceCipher(authResp.EncryptionKey)
	if err != nil {
		return fail(err)
	}
	g.mu.Lock()
	if g.ctx.Err() == nil {
		g.cipher = cipher
	}
	g.mu.Unlock()
	if g.ctx.Err() != nil {
		return fail(fmt.Errorf("connection canceled"))
	}

	if authResp.ScreenShareEnabled {
		screenAddr, resolveErr := resolveAdvertisedAddr(controlAddr, authResp.ScreenAddr, "9603")
		if resolveErr != nil {
			return fail(fmt.Errorf("resolve screen address: %w", resolveErr))
		}
		screen, screenErr := NewScreenClientContext(g.ctx, screenAddr, authResp.SessionID, authResp.ScreenAuthToken, ctrl.serverIdentity)
		if screenErr != nil {
			return fail(fmt.Errorf("connect screen plane: %w", screenErr))
		}
		screenInstalled := false
		g.mu.Lock()
		if g.ctx.Err() == nil {
			g.screen = screen
			screenInstalled = true
		}
		g.mu.Unlock()
		if !screenInstalled {
			_ = screen.Close()
			return fail(fmt.Errorf("connection canceled"))
		}
		if g.ctx.Err() != nil {
			return fail(fmt.Errorf("connection canceled"))
		}
	}
	if !e.publishConnectedGeneration(g, authResp) {
		return fail(fmt.Errorf("connection canceled"))
	}

	ctrl.SetEventHandler(func(msg *pb.ControlMessage) { e.handleEventGeneration(g, msg) })
	if !g.startTrackedReceiver(ctrl.StartReceiving, func() {
		<-ctrl.Done()
		e.requestDisconnect(g, "connection lost")
	}) {
		return fail(fmt.Errorf("connection canceled"))
	}
	if !g.startTrackedReceiver(voice.StartReceiving, func() { <-voice.done }) {
		return fail(fmt.Errorf("connection canceled"))
	}
	g.mu.Lock()
	screen := g.screen
	g.mu.Unlock()
	if screen != nil {
		screen.SetPacketHandler(func(pkt *protocol.ScreenPacket) { e.handleScreenPacketGeneration(g, pkt) })
		if !g.startTrackedReceiver(screen.StartReceiving, func() {
			<-screen.Done()
			e.handleScreenDisconnectGeneration(g)
		}) {
			return fail(fmt.Errorf("connection canceled"))
		}
	}

	if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		e.voiceDebugEnabled = true
		e.startVoiceDebugLogging(g)
	}
	g.run(func(context.Context) { e.keepaliveLoop(g) })
	e.startAudio(g)

	e.mu.RLock()
	current := e.generation == g && e.state == StateConnected
	e.mu.RUnlock()
	if !current || g.ctx.Err() != nil {
		return fail(fmt.Errorf("connection canceled"))
	}
	if callback := e.OnChannelsUpdate; callback != nil {
		e.invokeGenerationCallback(g, func() { callback(authResp.Channels) })
	}
	if g.ctx.Err() != nil {
		return fail(fmt.Errorf("connection canceled"))
	}

	if autoJoin, ok := selectAutoJoinChannel(authResp.Channels, authResp.ChannelScope); ok {
		if err := e.JoinChannel(autoJoin.ID); err != nil {
			slog.Warn("auto-join channel failed", "channel", autoJoin.Name, "err", err)
		}
	}
	if g.ctx.Err() != nil {
		return fail(fmt.Errorf("connection canceled"))
	}
	if callback := e.OnAutoToken; authResp.AutoToken != "" && callback != nil {
		e.invokeGenerationCallback(g, func() { callback(authResp.AutoToken) })
	}

	g.mu.Lock()
	e.mu.RLock()
	current = g.ctx.Err() == nil && e.generation == g && e.state == StateConnected
	e.mu.RUnlock()
	g.mu.Unlock()
	if !current {
		return fail(fmt.Errorf("connection canceled"))
	}

	e.lifecycleMu.Unlock()
	return nil
}

func (e *Engine) publishConnectingGeneration(g *connectionGeneration) bool {
	e.callbackMu.Lock()
	defer e.callbackMu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ctx.Err() != nil {
		return false
	}

	e.mu.Lock()
	if e.state != StateDisconnected {
		e.mu.Unlock()
		return false
	}
	e.generation = g
	e.state = StateConnecting
	e.mu.Unlock()

	if callback := e.OnStateChange; callback != nil {
		e.invokeCallback(func() { callback(StateConnecting) })
	}
	return true
}

func (e *Engine) publishConnectedGeneration(g *connectionGeneration, authResp *pb.AuthResponse) bool {
	e.callbackMu.Lock()
	defer e.callbackMu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ctx.Err() != nil {
		return false
	}

	e.mu.Lock()
	if e.generation != g || e.state != StateConnecting {
		e.mu.Unlock()
		return false
	}
	e.sessionID = authResp.SessionID
	e.username = authResp.Username
	e.role = authResp.Role
	e.channels = authResp.Channels
	e.screenShareEnabled = authResp.ScreenShareEnabled
	e.state = StateConnected
	e.mu.Unlock()

	if callback := e.OnStateChange; callback != nil {
		e.invokeCallback(func() { callback(StateConnected) })
	}
	return true
}

func selectAutoJoinChannel(channels []pb.ChannelInfo, channelScope int64) (pb.ChannelInfo, bool) {
	if channelScope == 0 {
		for _, channel := range channels {
			if channel.MaxUsers <= 0 || len(channel.Users) < int(channel.MaxUsers) {
				return channel, true
			}
		}
		return pb.ChannelInfo{}, false
	}
	for _, channel := range channels {
		if channel.ID == channelScope {
			return channel, true
		}
	}
	return pb.ChannelInfo{}, false
}

func (e *Engine) connectionClients() (*connectionGeneration, *ControlClient, *VoiceClient, *ScreenClient) {
	e.mu.RLock()
	g := e.generation
	e.mu.RUnlock()
	if g == nil {
		return nil, nil, nil, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g, g.control, g.voice, g.screen
}

// initAudioDefault initializes PortAudio devices and Opus codec (the default backend).
func (e *Engine) initAudioDefault() (*audioResources, error) {
	capture, err := audio.NewCaptureDevice(48000, 960)
	if err != nil {
		return nil, fmt.Errorf("capture device: %w", err)
	}
	if err := capture.Start(); err != nil {
		_ = capture.Close()
		return nil, fmt.Errorf("start capture: %w", err)
	}

	playback, err := audio.NewPlaybackDevice(48000, 960)
	if err != nil {
		_ = capture.Close()
		return nil, fmt.Errorf("playback device: %w", err)
	}
	if err := playback.Start(); err != nil {
		_ = capture.Close()
		return nil, fmt.Errorf("start playback: %w", err)
	}

	encoder, err := audio.NewEncoder()
	if err != nil {
		_ = playback.Stop()
		_ = capture.Close()
		return nil, fmt.Errorf("encoder: %w", err)
	}
	return &audioResources{capture: capture, playback: playback, encoder: encoder}, nil
}

func (e *Engine) startAudio(g *connectionGeneration) {
	g.run(func(context.Context) {
		resources, err := e.initAudioFn()
		if err != nil {
			slog.Error("audio init failed (continuing without audio)", "err", err)
			resources.close()
			return
		}

		g.mu.Lock()
		e.mu.RLock()
		live := e.generation == g && e.state == StateConnected && g.ctx.Err() == nil
		e.mu.RUnlock()
		if live {
			g.audio = resources
		}
		g.mu.Unlock()
		if !live {
			resources.close()
			return
		}

		e.mu.Lock()
		e.lastSendTime = time.Now()
		e.silenceBuf = make([]int16, 960)
		e.mu.Unlock()
		select {
		case g.keepaliveNow <- struct{}{}:
		default:
		}
		g.run(func(context.Context) { e.captureLoop(g) })
		g.run(func(context.Context) { e.playbackLoop(g) })
	})
}

// captureLoop reads audio from the mic, runs VAD, encodes, and sends.
func (e *Engine) captureLoop(g *connectionGeneration) {
	var timestamp uint32

	for {
		select {
		case <-g.ctx.Done():
			return
		default:
		}

		g.mu.Lock()
		resources := g.audio
		voice := g.voice
		g.mu.Unlock()
		e.mu.RLock()
		muted := e.muted
		channelID := e.channelID
		e.mu.RUnlock()
		if resources == nil || resources.capture == nil || resources.encoder == nil || voice == nil {
			return
		}

		pcm, err := resources.capture.ReadFrame()
		if err != nil {
			slog.Debug("capture read error", "err", err)
			return
		}

		// Compute RMS for VU meter
		rms := audio.GetRMS(pcm)
		if callback := e.OnRMSLevel; callback != nil {
			e.invokeLatestGenerationCallback(g, callbackRMS, func() { callback(rms) })
		}

		// VAD
		active := e.vad.Process(pcm)
		if callback := e.OnVoiceActivity; callback != nil {
			e.invokeLatestGenerationCallback(g, callbackVoiceActivity, func() { callback(active) })
		}

		// Skip sending when VAD is idle, muted, or not in a channel
		if !active || muted || channelID == 0 {
			timestamp += 960
			continue
		}

		opusData, err := resources.encoder.Encode(pcm)
		if err != nil {
			slog.Debug("encode error", "err", err)
			timestamp += 960
			continue
		}

		if err := voice.SendVoice(opusData, timestamp); err != nil {
			slog.Debug("voice send error", "err", err)
		} else if e.voiceDebugEnabled {
			e.voiceDebugMu.Lock()
			e.voiceDebugSent++
			e.voiceDebugMu.Unlock()
		}
		e.mu.Lock()
		e.lastSendTime = time.Now()
		e.mu.Unlock()

		timestamp += 960
	}
}

// playbackLoop receives voice packets, decodes, and plays them.
func (e *Engine) playbackLoop(g *connectionGeneration) {
	ticker := time.NewTicker(voiceFrameDuration)
	defer ticker.Stop()

	for {
		g.mu.Lock()
		voice := g.voice
		resources := g.audio
		g.mu.Unlock()
		e.mu.RLock()
		deafened := e.deafened
		e.mu.RUnlock()

		if voice == nil || resources == nil || resources.playback == nil {
			return
		}

		select {
		case pkt := <-voice.IncomingPackets:
			if deafened {
				continue
			}
			e.processIncomingVoice(g, pkt)
		case <-ticker.C:
			if deafened {
				continue
			}
			e.playJitterFrames(resources.playback)
		case <-g.ctx.Done():
			return
		}
	}
}

// processIncomingVoice decrypts and queues a received voice packet. Decoding
// and playback happen independently on the fixed playout clock.
func (e *Engine) processIncomingVoice(g *connectionGeneration, pkt *protocol.VoicePacket) {
	g.mu.Lock()
	cipher := g.cipher
	g.mu.Unlock()
	if cipher == nil {
		return
	}

	// Get or create decoder for this speaker
	e.decoderMu.Lock()
	_, ok := e.decoders[pkt.SessionID]
	if !ok {
		dec, err := e.decoderFactory.NewDecoder()
		if err != nil {
			e.decoderMu.Unlock()
			slog.Error("create decoder failed", "err", err)
			return
		}
		e.decoders[pkt.SessionID] = dec
		e.jitterBufs[pkt.SessionID] = NewJitterBuffer()
	}
	jb := e.jitterBufs[pkt.SessionID]
	e.speakerLastSeen[pkt.SessionID] = e.now()
	e.decoderMu.Unlock()

	// Decrypt the voice data
	header := pkt.MarshalHeader()
	opusData, err := cipher.Decrypt(pkt.SessionID, pkt.SeqNum, header, pkt.Payload)
	if err != nil {
		slog.Debug("voice decrypt failed", "session", pkt.SessionID, "err", err)
		return
	}

	// Track received packet for debug logging
	if e.voiceDebugEnabled {
		e.voiceDebugMu.Lock()
		e.voiceDebugRecv++
		e.voiceDebugSpeakers[pkt.SessionID] = struct{}{}
		e.voiceDebugMu.Unlock()
	}

	// Push to jitter buffer
	jb.Push(pkt.SeqNum, opusData)
}

type speakerPlayout struct {
	decoder audio.AudioDecoder
	buffer  *JitterBuffer
}

func (e *Engine) playJitterFrames(playback audio.Player) {
	now := e.now()
	e.decoderMu.Lock()
	speakers := make([]speakerPlayout, 0, len(e.jitterBufs))
	for sessionID, jb := range e.jitterBufs {
		if lastSeen, ok := e.speakerLastSeen[sessionID]; ok && now.Sub(lastSeen) >= speakerStateTTL {
			delete(e.decoders, sessionID)
			delete(e.jitterBufs, sessionID)
			delete(e.speakerLastSeen, sessionID)
			continue
		}
		if dec := e.decoders[sessionID]; dec != nil {
			speakers = append(speakers, speakerPlayout{decoder: dec, buffer: jb})
		}
	}
	e.decoderMu.Unlock()

	frames := make([][]int16, 0, len(speakers))
	for _, speaker := range speakers {
		data, _, ok := speaker.buffer.Pop()
		if !ok {
			continue
		}

		var (
			pcm []int16
			err error
		)
		if data == nil {
			pcm, err = speaker.decoder.DecodePLC()
		} else {
			pcm, err = speaker.decoder.Decode(data)
		}
		if err != nil {
			slog.Debug("decode error", "err", err)
			continue
		}
		frames = append(frames, pcm)
	}
	if len(frames) == 0 {
		return
	}

	if err := playback.WriteFrame(audio.MixFrames(frames, voiceFrameSamples)); err != nil {
		slog.Debug("playback error", "err", err)
	}
}

// logVoiceDebug logs voice activity for the last interval with diagnostic hints.
func (e *Engine) logVoiceDebug() {
	e.voiceDebugMu.Lock()
	sentVoice := e.voiceDebugSent
	sentKeepalive := e.voiceDebugKeepalive
	recv := e.voiceDebugRecv
	speakerCount := len(e.voiceDebugSpeakers)
	e.voiceDebugSent = 0
	e.voiceDebugKeepalive = 0
	e.voiceDebugRecv = 0
	e.voiceDebugSpeakers = make(map[uint32]struct{})
	e.voiceDebugMu.Unlock()

	e.mu.RLock()
	muted := e.muted
	deafened := e.deafened
	channelID := e.channelID
	e.mu.RUnlock()

	var hints []string
	switch {
	case muted:
		hints = append(hints, "muted")
	case sentVoice == 0 && sentKeepalive == 0:
		hints = append(hints, "no packets sent, check mic or not in a channel")
	case sentVoice == 0 && sentKeepalive > 0:
		hints = append(hints, "only keepalive packets, VAD may need adjustment")
	default:
		hints = append(hints, "sending audio")
	}
	switch {
	case deafened:
		hints = append(hints, "deafened")
	case recv == 0 && speakerCount == 0:
		hints = append(hints, "no audio received, nobody else is speaking")
	case recv > 0:
		hints = append(hints, fmt.Sprintf("receiving audio from %d speaker(s)", speakerCount))
	}

	attrs := []any{
		"sent_voice", sentVoice,
		"sent_keepalive", sentKeepalive,
		"recv", recv,
		"speakers", speakerCount,
		"muted", muted,
		"deafened", deafened,
		"channel", channelID,
	}
	if len(hints) > 0 {
		attrs = append(attrs, "hint", strings.Join(hints, "; "))
	}
	slog.Debug("voice_debug", attrs...)
}

// startVoiceDebugLogging starts a periodic goroutine that logs voice debug stats.
func (e *Engine) startVoiceDebugLogging(g *connectionGeneration) {
	g.run(func(context.Context) {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-g.ctx.Done():
				return
			case <-ticker.C:
				e.logVoiceDebug()
			}
		}
	})
}

// keepaliveLoop sends silence packets every keepAliveInterval to keep the
// server aware of our UDP address. It is independent of VAD state so that
// the connection is maintained during silence. It also sends immediately
// when signalled via keepaliveNow (e.g. right after joining a channel).
func (e *Engine) keepaliveLoop(g *connectionGeneration) {
	var timestamp uint32

	ticker := time.NewTicker(keepAliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-g.ctx.Done():
			return
		case <-g.keepaliveNow:
			e.tryRegisterVoiceEndpoint(g)
			e.trySendKeepalive(g, &timestamp, false)
		case <-ticker.C:
			e.tryRegisterVoiceEndpoint(g)
			e.trySendKeepalive(g, &timestamp, true)
		}
	}
}

func (e *Engine) tryRegisterVoiceEndpoint(g *connectionGeneration) {
	g.mu.Lock()
	voice := g.voice
	g.mu.Unlock()
	if voice != nil {
		if err := voice.SendRegistration(); err != nil {
			slog.Debug("voice endpoint registration error", "err", err)
		}
	}
}

// trySendKeepalive sends a single silence keepalive packet if the voice
// pipeline is ready and we are in a channel. When respectRecency is true it
// skips sending if a packet was already sent within keepAliveInterval.
func (e *Engine) trySendKeepalive(g *connectionGeneration, timestamp *uint32, respectRecency bool) {
	g.mu.Lock()
	voice := g.voice
	resources := g.audio
	g.mu.Unlock()
	e.mu.RLock()
	channelID := e.channelID
	lastSend := e.lastSendTime
	silenceBuf := e.silenceBuf
	e.mu.RUnlock()

	if voice == nil || resources == nil || resources.encoder == nil || channelID == 0 || silenceBuf == nil {
		return
	}

	if respectRecency && time.Since(lastSend) < keepAliveInterval {
		return
	}

	silenceData, err := resources.encoder.Encode(silenceBuf)
	if err != nil {
		slog.Debug("keepalive encode error", "err", err)
		return
	}

	if err := voice.SendVoice(silenceData, *timestamp); err != nil {
		slog.Debug("keepalive send error", "err", err)
		return
	}
	*timestamp += 960

	if e.voiceDebugEnabled {
		e.voiceDebugMu.Lock()
		e.voiceDebugKeepalive++
		e.voiceDebugMu.Unlock()
	}

	e.mu.Lock()
	e.lastSendTime = time.Now()
	e.mu.Unlock()
}

// handleEvent dispatches incoming server events.
func (e *Engine) handleEventGeneration(g *connectionGeneration, msg *pb.ControlMessage) {
	e.callbackMu.Lock()
	defer e.callbackMu.Unlock()
	e.mu.RLock()
	current := e.generation == g && e.state == StateConnected
	e.mu.RUnlock()
	if current {
		e.handleEvent(g, msg)
	}
}

func (e *Engine) handleEvent(g *connectionGeneration, msg *pb.ControlMessage) {
	switch {
	case msg.ServerStateEvent != nil:
		e.mu.Lock()
		e.channels = msg.ServerStateEvent.Channels
		e.screenShareEnabled = msg.ServerStateEvent.ScreenShareEnabled
		e.mu.Unlock()
		if callback := e.OnChannelsUpdate; callback != nil {
			e.enqueueGenerationCallbackLocked(g, func() { callback(msg.ServerStateEvent.Channels) })
		}

	case msg.ChannelJoinedEvent != nil:
		// Refresh will come via ServerStateEvent
		slog.Info("user joined channel",
			"user", msg.ChannelJoinedEvent.User.Username,
			"channel", msg.ChannelJoinedEvent.ChannelID,
		)

	case msg.ChannelJoinResponse != nil:
		response := msg.ChannelJoinResponse
		g.mu.Lock()
		pendingCount := g.pendingJoins[response.ChannelID]
		pending := pendingCount > 0
		if pendingCount <= 1 {
			delete(g.pendingJoins, response.ChannelID)
		} else {
			g.pendingJoins[response.ChannelID] = pendingCount - 1
		}
		voice := g.voice
		applied := false
		if pending && response.Success && g.ctx.Err() == nil {
			e.mu.Lock()
			if e.generation == g && e.state == StateConnected {
				e.channelID = response.ChannelID
				if voice != nil {
					voice.SetChannel(response.ChannelID)
				}
				applied = true
			}
			e.mu.Unlock()
		}
		g.mu.Unlock()
		if !pending {
			return
		}
		if !response.Success {
			if callback := e.OnError; callback != nil {
				err := fmt.Errorf("join channel: %s", response.Message)
				e.enqueueGenerationCallbackLocked(g, func() { callback(err) })
			}
			return
		}
		if applied {
			e.clearScreenShareStateGenerationLocked(g)
			select {
			case g.keepaliveNow <- struct{}{}:
			default:
			}
		}

	case msg.ChannelLeftEvent != nil:
		slog.Info("user left channel",
			"user", msg.ChannelLeftEvent.Username,
			"channel", msg.ChannelLeftEvent.ChannelID,
		)
		// Clean up decoder for departed user
		e.decoderMu.Lock()
		// We'd need to map userID→sessionID; for MVP just leave decoders

		e.decoderMu.Unlock()

	case msg.ErrorResponse != nil:
		slog.Error("server error", "code", msg.ErrorResponse.Code, "msg", msg.ErrorResponse.Message)
		if msg.ErrorResponse.Code == 40 && strings.Contains(strings.ToLower(msg.ErrorResponse.Message), "screen") {
			e.stopScreenShareLoop()
			e.screenMu.Lock()
			e.screenSharePending = false
			e.screenMu.Unlock()
		}
		if msg.ErrorResponse.Code == 99 {
			// Kicked or banned
			e.requestDisconnect(g, msg.ErrorResponse.Message)
		}
		if callback := e.OnError; callback != nil {
			e.enqueueGenerationCallbackLocked(g, func() { callback(fmt.Errorf("server: %s", msg.ErrorResponse.Message)) })
		}

	case msg.Pong != nil:
		// Ping/pong handled silently

	case msg.CreateTokenResp != nil:
		slog.Debug("token created", "token", msg.CreateTokenResp.Token)
		if callback := e.OnTokenCreated; callback != nil {
			e.enqueueGenerationCallbackLocked(g, func() { callback(msg.CreateTokenResp.Token) })
		}

	case msg.ChatEvent != nil:
		if callback := e.OnChatMessage; callback != nil {
			e.enqueueGenerationCallbackLocked(g, func() {
				callback(msg.ChatEvent.ChannelID, msg.ChatEvent.SenderName, msg.ChatEvent.Text, msg.ChatEvent.Timestamp)
			})
		}

	case msg.ScreenShareEvent != nil:
		e.handleScreenShareEvent(g, msg.ScreenShareEvent)

	case msg.SetUserRoleResp != nil:
		if callback := e.OnRoleChanged; callback != nil {
			e.enqueueGenerationCallbackLocked(g, func() { callback(msg.SetUserRoleResp.Success, msg.SetUserRoleResp.Message) })
		}

	case msg.ExportDataResp != nil:
		if callback := e.OnExportData; callback != nil {
			e.enqueueGenerationCallbackLocked(g, func() { callback(msg.ExportDataResp.Type, msg.ExportDataResp.Data) })
		}

	case msg.ImportChannelsResp != nil:
		if callback := e.OnImportResult; callback != nil {
			e.enqueueGenerationCallbackLocked(g, func() {
				callback(msg.ImportChannelsResp.Success, msg.ImportChannelsResp.Message)
			})
		}
	}
}

func (e *Engine) beginControlOperation() (*connectionGeneration, *ControlClient, bool) {
	g, ctrl, _, _ := e.connectionClients()
	if g == nil || ctrl == nil || !g.begin() {
		return nil, nil, false
	}
	return g, ctrl, true
}

// JoinChannel sends a request to join a channel. Local channel state changes
// only after the server returns a successful ChannelJoinResponse.
func (e *Engine) JoinChannel(channelID int64) error {
	g, ctrl, _, _ := e.connectionClients()

	if ctrl == nil || g == nil || !g.begin() {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()

	g.mu.Lock()
	g.pendingJoins[channelID]++
	g.mu.Unlock()
	if err := ctrl.Send(&pb.ControlMessage{
		JoinChannelRequest: &pb.JoinChannelRequest{ChannelID: channelID},
	}); err != nil {
		g.mu.Lock()
		if g.pendingJoins[channelID] <= 1 {
			delete(g.pendingJoins, channelID)
		} else {
			g.pendingJoins[channelID]--
		}
		g.mu.Unlock()
		return err
	}

	return nil
}

// LeaveChannel sends a request to leave the current channel.
func (e *Engine) LeaveChannel() error {
	g, ctrl, _, _ := e.connectionClients()

	if ctrl == nil || g == nil || !g.begin() {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()

	if err := ctrl.Send(&pb.ControlMessage{
		LeaveChannelRequest: &pb.LeaveChannelRequest{},
	}); err != nil {
		return err
	}

	g.mu.Lock()
	clear(g.pendingJoins)
	voice := g.voice
	e.mu.Lock()
	if e.generation != g || e.state != StateConnected || g.ctx.Err() != nil {
		e.mu.Unlock()
		g.mu.Unlock()
		return fmt.Errorf("not connected")
	}
	e.channelID = 0
	if voice != nil {
		voice.SetChannel(0)
	}
	e.mu.Unlock()
	g.mu.Unlock()
	e.clearScreenShareStateGeneration(g)

	return nil
}

// SetMuted toggles mute state.
func (e *Engine) SetMuted(muted bool) {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return
	}
	defer g.wg.Done()

	deafened, current := e.updateMutedGeneration(g, muted)
	if !current {
		return
	}
	_ = ctrl.Send(&pb.ControlMessage{
		UserStateUpdate: &pb.UserStateUpdate{Muted: muted, Deafened: deafened},
	})
}

func (e *Engine) updateMutedGeneration(g *connectionGeneration, muted bool) (bool, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if g.ctx.Err() != nil || e.generation != g || e.state != StateConnected {
		return e.deafened, false
	}
	e.muted = muted
	return e.deafened, true
}

// SetDeafened toggles deafen state.
func (e *Engine) SetDeafened(deafened bool) {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return
	}
	defer g.wg.Done()

	muted, reset, current := e.updateDeafenedGeneration(g, deafened)
	if !current {
		return
	}
	if reset {
		e.resetReceiveState()
	}
	_ = ctrl.Send(&pb.ControlMessage{
		UserStateUpdate: &pb.UserStateUpdate{Muted: muted, Deafened: deafened},
	})
}

func (e *Engine) updateDeafenedGeneration(g *connectionGeneration, deafened bool) (bool, bool, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if g.ctx.Err() != nil || e.generation != g || e.state != StateConnected {
		return e.muted, false, false
	}
	reset := e.deafened && !deafened
	e.deafened = deafened
	return e.muted, reset, true
}

// SetVADThreshold updates the VAD sensitivity.
func (e *Engine) SetVADThreshold(threshold float64) {
	e.vad.SetThreshold(threshold)
}

// CreateChannel sends a create channel request (admin only).
func (e *Engine) CreateChannel(name, description string, maxUsers int) error {
	return e.CreateChannelAdvanced(name, description, maxUsers, 0, false, false)
}

// CreateChannelAdvanced sends a create channel request with all options.
func (e *Engine) CreateChannelAdvanced(name, description string, maxUsers int, parentID int64, isTemp bool, allowSubChannels bool) error {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()

	return ctrl.Send(&pb.ControlMessage{
		CreateChannelReq: &pb.CreateChannelRequest{
			Name:             name,
			Description:      description,
			MaxUsers:         int32(maxUsers), //nolint:gosec // practical channel limits fit int32
			ParentID:         parentID,
			IsTemp:           isTemp,
			AllowSubChannels: allowSubChannels,
		},
	})
}

// CreateSubChannel creates a temporary sub-channel under a parent.
func (e *Engine) CreateSubChannel(parentID int64, name string) error {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()

	return ctrl.Send(&pb.ControlMessage{
		CreateChannelReq: &pb.CreateChannelRequest{
			Name:     name,
			ParentID: parentID,
			IsTemp:   true,
		},
	})
}

// DeleteChannel sends a delete channel request (admin only).
func (e *Engine) DeleteChannel(channelID int64) error {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()

	return ctrl.Send(&pb.ControlMessage{
		DeleteChannelReq: &pb.DeleteChannelRequest{ChannelID: channelID},
	})
}

// ExportData requests the server to export data ("channels" or "users") as YAML.
func (e *Engine) ExportData(dataType string) error {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()

	return ctrl.Send(&pb.ControlMessage{
		ExportDataReq: &pb.ExportDataRequest{Type: dataType},
	})
}

// ImportChannels sends a YAML blob for the server to import as channels.
func (e *Engine) ImportChannels(yamlData string) error {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()

	return ctrl.Send(&pb.ControlMessage{
		ImportChannelsReq: &pb.ImportChannelsRequest{YAML: yamlData},
	})
}

// CreateToken sends a create token request (admin only).
func (e *Engine) CreateToken(role string, maxUses int, expiresInSeconds int64) error {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()

	return ctrl.Send(&pb.ControlMessage{
		CreateTokenReq: &pb.CreateTokenRequest{
			Role:             role,
			MaxUses:          int32(maxUses), //nolint:gosec // practical token limits fit int32
			ExpiresInSeconds: expiresInSeconds,
		},
	})
}

// SendChat sends a text message to the current channel.
func (e *Engine) SendChat(text string) error {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()

	g.mu.Lock()
	e.mu.RLock()
	current := g.ctx.Err() == nil && e.generation == g && e.state == StateConnected
	channelID := e.channelID
	e.mu.RUnlock()
	g.mu.Unlock()
	if !current {
		return fmt.Errorf("not connected")
	}
	if channelID == 0 {
		return fmt.Errorf("not in a channel")
	}

	return ctrl.Send(&pb.ControlMessage{
		ChatMsg: &pb.ChatMessage{
			ChannelID: channelID,
			Text:      text,
		},
	})
}

// StartScreenShare announces a screen share and begins streaming once the server confirms it.
func (e *Engine) StartScreenShare(displayIndex int) error {
	g, ctrl, _, _ := e.connectionClients()
	if g == nil || !g.begin() {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()
	e.mu.RLock()
	channelID := e.channelID
	mySessionID := e.sessionID
	e.mu.RUnlock()
	e.screenMu.Lock()
	active := e.activeScreenShare
	e.screenMu.Unlock()

	if ctrl == nil {
		return fmt.Errorf("not connected")
	}
	if channelID == 0 {
		return fmt.Errorf("not in a channel")
	}
	if active != nil && active.Active {
		if active.SessionID == mySessionID {
			return fmt.Errorf("screen share already active")
		}
		return fmt.Errorf("another user is already sharing their screen in this channel")
	}
	if displayIndex < 0 || displayIndex > math.MaxInt32 {
		return fmt.Errorf("display index out of range: %d", displayIndex)
	}

	e.screenMu.Lock()
	if e.screenShareRunning || e.screenSharePending {
		e.screenMu.Unlock()
		return fmt.Errorf("screen share already active")
	}
	e.screenMu.Unlock()

	_, width, height, err := e.prepareScreenShareFrame(g.ctx, displayIndex)
	if err != nil {
		return fmt.Errorf("prepare screen share: %w", err)
	}

	e.callbackMu.Lock()
	e.mu.RLock()
	current := e.generation == g && e.state == StateConnected && e.channelID == channelID
	e.mu.RUnlock()
	g.mu.Lock()
	activeGeneration := g.ctx.Err() == nil && g.control == ctrl
	g.mu.Unlock()
	if !current || !activeGeneration {
		e.callbackMu.Unlock()
		return fmt.Errorf("not connected")
	}
	e.screenMu.Lock()
	if e.screenShareRunning || e.screenSharePending {
		e.screenMu.Unlock()
		e.callbackMu.Unlock()
		return fmt.Errorf("screen share already active")
	}
	e.screenSharePending = true
	e.screenShareDisplay = displayIndex
	e.screenShareAttempt++
	attempt := e.screenShareAttempt
	startWait := e.screenStartWait
	e.screenMu.Unlock()
	e.callbackMu.Unlock()

	if err := ctrl.Send(&pb.ControlMessage{
		ScreenShareStartReq: &pb.ScreenShareStartRequest{
			DisplayIndex: int32(displayIndex),
			Width:        width,
			Height:       height,
		},
	}); err != nil {
		e.screenMu.Lock()
		if e.screenShareAttempt == attempt {
			e.screenSharePending = false
		}
		e.screenMu.Unlock()
		return err
	}
	if !g.run(func(context.Context) { e.watchScreenShareStart(g, attempt, startWait) }) {
		e.screenMu.Lock()
		if e.screenShareAttempt == attempt {
			e.screenSharePending = false
		}
		e.screenMu.Unlock()
		return fmt.Errorf("not connected")
	}

	return nil
}

func (e *Engine) StopScreenShare() error {
	g, ctrl, _, _ := e.connectionClients()
	if g == nil || ctrl == nil || !g.begin() {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()

	e.callbackMu.Lock()
	e.mu.RLock()
	current := e.generation == g && e.state == StateConnected
	e.mu.RUnlock()
	g.mu.Lock()
	activeGeneration := g.ctx.Err() == nil && g.control == ctrl
	g.mu.Unlock()
	if !current || !activeGeneration {
		e.callbackMu.Unlock()
		return fmt.Errorf("not connected")
	}
	e.stopScreenShareLoop()
	e.screenMu.Lock()
	e.screenSharePending = false
	e.screenMu.Unlock()
	e.callbackMu.Unlock()

	return ctrl.Send(&pb.ControlMessage{ScreenShareStopReq: &pb.ScreenShareStopRequest{}})
}

func (e *Engine) SubscribeScreenShare(channelID int64) error {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()
	return ctrl.Send(&pb.ControlMessage{ScreenShareSubReq: &pb.ScreenShareSubscribeRequest{ChannelID: channelID}})
}

func (e *Engine) ShareScreenShareWithChannel() error {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()
	return ctrl.Send(&pb.ControlMessage{ScreenShareShareReq: &pb.ScreenShareShareRequest{}})
}

func (e *Engine) UnsubscribeScreenShare() error {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()
	return ctrl.Send(&pb.ControlMessage{ScreenShareUnsubReq: &pb.ScreenShareUnsubscribeRequest{}})
}

// SetUserRole sends a role change request (admin only).
func (e *Engine) SetUserRole(targetUserID int64, newRole string) error {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()

	return ctrl.Send(&pb.ControlMessage{
		SetUserRoleReq: &pb.SetUserRoleRequest{
			TargetUserID: targetUserID,
			NewRole:      newRole,
		},
	})
}

// KickUser sends a kick request (admin/mod only).
func (e *Engine) KickUser(userID int64, reason string) error {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()

	return ctrl.Send(&pb.ControlMessage{
		KickUserReq: &pb.KickUserRequest{UserID: userID, Reason: reason},
	})
}

// BanUser sends a ban request (admin only).
func (e *Engine) BanUser(userID int64, reason string, durationSeconds int64) error {
	g, ctrl, ok := e.beginControlOperation()
	if !ok {
		return fmt.Errorf("not connected")
	}
	defer g.wg.Done()

	return ctrl.Send(&pb.ControlMessage{
		BanUserReq: &pb.BanUserRequest{UserID: userID, Reason: reason, DurationSeconds: durationSeconds},
	})
}

// Disconnect disconnects from the server.
func (e *Engine) Disconnect() {
	e.mu.RLock()
	g := e.generation
	if g == nil {
		g = e.disconnecting
	}
	e.mu.RUnlock()
	if g != nil {
		e.requestDisconnect(g, "user disconnected")
		<-g.done
	}
}

// GetState returns the current connection state.
func (e *Engine) GetState() State {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state
}

// GetUsername returns the authenticated username.
func (e *Engine) GetUsername() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.username
}

// GetSessionID returns the authenticated session ID.
func (e *Engine) GetSessionID() uint32 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.sessionID
}

// IsScreenShareEnabled returns whether the server advertised screen sharing support.
func (e *Engine) IsScreenShareEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.screenShareEnabled
}

// GetRole returns the user's role.
func (e *Engine) GetRole() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.role
}

// GetChannels returns the current channel list.
func (e *Engine) GetChannels() []pb.ChannelInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]pb.ChannelInfo, len(e.channels))
	copy(result, e.channels)
	return result
}

// GetChannelID returns the current channel ID (0 if none).
func (e *Engine) GetChannelID() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.channelID
}

// IsMuted returns whether the client is muted.
func (e *Engine) IsMuted() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.muted
}

// IsDeafened returns whether the client is deafened.
func (e *Engine) IsDeafened() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.deafened
}

func (e *Engine) resetReceiveState() {
	e.decoderMu.Lock()
	e.decoders = make(map[uint32]audio.AudioDecoder)
	e.jitterBufs = make(map[uint32]*JitterBuffer)
	e.speakerLastSeen = make(map[uint32]time.Time)
	e.decoderMu.Unlock()
}

func (e *Engine) requestDisconnect(g *connectionGeneration, reason string) {
	g.mu.Lock()
	g.cancel()
	g.mu.Unlock()
	g.disconnectOnce.Do(func() {
		go func() {
			g.closeResources()
			e.handleDisconnectGeneration(g, reason)
			e.finishGeneration(g)
		}()
	})
}

func (e *Engine) finishGeneration(g *connectionGeneration) {
	g.finish()
	e.mu.Lock()
	if e.disconnecting == g {
		e.disconnecting = nil
	}
	e.mu.Unlock()
}

func (e *Engine) handleDisconnectGeneration(g *connectionGeneration, reason string) {
	e.lifecycleMu.Lock()
	e.callbackMu.Lock()
	e.mu.Lock()
	if e.generation != g {
		e.mu.Unlock()
		e.callbackMu.Unlock()
		e.lifecycleMu.Unlock()
		return
	}
	e.generation = nil
	e.disconnecting = g
	e.channelID = 0
	e.muted = false
	e.deafened = false
	e.screenShareEnabled = false
	e.mu.Unlock()
	e.callbackMu.Unlock()

	e.stopScreenShareLoop()
	e.screenMu.Lock()
	e.activeScreenShare = nil
	e.screenCipher = nil
	e.screenSeqNum = 0
	e.screenMu.Unlock()

	g.closeResources()
	g.wg.Wait()
	e.resetReceiveState()

	e.mu.Lock()
	e.state = StateDisconnected
	e.mu.Unlock()
	slog.Info("disconnected", "reason", reason)
	e.callbackMu.Lock()
	if callback := e.OnStateChange; callback != nil {
		e.invokeCallback(func() { callback(StateDisconnected) })
	}
	if callback := e.OnDisconnect; callback != nil {
		e.invokeCallback(func() { callback(reason) })
	}
	e.callbackMu.Unlock()
	e.finishGeneration(g)
	e.lifecycleMu.Unlock()
}

func (e *Engine) handleScreenShareEvent(g *connectionGeneration, event *pb.ScreenShareEvent) {
	if event == nil {
		e.clearScreenShareStateGenerationLocked(g)
		return
	}

	e.mu.RLock()
	mySessionID := e.sessionID
	channelID := e.channelID
	e.mu.RUnlock()

	if event.SessionID == mySessionID {
		if event.Active {
			e.screenMu.Lock()
			pending := e.screenSharePending
			e.screenMu.Unlock()
			if pending {
				e.startScreenShareLoop(g)
			}
		} else {
			e.stopScreenShareLoop()
			e.screenMu.Lock()
			e.screenSharePending = false
			e.screenMu.Unlock()
		}
	}

	e.screenMu.Lock()
	previous := e.activeScreenShare
	if !event.Active {
		if previous == nil || previous.SessionID == event.SessionID {
			e.activeScreenShare = nil
			e.screenCipher = nil
			e.screenSeqNum = 0
		}
	} else if len(event.EncryptionKey) > 0 {
		cipher, err := gospeakCrypto.NewVoiceCipher(event.EncryptionKey)
		if err != nil {
			e.screenMu.Unlock()
			slog.Error("screen cipher init failed", "err", err)
			return
		}
		e.activeScreenShare = event
		e.screenCipher = cipher
		e.screenSeqNum = 0
	} else {
		e.activeScreenShare = event
		if previous == nil || previous.SessionID != event.SessionID {
			e.screenCipher = nil
			e.screenSeqNum = 0
		}
	}
	e.screenMu.Unlock()

	if callback := e.OnScreenFrame; !event.Active && callback != nil {
		e.enqueueGenerationCallbackLocked(g, func() { callback(nil) })
	}
	if event.ChannelID != 0 && channelID != 0 && event.ChannelID != channelID {
		return
	}
	if callback := e.OnScreenShareEvent; callback != nil {
		e.enqueueGenerationCallbackLocked(g, func() { callback(event) })
	}
}

func (e *Engine) clearScreenShareState() {
	e.callbackMu.Lock()
	e.clearScreenShareStateGenerationLocked(nil)
	e.callbackMu.Unlock()
}

func (e *Engine) clearScreenShareStateGeneration(g *connectionGeneration) {
	e.callbackMu.Lock()
	e.clearScreenShareStateGenerationLocked(g)
	e.callbackMu.Unlock()
}

func (e *Engine) clearScreenShareStateGenerationLocked(g *connectionGeneration) {
	e.stopScreenShareLoop()
	e.screenMu.Lock()
	e.activeScreenShare = nil
	e.screenCipher = nil
	e.screenSeqNum = 0
	e.screenMu.Unlock()
	if callback := e.OnScreenFrame; callback != nil {
		e.enqueueOptionalGenerationCallbackLocked(g, func() { callback(nil) })
	}
	if callback := e.OnScreenShareEvent; callback != nil {
		e.enqueueOptionalGenerationCallbackLocked(g, func() { callback(nil) })
	}
}

func (e *Engine) handleScreenPacketGeneration(g *connectionGeneration, pkt *protocol.ScreenPacket) {
	e.callbackMu.Lock()
	defer e.callbackMu.Unlock()
	e.mu.RLock()
	current := e.generation == g && e.state == StateConnected
	e.mu.RUnlock()
	if current {
		e.handleScreenPacket(g, pkt)
	}
}

func (e *Engine) handleScreenPacket(g *connectionGeneration, pkt *protocol.ScreenPacket) {
	e.screenMu.Lock()
	cipher := e.screenCipher
	event := e.activeScreenShare
	e.screenMu.Unlock()
	if cipher == nil || event == nil || !event.Active || pkt == nil || pkt.SessionID != event.SessionID {
		return
	}
	frameData, err := cipher.Decrypt(pkt.SessionID, pkt.SeqNum, pkt.MarshalHeader(), pkt.Payload)
	if err != nil {
		slog.Debug("screen frame decrypt error", "err", err)
		return
	}
	frame, err := protocol.UnmarshalScreenFrame(frameData)
	if err != nil {
		slog.Debug("screen frame unmarshal error", "err", err)
		return
	}
	img, _, err := image.Decode(bytes.NewReader(frame.Data))
	if err != nil {
		slog.Debug("screen frame decode error", "err", err)
		return
	}
	if callback := e.OnScreenFrame; callback != nil {
		e.enqueueLatestGenerationCallbackLocked(g, callbackScreenFrame, func() { callback(img) })
	}
}

func (e *Engine) startScreenShareLoop(g *connectionGeneration) {
	e.screenMu.Lock()
	if e.screenShareRunning {
		e.screenSharePending = false
		e.screenMu.Unlock()
		return
	}
	displayIndex := e.screenShareDisplay
	ctx, cancel := context.WithCancel(g.ctx)
	e.screenShareCancel = cancel
	e.screenShareRunning = true
	e.screenSharePending = false
	e.screenMu.Unlock()

	g.run(func(context.Context) {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		defer e.stopScreenShareLoop()

		for {
			if err := e.sendScreenShareFrame(g, displayIndex); err != nil {
				if errors.Is(err, errScreenShareCaptureTimedOut) {
					e.requestScreenShareStop(g)
					if callback := e.OnError; callback != nil {
						e.invokeGenerationCallback(g, func() { callback(fmt.Errorf("screen share stopped: %w", err)) })
					}
					return
				}
				slog.Debug("screen share send failed", "err", err)
			}

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (e *Engine) stopScreenShareLoop() {
	e.screenMu.Lock()
	cancel := e.screenShareCancel
	e.screenShareCancel = nil
	e.screenShareRunning = false
	e.screenSharePending = false
	e.screenMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *Engine) handleScreenDisconnectGeneration(g *connectionGeneration) {
	e.callbackMu.Lock()
	defer e.callbackMu.Unlock()
	e.mu.Lock()
	if e.generation != g || e.state != StateConnected {
		e.mu.Unlock()
		return
	}
	e.screenShareEnabled = false
	e.mu.Unlock()

	g.mu.Lock()
	if g.screen == nil {
		g.mu.Unlock()
		return
	}
	screen := g.screen
	g.screen = nil
	g.mu.Unlock()
	_ = screen.Close()

	e.stopScreenShareLoop()
	e.screenMu.Lock()
	e.activeScreenShare = nil
	e.screenCipher = nil
	e.screenSeqNum = 0
	e.screenMu.Unlock()

	if callback := e.OnScreenFrame; callback != nil {
		e.enqueueGenerationCallbackLocked(g, func() { callback(nil) })
	}
	if callback := e.OnScreenShareEvent; callback != nil {
		e.enqueueGenerationCallbackLocked(g, func() { callback(&pb.ScreenShareEvent{Active: false}) })
	}
	if callback := e.OnChannelsUpdate; callback != nil {
		channels := e.GetChannels()
		e.enqueueGenerationCallbackLocked(g, func() { callback(channels) })
	}
	if callback := e.OnError; callback != nil {
		e.enqueueGenerationCallbackLocked(g, func() { callback(fmt.Errorf("screen share connection lost")) })
	}
}

func (e *Engine) sendScreenShareFrame(g *connectionGeneration, displayIndex int) error {
	g.mu.Lock()
	screen := g.screen
	g.mu.Unlock()
	e.mu.RLock()
	sessionID := e.sessionID
	e.mu.RUnlock()

	if screen == nil {
		return fmt.Errorf("not connected")
	}
	data, width, height, err := e.prepareScreenShareFrame(g.ctx, displayIndex)
	if err != nil {
		return err
	}
	frameData, err := protocol.MarshalScreenFrame(&protocol.ScreenFrame{
		Timestamp: time.Now().UnixMilli(),
		Width:     width,
		Height:    height,
		Format:    "jpeg",
		Data:      data,
	})
	if err != nil {
		return err
	}
	e.screenMu.Lock()
	cipher := e.screenCipher
	if cipher == nil {
		e.screenMu.Unlock()
		return fmt.Errorf("screen share encryption is not ready")
	}
	e.screenSeqNum++
	seqNum := e.screenSeqNum
	e.screenMu.Unlock()
	pkt := &protocol.ScreenPacket{SessionID: sessionID, SeqNum: seqNum}
	pkt.Payload = cipher.Encrypt(sessionID, seqNum, pkt.MarshalHeader(), frameData)
	return screen.Send(pkt)
}

type screenSharePrepareResult struct {
	data   []byte
	width  int32
	height int32
	err    error
}

func (e *Engine) prepareScreenShareFrame(ctx context.Context, displayIndex int) ([]byte, int32, int32, error) {
	timeout := e.screenCaptureWait
	if timeout <= 0 {
		timeout = defaultScreenShareCaptureTimeout
	}

	// Native display capture cannot be force-canceled on every backend. Serialize
	// helpers process-wide so a stuck backend cannot create an unbounded goroutine backlog.
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case screenCaptureSlot <- struct{}{}:
	case <-timer.C:
		return nil, 0, 0, errScreenShareCaptureTimedOut
	case <-ctx.Done():
		return nil, 0, 0, fmt.Errorf("not connected")
	}

	resultCh := make(chan screenSharePrepareResult, 1)
	go func() {
		defer func() { <-screenCaptureSlot }()
		img, err := e.captureScreenFn(displayIndex)
		if err != nil {
			resultCh <- screenSharePrepareResult{err: err}
			return
		}
		data, width, height, err := e.encodeScreenFn(img, screenshare.DefaultMaxWidth, screenshare.DefaultJPEGQuality)
		resultCh <- screenSharePrepareResult{data: data, width: width, height: height, err: err}
	}()

	select {
	case result := <-resultCh:
		return result.data, result.width, result.height, result.err
	case <-timer.C:
		return nil, 0, 0, errScreenShareCaptureTimedOut
	case <-ctx.Done():
		return nil, 0, 0, fmt.Errorf("not connected")
	}
}

func (e *Engine) watchScreenShareStart(g *connectionGeneration, attempt uint64, timeout time.Duration) {
	if timeout <= 0 {
		timeout = defaultScreenShareStartTimeout
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-g.ctx.Done():
		return
	}

	e.screenMu.Lock()
	if !e.screenSharePending || e.screenShareRunning || e.screenShareAttempt != attempt {
		e.screenMu.Unlock()
		return
	}
	e.screenSharePending = false
	e.screenMu.Unlock()

	e.requestScreenShareStop(g)
	if callback := e.OnError; callback != nil {
		e.invokeGenerationCallback(g, func() { callback(errScreenShareStartTimedOut) })
	}
}

func (e *Engine) requestScreenShareStop(g *connectionGeneration) {
	g.mu.Lock()
	ctrl := g.control
	active := g.ctx.Err() == nil
	g.mu.Unlock()
	if ctrl == nil || !active {
		return
	}
	if err := ctrl.Send(&pb.ControlMessage{ScreenShareStopReq: &pb.ScreenShareStopRequest{}}); err != nil {
		slog.Debug("screen share stop request failed", "err", err)
	}
}

// invokeCallback serializes user callbacks outside lifecycle workers. Producers never
// wait for callback completion, so callbacks may safely call synchronous Engine methods.
func (e *Engine) invokeCallback(fn func()) {
	e.enqueueCallback(fn, true)
}

func (e *Engine) enqueueCallback(fn func(), reliable bool) bool {
	e.callbackQueueMu.Lock()
	limit := maxCallbackQueue
	if reliable {
		limit = maxReliableCallbackQueue
	}
	if len(e.callbackQueue) >= limit {
		e.callbackQueueMu.Unlock()
		if reliable {
			slog.Error("reliable client callback dropped because callback queue is full", "limit", limit)
		}
		return false
	}
	e.callbackQueue = append(e.callbackQueue, fn)
	startRunner := !e.callbackQueueRunning
	if startRunner {
		e.callbackQueueRunning = true
	}
	e.callbackQueueMu.Unlock()
	if startRunner {
		go e.runCallbackQueue()
	}
	return true
}

func (e *Engine) runCallbackQueue() {
	for {
		e.callbackQueueMu.Lock()
		if len(e.callbackQueue) == 0 {
			e.callbackQueueRunning = false
			e.callbackQueueMu.Unlock()
			return
		}
		fn := e.callbackQueue[0]
		e.callbackQueue[0] = nil
		e.callbackQueue = e.callbackQueue[1:]
		e.callbackQueueMu.Unlock()
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.Error("client callback panic", "panic", recovered)
				}
			}()
			fn()
		}()
	}
}

func (e *Engine) enqueueGenerationCallbackLocked(g *connectionGeneration, fn func()) {
	e.enqueueQualifiedGenerationCallbackLocked(g, fn, false)
}

func (e *Engine) enqueueReliableGenerationCallbackLocked(g *connectionGeneration, fn func()) {
	e.enqueueQualifiedGenerationCallbackLocked(g, fn, true)
}

func (e *Engine) enqueueQualifiedGenerationCallbackLocked(g *connectionGeneration, fn func(), reliable bool) {
	e.mu.RLock()
	current := e.generation == g
	e.mu.RUnlock()
	g.mu.Lock()
	active := g.ctx.Err() == nil
	if current && active {
		e.enqueueCallback(fn, reliable)
	}
	g.mu.Unlock()
}

func (e *Engine) invokeGenerationCallback(g *connectionGeneration, fn func()) {
	e.callbackMu.Lock()
	e.enqueueGenerationCallbackLocked(g, fn)
	e.callbackMu.Unlock()
}

func (e *Engine) invokeLatestGenerationCallback(g *connectionGeneration, kind callbackKind, fn func()) {
	e.callbackMu.Lock()
	e.mu.RLock()
	current := e.generation == g
	e.mu.RUnlock()
	g.mu.Lock()
	active := g.ctx.Err() == nil
	if current && active {
		e.enqueueLatestCallback(latestCallbackKey{generation: g, kind: kind}, fn)
	}
	g.mu.Unlock()
	e.callbackMu.Unlock()
}

func (e *Engine) enqueueLatestGenerationCallbackLocked(g *connectionGeneration, kind callbackKind, fn func()) {
	e.mu.RLock()
	current := e.generation == g
	e.mu.RUnlock()
	g.mu.Lock()
	active := g.ctx.Err() == nil
	if current && active {
		e.enqueueLatestCallback(latestCallbackKey{generation: g, kind: kind}, fn)
	}
	g.mu.Unlock()
}

func (e *Engine) enqueueLatestCallback(key latestCallbackKey, fn func()) {
	e.callbackQueueMu.Lock()
	if e.latestCallbacks == nil {
		e.latestCallbacks = make(map[latestCallbackKey]func())
		e.latestCallbackQueued = make(map[latestCallbackKey]bool)
	}
	e.latestCallbacks[key] = fn
	if e.latestCallbackQueued[key] {
		e.callbackQueueMu.Unlock()
		return
	}
	if len(e.callbackQueue) >= maxCallbackQueue {
		delete(e.latestCallbacks, key)
		e.callbackQueueMu.Unlock()
		return
	}
	e.latestCallbackQueued[key] = true
	e.callbackQueue = append(e.callbackQueue, func() {
		e.callbackQueueMu.Lock()
		latest := e.latestCallbacks[key]
		delete(e.latestCallbacks, key)
		delete(e.latestCallbackQueued, key)
		e.callbackQueueMu.Unlock()
		if latest != nil {
			latest()
		}
	})
	startRunner := !e.callbackQueueRunning
	if startRunner {
		e.callbackQueueRunning = true
	}
	e.callbackQueueMu.Unlock()
	if startRunner {
		go e.runCallbackQueue()
	}
}

func (e *Engine) enqueueOptionalGenerationCallbackLocked(g *connectionGeneration, fn func()) {
	if g == nil {
		e.invokeCallback(fn)
		return
	}
	e.enqueueGenerationCallbackLocked(g, fn)
}

func (e *Engine) notifyGenerationState(g *connectionGeneration, state State) {
	if callback := e.OnStateChange; callback != nil {
		e.callbackMu.Lock()
		e.enqueueReliableGenerationCallbackLocked(g, func() { callback(state) })
		e.callbackMu.Unlock()
	}
}

func (e *Engine) notifyStateChange(state State) {
	if callback := e.OnStateChange; callback != nil {
		e.callbackMu.Lock()
		e.invokeCallback(func() { callback(state) })
		e.callbackMu.Unlock()
	}
}

func resolveAdvertisedAddr(controlAddr, advertisedAddr, defaultPort string) (string, error) {
	controlHost, _, err := net.SplitHostPort(controlAddr)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(advertisedAddr) == "" {
		if controlHost == "" {
			return "", fmt.Errorf("missing screen address")
		}
		return net.JoinHostPort(controlHost, defaultPort), nil
	}
	host, port, err := net.SplitHostPort(advertisedAddr)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = controlHost
	}
	return net.JoinHostPort(host, port), nil
}
