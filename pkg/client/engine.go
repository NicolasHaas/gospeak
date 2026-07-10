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
)

const (
	defaultScreenShareCaptureTimeout = 15 * time.Second
	defaultScreenShareStartTimeout   = 10 * time.Second
	keepAliveInterval                = 5 * time.Second
)

// State represents the client's connection state.
type State int

const (
	StateDisconnected State = iota
	StateConnecting
	StateConnected
)

// Engine is the main client engine that wires together audio, networking, and state.
type Engine struct {
	mu sync.RWMutex

	state     State
	sessionID uint32
	username  string
	role      string
	channelID int64
	muted     bool
	deafened  bool

	control *ControlClient
	voice   *VoiceClient
	screen  *ScreenClient
	cipher  *gospeakCrypto.VoiceCipher

	capture  audio.Capturer
	playback audio.Player
	encoder  audio.AudioEncoder
	vad      audio.VoiceDetector

	// Per-speaker decoders and jitter buffers
	decoders       map[uint32]audio.AudioDecoder
	jitterBufs     map[uint32]*JitterBuffer
	decoderMu      sync.Mutex
	decoderFactory audio.DecoderFactory

	channels []pb.ChannelInfo

	ctx    context.Context
	cancel context.CancelFunc

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
	initAudioFn func() error

	// Voice debug counters (reset each log interval)
	voiceDebugMu        sync.Mutex
	voiceDebugSent      int64
	voiceDebugKeepalive int64
	voiceDebugRecv      int64
	voiceDebugSpeakers  map[uint32]struct{}

	// Keep-alive state
	lastSendTime time.Time
	silenceBuf   []int16

	// Callbacks for UI updates
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
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		state:              StateDisconnected,
		decoders:           make(map[uint32]audio.AudioDecoder),
		jitterBufs:         make(map[uint32]*JitterBuffer),
		voiceDebugSpeakers: make(map[uint32]struct{}),
		ctx:                ctx,
		cancel:             cancel,
		vad:                audio.NewVAD(200, 15, 3), // threshold=200, hold=300ms, prebuf=60ms
		decoderFactory:     &defaultDecoderFactory{},
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
func (e *Engine) Connect(controlAddr, voiceAddr, token, username string) error {
	e.mu.Lock()
	if e.state != StateDisconnected {
		e.mu.Unlock()
		return fmt.Errorf("already connected")
	}
	e.state = StateConnecting
	e.mu.Unlock()

	e.notifyStateChange(StateConnecting)

	// Connect control plane
	ctrl, err := NewControlClient(controlAddr)
	if err != nil {
		e.setState(StateDisconnected)
		return err
	}

	// Authenticate
	authResp, err := ctrl.Authenticate(token, username)
	if err != nil {
		_ = ctrl.Close()
		e.setState(StateDisconnected)
		return err
	}

	slog.Info("authenticated",
		"session", authResp.SessionID,
		"user", authResp.Username,
		"role", authResp.Role,
	)

	// Set up voice connection
	voice, err := NewVoiceClient(voiceAddr, authResp.SessionID, authResp.EncryptionKey)
	if err != nil {
		_ = ctrl.Close()
		e.setState(StateDisconnected)
		return err
	}

	cipher, err := gospeakCrypto.NewVoiceCipher(authResp.EncryptionKey)
	if err != nil {
		_ = ctrl.Close()
		_ = voice.Close()
		e.setState(StateDisconnected)
		return err
	}

	var screen *ScreenClient
	if authResp.ScreenShareEnabled {
		screenAddr, err := resolveAdvertisedAddr(controlAddr, authResp.ScreenAddr, "9603")
		if err != nil {
			_ = ctrl.Close()
			_ = voice.Close()
			e.setState(StateDisconnected)
			return fmt.Errorf("resolve screen address: %w", err)
		} else {
			screen, err = NewScreenClient(screenAddr, authResp.SessionID, authResp.ScreenAuthToken)
			if err != nil {
				_ = ctrl.Close()
				_ = voice.Close()
				e.setState(StateDisconnected)
				return fmt.Errorf("connect screen plane: %w", err)
			}
		}
	}

	e.mu.Lock()
	e.control = ctrl
	e.voice = voice
	e.screen = screen
	e.cipher = cipher
	e.sessionID = authResp.SessionID
	e.username = authResp.Username
	e.role = authResp.Role
	e.channels = authResp.Channels
	e.screenShareEnabled = authResp.ScreenShareEnabled
	e.state = StateConnected
	e.mu.Unlock()

	// Set up event handling
	ctrl.SetEventHandler(e.handleEvent)
	ctrl.StartReceiving()
	voice.StartReceiving()
	if screen != nil {
		screen.SetPacketHandler(e.handleScreenPacket)
		screen.StartReceiving()
		go func() {
			<-screen.Done()
			e.handleScreenDisconnect()
		}()
	}

	// Report connected immediately — audio init happens in background
	e.notifyStateChange(StateConnected)
	if e.OnChannelsUpdate != nil {
		e.OnChannelsUpdate(authResp.Channels)
	}

	// Auto-join the first available channel so the user isn't stuck in "no channel"
	if len(authResp.Channels) > 0 {
		firstCh := authResp.Channels[0]
		if err := e.JoinChannel(firstCh.ID); err != nil {
			slog.Warn("auto-join channel failed", "channel", firstCh.Name, "err", err)
		}
	}

	// Notify if server auto-generated a token for this user
	if authResp.AutoToken != "" && e.OnAutoToken != nil {
		e.OnAutoToken(authResp.AutoToken)
	}

	// Start periodic voice debug logging and keepalive loop
	e.startVoiceDebugLogging()
	go e.keepaliveLoop()

	// Initialize audio devices asynchronously (PortAudio init is slow on Windows)
	go func() {
		if err := e.initAudioFn(); err != nil {
			slog.Error("audio init failed (continuing without audio)", "err", err)
		}
		// Start audio pipelines
		go e.captureLoop()
		go e.playbackLoop()
	}()

	// Monitor for disconnect
	go func() {
		<-ctrl.Done()
		e.handleDisconnect("connection lost")
	}()

	return nil
}

// initAudioDefault initializes PortAudio devices and Opus codec (the default backend).
func (e *Engine) initAudioDefault() error {
	capture, err := audio.NewCaptureDevice(48000, 960)
	if err != nil {
		return fmt.Errorf("capture device: %w", err)
	}
	if err := capture.Start(); err != nil {
		return fmt.Errorf("start capture: %w", err)
	}

	playback, err := audio.NewPlaybackDevice(48000, 960)
	if err != nil {
		_ = capture.Close()
		return fmt.Errorf("playback device: %w", err)
	}
	if err := playback.Start(); err != nil {
		_ = capture.Close()
		return fmt.Errorf("start playback: %w", err)
	}

	encoder, err := audio.NewEncoder()
	if err != nil {
		_ = playback.Stop()
		_ = capture.Close()
		return fmt.Errorf("encoder: %w", err)
	}

	e.mu.Lock()
	e.capture = capture
	e.playback = playback
	e.encoder = encoder
	e.lastSendTime = time.Now()
	e.silenceBuf = make([]int16, 960)
	e.mu.Unlock()

	return nil
}

// captureLoop reads audio from the mic, runs VAD, encodes, and sends.
func (e *Engine) captureLoop() {
	var timestamp uint32

	for {
		select {
		case <-e.ctx.Done():
			return
		default:
		}

		e.mu.RLock()
		capture := e.capture
		encoder := e.encoder
		voice := e.voice
		muted := e.muted
		channelID := e.channelID
		e.mu.RUnlock()
		if capture == nil || encoder == nil || voice == nil {
			return
		}

		pcm, err := capture.ReadFrame()
		if err != nil {
			slog.Debug("capture read error", "err", err)
			return
		}

		// Compute RMS for VU meter
		rms := audio.GetRMS(pcm)
		if e.OnRMSLevel != nil {
			e.OnRMSLevel(rms)
		}

		// VAD
		active := e.vad.Process(pcm)
		if e.OnVoiceActivity != nil {
			e.OnVoiceActivity(active)
		}

		// Skip sending when VAD is idle, muted, or not in a channel
		if !active || muted || channelID == 0 {
			timestamp += 960
			continue
		}

		opusData, err := encoder.Encode(pcm)
		if err != nil {
			slog.Debug("encode error", "err", err)
			timestamp += 960
			continue
		}

		if err := voice.SendVoice(opusData, timestamp); err != nil {
			slog.Debug("voice send error", "err", err)
		} else {
			e.voiceDebugMu.Lock()
			e.voiceDebugSent++
			e.voiceDebugMu.Unlock()
		}
		e.lastSendTime = time.Now()

		timestamp += 960
	}
}

// playbackLoop receives voice packets, decodes, and plays them.
func (e *Engine) playbackLoop() {
	for {
		select {
		case <-e.ctx.Done():
			return
		default:
		}

		e.mu.RLock()
		voice := e.voice
		playback := e.playback
		deafened := e.deafened
		e.mu.RUnlock()

		if voice == nil || playback == nil {
			return
		}

		select {
		case pkt := <-voice.IncomingPackets:
			if deafened {
				continue
			}
			e.processIncomingVoice(pkt, playback)
		case <-e.ctx.Done():
			return
		}
	}
}

// processIncomingVoice decrypts and plays a received voice packet.
func (e *Engine) processIncomingVoice(pkt *protocol.VoicePacket, playback audio.Player) {
	// Get or create decoder for this speaker
	e.decoderMu.Lock()
	dec, ok := e.decoders[pkt.SessionID]
	if !ok {
		var err error
		dec, err = e.decoderFactory.NewDecoder()
		if err != nil {
			e.decoderMu.Unlock()
			slog.Error("create decoder failed", "err", err)
			return
		}
		e.decoders[pkt.SessionID] = dec
		e.jitterBufs[pkt.SessionID] = NewJitterBuffer()
	}
	jb := e.jitterBufs[pkt.SessionID]
	e.decoderMu.Unlock()

	// Decrypt the voice data
	header := pkt.MarshalHeader()
	opusData, err := e.cipher.Decrypt(pkt.SessionID, pkt.SeqNum, header, pkt.Payload)
	if err != nil {
		slog.Debug("voice decrypt failed", "session", pkt.SessionID, "err", err)
		return
	}

	// Track received packet for debug logging
	e.voiceDebugMu.Lock()
	e.voiceDebugRecv++
	e.voiceDebugSpeakers[pkt.SessionID] = struct{}{}
	e.voiceDebugMu.Unlock()

	// Push to jitter buffer
	jb.Push(pkt.SeqNum, opusData)

	// Pop and play
	for {
		data, _, ok := jb.Pop()
		if !ok {
			break
		}

		var pcm []int16
		if data == nil {
			// Packet lost — use PLC
			pcm, err = dec.DecodePLC()
		} else {
			pcm, err = dec.Decode(data)
		}
		if err != nil {
			slog.Debug("decode error", "err", err)
			continue
		}

		if err := playback.WriteFrame(pcm); err != nil {
			slog.Debug("playback error", "err", err)
		}
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
		hints = append(hints, "no packets sent — check mic or not in a channel")
	case sentVoice == 0 && sentKeepalive > 0:
		hints = append(hints, "only keepalive packets — VAD may need adjustment")
	default:
		hints = append(hints, "sending audio")
	}
	switch {
	case deafened:
		hints = append(hints, "deafened")
	case recv == 0 && speakerCount == 0:
		hints = append(hints, "no audio received — check speaker or deafen status")
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
func (e *Engine) startVoiceDebugLogging() {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-ticker.C:
				e.logVoiceDebug()
			}
		}
	}()
}

