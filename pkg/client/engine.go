package client

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/NicolasHaas/gospeak/pkg/audio"
	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
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

	// Audio initialization function (allows platform-specific audio backends)
	initAudioFn func() error

	// Callbacks for UI updates
	OnStateChange    func(state State)
	OnChannelsUpdate func(channels []pb.ChannelInfo)
	OnError          func(err error)
	OnVoiceActivity  func(active bool)
	OnRMSLevel       func(level float64)
	OnDisconnect     func(reason string)
	OnChatMessage    func(channelID int64, sender, text string, ts int64)
	OnTokenCreated   func(token string)
	OnRoleChanged    func(success bool, message string)
	OnAutoToken      func(token string) // called when server auto-generates a token for this user
	OnExportData     func(dataType, data string)
	OnImportResult   func(success bool, message string)
}

// NewEngine creates a new client engine.
func NewEngine() *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		state:          StateDisconnected,
		decoders:       make(map[uint32]audio.AudioDecoder),
		jitterBufs:     make(map[uint32]*JitterBuffer),
		ctx:            ctx,
		cancel:         cancel,
		vad:            audio.NewVAD(200, 15, 3), // threshold=200, hold=300ms, prebuf=60ms
		decoderFactory: &defaultDecoderFactory{},
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
	voice, err := NewVoiceClient(voiceAddr, authResp.SessionID, authResp.Encryption)
	if err != nil {
		_ = ctrl.Close()
		e.setState(StateDisconnected)
		return err
	}

	cipher, err := gospeakCrypto.NewVoiceCipher(authResp.Encryption)
	if err != nil {
		_ = ctrl.Close()
		_ = voice.Close()
		e.setState(StateDisconnected)
		return err
	}

	e.mu.Lock()
	e.control = ctrl
	e.voice = voice
	e.cipher = cipher
	e.sessionID = authResp.SessionID
	e.username = authResp.Username
	e.role = authResp.Role
	e.channels = authResp.Channels
	e.state = StateConnected
	e.mu.Unlock()

	// Set up event handling
	ctrl.SetEventHandler(e.handleEvent)
	ctrl.StartReceiving()
	voice.StartReceiving()

	// Report connected immediately — audio init happens in background
	e.notifyStateChange(StateConnected)
	if e.OnChannelsUpdate != nil {
		e.OnChannelsUpdate(authResp.Channels)
	}

	// Notify if server auto-generated a token for this user
	if authResp.AutoToken != "" && e.OnAutoToken != nil {
		e.OnAutoToken(authResp.AutoToken)
	}

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
		_ = capture.Close()
		_ = playback.Stop()
		return fmt.Errorf("encoder: %w", err)
	}

	e.mu.Lock()
	e.capture = capture
	e.playback = playback
	e.encoder = encoder
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

		// Only send if VAD active, not muted, and in a channel
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
		}

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

// handleEvent dispatches incoming server events.
func (e *Engine) handleEvent(msg *pb.ControlMessage) {
	switch {
	case msg.ServerStateEvent != nil:
		e.mu.Lock()
		e.channels = msg.ServerStateEvent.Channels
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
		slog.Info("token created", "token", msg.CreateTokenResp.Token)
		if e.OnTokenCreated != nil {
			e.OnTokenCreated(msg.CreateTokenResp.Token)
		}

	case msg.ChatEvent != nil:
		if e.OnChatMessage != nil {
			e.OnChatMessage(msg.ChatEvent.ChannelID, msg.ChatEvent.SenderName, msg.ChatEvent.Text, msg.ChatEvent.Timestamp)
		}

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

	return nil
}

// SetMuted toggles mute state.
func (e *Engine) SetMuted(muted bool) {
	e.mu.Lock()
	e.muted = muted
	ctrl := e.control
	e.mu.Unlock()

	if ctrl != nil {
		_ = ctrl.Send(&pb.ControlMessage{
			UserStateUpdate: &pb.UserStateUpdate{Muted: muted, Deafened: e.deafened},
		})
	}
}

// SetDeafened toggles deafen state.
func (e *Engine) SetDeafened(deafened bool) {
	e.mu.Lock()
	e.deafened = deafened
	ctrl := e.control
	e.mu.Unlock()

	if ctrl != nil {
		_ = ctrl.Send(&pb.ControlMessage{
			UserStateUpdate: &pb.UserStateUpdate{Muted: e.muted, Deafened: deafened},
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

func (e *Engine) handleDisconnect(reason string) {
	e.mu.Lock()
	if e.state == StateDisconnected {
		e.mu.Unlock()
		return
	}
	e.state = StateDisconnected
	e.channelID = 0

	ctrl := e.control
	voice := e.voice
	capture := e.capture
	playback := e.playback

	e.control = nil
	e.voice = nil
	e.capture = nil
	e.playback = nil
	e.mu.Unlock()

	// Clean up resources
	if capture != nil {
		_ = capture.Close()
	}
	if playback != nil {
		_ = playback.Stop()
	}
	if voice != nil {
		_ = voice.Close()
	}
	if ctrl != nil {
		_ = ctrl.Close()
	}

	e.cancel()
	// Reset context for reconnection
	e.ctx, e.cancel = context.WithCancel(context.Background())

	// Clean up decoders
	e.decoderMu.Lock()
	e.decoders = make(map[uint32]audio.AudioDecoder)
	e.jitterBufs = make(map[uint32]*JitterBuffer)
	e.decoderMu.Unlock()

	slog.Info("disconnected", "reason", reason)
	e.notifyStateChange(StateDisconnected)
	if e.OnDisconnect != nil {
		e.OnDisconnect(reason)
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