// keepaliveLoop sends silence packets every keepAliveInterval to keep the
// server aware of our UDP address. It is independent of VAD state so that
// the connection is maintained during silence.
func (e *Engine) keepaliveLoop() {
	var timestamp uint32

	ticker := time.NewTicker(keepAliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.mu.RLock()
			voice := e.voice
			encoder := e.encoder
			channelID := e.channelID
			lastSend := e.lastSendTime
			silenceBuf := e.silenceBuf
			e.mu.RUnlock()

			if voice == nil || encoder == nil || channelID == 0 || silenceBuf == nil {
				continue
			}

			// Skip if a real voice or keepalive packet was sent recently
			if time.Since(lastSend) < keepAliveInterval {
				continue
			}

			silenceData, err := encoder.Encode(silenceBuf)
			if err != nil {
				slog.Debug("keepalive encode error", "err", err)
				continue
			}

			if err := voice.SendVoice(silenceData, timestamp); err != nil {
				slog.Debug("keepalive send error", "err", err)
				continue
			}
			timestamp += 960

			e.voiceDebugMu.Lock()
			e.voiceDebugKeepalive++
			e.voiceDebugMu.Unlock()

			e.mu.Lock()
			e.lastSendTime = time.Now()
			e.mu.Unlock()
		}
	}
}

// handleEvent dispatches incoming server events.
func (e *Engine) handleEvent(msg *pb.ControlMessage) {
	switch {
	case msg.ServerStateEvent != nil:
		e.mu.Lock()
		e.channels = msg.ServerStateEvent.Channels
		e.screenShareEnabled = msg.ServerStateEvent.ScreenShareEnabled
		e.mu.Unlock()
		if e.OnChannelsUpdate != nil {
			e.OnChannelsUpdate(msg.ServerStateEvent.Channels)
		}

	case msg.ChannelJoinedEvent != nil:
		// Refresh will come via ServerStateEvent
		slog.Info("user joined channel",
			"user", msg.ChannelJoinedEvent.User.Username,
			"channel", msg.ChannelJoinedEvent.ChannelID,
		)

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
			e.handleDisconnect(msg.ErrorResponse.Message)
		}
		if e.OnError != nil {
			e.OnError(fmt.Errorf("server: %s", msg.ErrorResponse.Message))
		}

	case msg.Pong != nil:
		// Ping/pong handled silently

	case msg.CreateTokenResp != nil:
		slog.Debug("token created", "token", msg.CreateTokenResp.Token)
		if e.OnTokenCreated != nil {
			e.OnTokenCreated(msg.CreateTokenResp.Token)
		}

	case msg.ChatEvent != nil:
		if e.OnChatMessage != nil {
			e.OnChatMessage(msg.ChatEvent.ChannelID, msg.ChatEvent.SenderName, msg.ChatEvent.Text, msg.ChatEvent.Timestamp)
		}

	case msg.ScreenShareEvent != nil:
		e.handleScreenShareEvent(msg.ScreenShareEvent)

	case msg.SetUserRoleResp != nil:
		if e.OnRoleChanged != nil {
			e.OnRoleChanged(msg.SetUserRoleResp.Success, msg.SetUserRoleResp.Message)
		}

	case msg.ExportDataResp != nil:
		if e.OnExportData != nil {
			e.OnExportData(msg.ExportDataResp.Type, msg.ExportDataResp.Data)
		}

	case msg.ImportChannelsResp != nil:
		if e.OnImportResult != nil {
			e.OnImportResult(msg.ImportChannelsResp.Success, msg.ImportChannelsResp.Message)
		}
	}
}

// JoinChannel sends a request to join a channel.
func (e *Engine) JoinChannel(channelID int64) error {
	e.mu.RLock()
	ctrl := e.control
	voice := e.voice
	e.mu.RUnlock()

	if ctrl == nil {
		return fmt.Errorf("not connected")
	}

	if err := ctrl.Send(&pb.ControlMessage{
		JoinChannelRequest: &pb.JoinChannelRequest{ChannelID: channelID},
	}); err != nil {
		return err
	}

	e.mu.Lock()
	e.channelID = channelID
	e.mu.Unlock()
	e.clearScreenShareState()

	if voice != nil {
		voice.SetChannel(channelID)
	}

	return nil
}

// LeaveChannel sends a request to leave the current channel.
func (e *Engine) LeaveChannel() error {
	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()

	if ctrl == nil {
		return fmt.Errorf("not connected")
	}

	if err := ctrl.Send(&pb.ControlMessage{
		LeaveChannelRequest: &pb.LeaveChannelRequest{},
	}); err != nil {
		return err
	}

	e.mu.Lock()
	e.channelID = 0
	e.mu.Unlock()
	e.clearScreenShareState()

	return nil
}

// SetMuted toggles mute state.
func (e *Engine) SetMuted(muted bool) {
	e.mu.Lock()
	e.muted = muted
	deafened := e.deafened
	ctrl := e.control
	e.mu.Unlock()

	if ctrl != nil {
		_ = ctrl.Send(&pb.ControlMessage{
			UserStateUpdate: &pb.UserStateUpdate{Muted: muted, Deafened: deafened},
		})
	}
}

// SetDeafened toggles deafen state.
func (e *Engine) SetDeafened(deafened bool) {
	e.mu.Lock()
	wasDeafened := e.deafened
	e.deafened = deafened
	muted := e.muted
	ctrl := e.control
	e.mu.Unlock()

	if wasDeafened && !deafened {
		e.resetReceiveState()
	}

	if ctrl != nil {
		_ = ctrl.Send(&pb.ControlMessage{
			UserStateUpdate: &pb.UserStateUpdate{Muted: muted, Deafened: deafened},
		})
	}
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
	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()

	if ctrl == nil {
		return fmt.Errorf("not connected")
	}

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
	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()

	if ctrl == nil {
		return fmt.Errorf("not connected")
	}

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
	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()

	if ctrl == nil {
		return fmt.Errorf("not connected")
	}

	return ctrl.Send(&pb.ControlMessage{
		DeleteChannelReq: &pb.DeleteChannelRequest{ChannelID: channelID},
	})
}

// ExportData requests the server to export data ("channels" or "users") as YAML.
func (e *Engine) ExportData(dataType string) error {
	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()

	if ctrl == nil {
		return fmt.Errorf("not connected")
	}

	return ctrl.Send(&pb.ControlMessage{
		ExportDataReq: &pb.ExportDataRequest{Type: dataType},
	})
}

// ImportChannels sends a YAML blob for the server to import as channels.
func (e *Engine) ImportChannels(yamlData string) error {
	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()

	if ctrl == nil {
		return fmt.Errorf("not connected")
	}

	return ctrl.Send(&pb.ControlMessage{
		ImportChannelsReq: &pb.ImportChannelsRequest{YAML: yamlData},
	})
}

// CreateToken sends a create token request (admin only).
func (e *Engine) CreateToken(role string, maxUses int, expiresInSeconds int64) error {
	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()

	if ctrl == nil {
		return fmt.Errorf("not connected")
	}

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
	e.mu.RLock()
	ctrl := e.control
	channelID := e.channelID
	e.mu.RUnlock()

	if ctrl == nil {
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
	e.mu.RLock()
	ctrl := e.control
	channelID := e.channelID
	mySessionID := e.sessionID
	active := e.activeScreenShare
	e.mu.RUnlock()

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

	_, width, height, err := e.prepareScreenShareFrame(displayIndex)
	if err != nil {
		return fmt.Errorf("prepare screen share: %w", err)
	}

	e.screenMu.Lock()
	e.screenSharePending = true
	e.screenShareDisplay = displayIndex
	e.screenShareAttempt++
	attempt := e.screenShareAttempt
	startWait := e.screenStartWait
	e.screenMu.Unlock()

	if err := ctrl.Send(&pb.ControlMessage{
		ScreenShareStartReq: &pb.ScreenShareStartRequest{
			DisplayIndex: int32(displayIndex),
			Width:        width,
			Height:       height,
		},
	}); err != nil {
		e.screenMu.Lock()
		e.screenSharePending = false
		e.screenMu.Unlock()
		return err
	}
	go e.watchScreenShareStart(attempt, startWait)

	return nil
}

func (e *Engine) StopScreenShare() error {
	e.stopScreenShareLoop()
	e.screenMu.Lock()
	e.screenSharePending = false
	e.screenMu.Unlock()

	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()
	if ctrl == nil {
		return fmt.Errorf("not connected")
	}
	return ctrl.Send(&pb.ControlMessage{ScreenShareStopReq: &pb.ScreenShareStopRequest{}})
}

func (e *Engine) SubscribeScreenShare(channelID int64) error {
	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()
	if ctrl == nil {
		return fmt.Errorf("not connected")
	}
	return ctrl.Send(&pb.ControlMessage{ScreenShareSubReq: &pb.ScreenShareSubscribeRequest{ChannelID: channelID}})
}

func (e *Engine) ShareScreenShareWithChannel() error {
	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()
	if ctrl == nil {
		return fmt.Errorf("not connected")
	}
	return ctrl.Send(&pb.ControlMessage{ScreenShareShareReq: &pb.ScreenShareShareRequest{}})
}

func (e *Engine) UnsubscribeScreenShare() error {
	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()
	if ctrl == nil {
		return fmt.Errorf("not connected")
	}
	return ctrl.Send(&pb.ControlMessage{ScreenShareUnsubReq: &pb.ScreenShareUnsubscribeRequest{}})
}

// SetUserRole sends a role change request (admin only).
func (e *Engine) SetUserRole(targetUserID int64, newRole string) error {
	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()

	if ctrl == nil {
		return fmt.Errorf("not connected")
	}

	return ctrl.Send(&pb.ControlMessage{
		SetUserRoleReq: &pb.SetUserRoleRequest{
			TargetUserID: targetUserID,
			NewRole:      newRole,
		},
	})
}

// KickUser sends a kick request (admin/mod only).
func (e *Engine) KickUser(userID int64, reason string) error {
	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()

	if ctrl == nil {
		return fmt.Errorf("not connected")
	}

	return ctrl.Send(&pb.ControlMessage{
		KickUserReq: &pb.KickUserRequest{UserID: userID, Reason: reason},
	})
}

// BanUser sends a ban request (admin only).
func (e *Engine) BanUser(userID int64, reason string, durationSeconds int64) error {
	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()

	if ctrl == nil {
		return fmt.Errorf("not connected")
	}

	return ctrl.Send(&pb.ControlMessage{
		BanUserReq: &pb.BanUserRequest{UserID: userID, Reason: reason, DurationSeconds: durationSeconds},
	})
}

// Disconnect disconnects from the server.
func (e *Engine) Disconnect() {
	e.handleDisconnect("user disconnected")
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
	e.decoderMu.Unlock()
}

func (e *Engine) handleDisconnect(reason string) {
	e.mu.Lock()
	if e.state == StateDisconnected {
		e.mu.Unlock()
		return
	}
	e.state = StateDisconnected
	e.channelID = 0
	e.muted = false
	e.deafened = false

	ctrl := e.control
	voice := e.voice
	capture := e.capture
	playback := e.playback
	screen := e.screen

	e.control = nil
	e.voice = nil
	e.screen = nil
	e.capture = nil
	e.playback = nil
	e.activeScreenShare = nil
	e.screenShareEnabled = false
	e.mu.Unlock()
	e.stopScreenShareLoop()
	e.screenMu.Lock()
	e.screenCipher = nil
	e.screenSeqNum = 0
	e.screenMu.Unlock()

	// Clean up resources
	if playback != nil {
		_ = playback.Stop()
	}
	if capture != nil {
		_ = capture.Close()
	}
	if voice != nil {
		_ = voice.Close()
	}
	if screen != nil {
		_ = screen.Close()
	}
	if ctrl != nil {
		_ = ctrl.Close()
	}

	e.cancel()
	// Reset context for reconnection
	e.ctx, e.cancel = context.WithCancel(context.Background())

	e.resetReceiveState()

	slog.Info("disconnected", "reason", reason)
	e.notifyStateChange(StateDisconnected)
	if e.OnDisconnect != nil {
		e.OnDisconnect(reason)
	}
}

func (e *Engine) handleScreenShareEvent(event *pb.ScreenShareEvent) {
	if event == nil {
		e.clearScreenShareState()
		return
	}

	e.mu.Lock()
	e.activeScreenShare = event
	mySessionID := e.sessionID
	channelID := e.channelID
	e.mu.Unlock()

	if event.SessionID == mySessionID {
		if event.Active {
			e.screenMu.Lock()
			pending := e.screenSharePending
			e.screenMu.Unlock()
			if pending {
				e.startScreenShareLoop()
			}
		} else {
			e.stopScreenShareLoop()
			e.screenMu.Lock()
			e.screenSharePending = false
			e.screenMu.Unlock()
		}
	}

	e.screenMu.Lock()
	if !event.Active {
		if e.activeScreenShare == nil || e.activeScreenShare.SessionID == event.SessionID {
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
		e.screenCipher = cipher
		e.screenSeqNum = 0
	} else if e.activeScreenShare == nil || e.activeScreenShare.SessionID != event.SessionID {
		e.screenCipher = nil
		e.screenSeqNum = 0
	}
	e.screenMu.Unlock()

	if !event.Active && e.OnScreenFrame != nil {
		e.OnScreenFrame(nil)
	}
	if event.ChannelID != 0 && channelID != 0 && event.ChannelID != channelID {
		return
	}
	if e.OnScreenShareEvent != nil {
		e.OnScreenShareEvent(event)
	}
}

func (e *Engine) clearScreenShareState() {
	e.mu.Lock()
	e.activeScreenShare = nil
	e.mu.Unlock()
	e.stopScreenShareLoop()
	e.screenMu.Lock()
	e.screenCipher = nil
	e.screenSeqNum = 0
	e.screenMu.Unlock()
	if e.OnScreenFrame != nil {
		e.OnScreenFrame(nil)
	}
	if e.OnScreenShareEvent != nil {
		e.OnScreenShareEvent(nil)
	}
}

func (e *Engine) handleScreenPacket(pkt *protocol.ScreenPacket) {
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
	if e.OnScreenFrame != nil {
		e.OnScreenFrame(img)
	}
}

func (e *Engine) startScreenShareLoop() {
	e.screenMu.Lock()
	if e.screenShareRunning {
		e.screenSharePending = false
		e.screenMu.Unlock()
		return
	}
	displayIndex := e.screenShareDisplay
	ctx, cancel := context.WithCancel(e.ctx)
	e.screenShareCancel = cancel
	e.screenShareRunning = true
	e.screenSharePending = false
	e.screenMu.Unlock()

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		defer e.stopScreenShareLoop()

		for {
			if err := e.sendScreenShareFrame(displayIndex); err != nil {
				if errors.Is(err, errScreenShareCaptureTimedOut) {
					e.requestScreenShareStop()
					if e.OnError != nil {
						e.OnError(fmt.Errorf("screen share stopped: %w", err))
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
	}()
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

func (e *Engine) handleScreenDisconnect() {
	e.mu.Lock()
	if e.state == StateDisconnected || e.screen == nil {
		e.mu.Unlock()
		return
	}
	e.screen = nil
	e.screenShareEnabled = false
	e.activeScreenShare = nil
	e.mu.Unlock()

	e.stopScreenShareLoop()
	e.screenMu.Lock()
	e.screenCipher = nil
	e.screenSeqNum = 0
	e.screenMu.Unlock()

	if e.OnScreenFrame != nil {
		e.OnScreenFrame(nil)
	}
	if e.OnScreenShareEvent != nil {
		e.OnScreenShareEvent(&pb.ScreenShareEvent{Active: false})
	}
	if e.OnChannelsUpdate != nil {
		e.OnChannelsUpdate(e.GetChannels())
	}
	if e.OnError != nil {
		e.OnError(fmt.Errorf("screen share connection lost"))
	}
}

func (e *Engine) sendScreenShareFrame(displayIndex int) error {
	e.mu.RLock()
	screen := e.screen
	sessionID := e.sessionID
	e.mu.RUnlock()

	if screen == nil {
		return fmt.Errorf("not connected")
	}
	data, width, height, err := e.prepareScreenShareFrame(displayIndex)
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

func (e *Engine) prepareScreenShareFrame(displayIndex int) ([]byte, int32, int32, error) {
	timeout := e.screenCaptureWait
	if timeout <= 0 {
		timeout = defaultScreenShareCaptureTimeout
	}

	resultCh := make(chan screenSharePrepareResult, 1)
	go func() {
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
	case <-time.After(timeout):
		return nil, 0, 0, errScreenShareCaptureTimedOut
	case <-e.ctx.Done():
		return nil, 0, 0, fmt.Errorf("not connected")
	}
}

func (e *Engine) watchScreenShareStart(attempt uint64, timeout time.Duration) {
	if timeout <= 0 {
		timeout = defaultScreenShareStartTimeout
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-e.ctx.Done():
		return
	}

	e.screenMu.Lock()
	if !e.screenSharePending || e.screenShareRunning || e.screenShareAttempt != attempt {
		e.screenMu.Unlock()
		return
	}
	e.screenSharePending = false
	e.screenMu.Unlock()

	e.requestScreenShareStop()
	if e.OnError != nil {
		e.OnError(errScreenShareStartTimedOut)
	}
}

func (e *Engine) requestScreenShareStop() {
	e.mu.RLock()
	ctrl := e.control
	e.mu.RUnlock()
	if ctrl == nil {
		return
	}
	if err := ctrl.Send(&pb.ControlMessage{ScreenShareStopReq: &pb.ScreenShareStopRequest{}}); err != nil {
		slog.Debug("screen share stop request failed", "err", err)
	}
}

func (e *Engine) setState(state State) {
	e.mu.Lock()
	e.state = state
	e.mu.Unlock()
	e.notifyStateChange(state)
}

func (e *Engine) notifyStateChange(state State) {
	if e.OnStateChange != nil {
		e.OnStateChange(state)
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
