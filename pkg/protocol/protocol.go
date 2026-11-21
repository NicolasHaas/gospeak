// Package protocol defines the voice packet format and control message framing.
package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

const (
	// VoiceHeaderSize is the byte size of the voice packet header.
	// [sessionID(4) | seqNum(4) | timestamp(4) | channelID(8)] = 20 bytes
	VoiceHeaderSize = 20

	// MaxVoicePayload is the maximum encrypted Opus payload size.
	MaxVoicePayload = 1400

	// MaxControlMessage is the maximum control message size (512KB).
	MaxControlMessage = 512 * 1024

	// FrameDuration is the Opus frame duration in milliseconds.
	FrameDuration = 20

	// SampleRate is the audio sample rate in Hz.
	SampleRate = 48000

	// Channels is the number of audio channels (mono).
	AudioChannels = 1

	// FrameSize is the number of samples per frame (SampleRate * FrameDuration / 1000).
	FrameSize = SampleRate * FrameDuration / 1000 // 960
)

var controlMessageFields = map[string]struct{}{
	"personal_token_status_request": {}, "personal_token_status_response": {},
	"rotate_personal_token_request": {}, "rotate_personal_token_response": {},
	"revoke_personal_token_request": {}, "revoke_personal_token_response": {},
	"issue_personal_recovery_request": {}, "issue_personal_recovery_response": {},
	"revoke_personal_recovery_request": {}, "revoke_personal_recovery_response": {},
	"redeem_personal_recovery_request": {}, "redeem_personal_recovery_response": {},
	"auth_request": {}, "auth_response": {},
	"channel_list_request": {}, "channel_list_response": {},
	"join_channel_request": {}, "channel_join_response": {},
	"leave_channel_request": {}, "channel_joined_event": {}, "channel_left_event": {},
	"user_state_update": {}, "server_state_event": {},
	"create_channel_request": {}, "delete_channel_request": {},
	"create_token_request": {}, "create_token_response": {},
	"list_invites_request": {}, "list_invites_response": {},
	"revoke_invite_request": {}, "revoke_invite_response": {},
	"kick_user_request": {}, "ban_user_request": {},
	"list_bans_request": {}, "list_bans_response": {},
	"unban_request": {}, "unban_response": {},
	"chat_message": {}, "chat_event": {}, "chat_history_request": {}, "chat_history_response": {},
	"chat_delete_request": {}, "chat_delete_event": {},
	"screen_share_start_request": {}, "screen_share_stop_request": {},
	"screen_share_subscribe_request": {}, "screen_share_share_request": {},
	"screen_share_unsubscribe_request": {}, "screen_share_event": {},
	"set_user_role_request": {}, "set_user_role_response": {},
	"export_data_request": {}, "export_data_response": {},
	"import_channels_request": {}, "import_channels_response": {},
	"error_response": {}, "ping": {}, "pong": {},
}

// VoicePacket represents a voice data packet sent over UDP.
type VoicePacket struct {
	SessionID uint32 // 4 bytes: identifies the sender session
	SeqNum    uint32 // 4 bytes: sequence number for ordering (prevents AEAD nonce reuse)
	Timestamp uint32 // 4 bytes: RTP-style timestamp
	ChannelID uint64 // 8 bytes: target channel
	Payload   []byte // encrypted Opus frame + AEAD auth tag
}

// MarshalHeader marshals only the header portion (20 bytes).
func (p *VoicePacket) MarshalHeader() []byte {
	h := make([]byte, VoiceHeaderSize)
	binary.BigEndian.PutUint32(h[0:4], p.SessionID)
	binary.BigEndian.PutUint32(h[4:8], p.SeqNum)
	binary.BigEndian.PutUint32(h[8:12], p.Timestamp)
	binary.BigEndian.PutUint64(h[12:20], p.ChannelID)
	return h
}

// Marshal serializes the entire voice packet to bytes.
func (p *VoicePacket) Marshal() []byte {
	h := p.MarshalHeader()
	buf := make([]byte, VoiceHeaderSize+len(p.Payload))
	copy(buf, h)
	copy(buf[VoiceHeaderSize:], p.Payload)
	return buf
}

// UnmarshalVoicePacket parses a voice packet from raw bytes.
func UnmarshalVoicePacket(data []byte) (*VoicePacket, error) {
	if len(data) < VoiceHeaderSize {
		return nil, errors.New("protocol: packet too short")
	}
	pkt := &VoicePacket{
		SessionID: binary.BigEndian.Uint32(data[0:4]),
		SeqNum:    binary.BigEndian.Uint32(data[4:8]),
		Timestamp: binary.BigEndian.Uint32(data[8:12]),
		ChannelID: binary.BigEndian.Uint64(data[12:20]),
		Payload:   make([]byte, len(data)-VoiceHeaderSize),
	}
	copy(pkt.Payload, data[VoiceHeaderSize:])
	return pkt, nil
}

// WriteControlMessage writes a length-prefixed JSON control message to a writer.
// Format: [4-byte big-endian length][JSON payload]
func WriteControlMessage(w io.Writer, msg *pb.ControlMessage) error {
	if err := validateLiveSessions(msg); err != nil {
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("protocol: marshal: %w", err)
	}
	if err := validateControlEnvelope(data); err != nil {
		return err
	}
	if len(data) > MaxControlMessage {
		return fmt.Errorf("protocol: message too large: %d bytes", len(data))
	}

	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(data))) //nolint:gosec // length already bounds-checked above
	copy(frame[4:], data)

	for len(frame) > 0 {
		n, writeErr := w.Write(frame)
		if n < 0 || n > len(frame) {
			return fmt.Errorf("protocol: invalid write count: %d", n)
		}
		frame = frame[n:]
		if writeErr != nil {
			return fmt.Errorf("protocol: write frame: %w", writeErr)
		}
		if n == 0 {
			return fmt.Errorf("protocol: write frame: %w", io.ErrShortWrite)
		}
	}
	return nil
}

// ReadControlMessage reads a length-prefixed JSON control message from a reader.
func ReadControlMessage(r io.Reader) (*pb.ControlMessage, error) {
	msg, _, err := ReadControlMessageWithSize(r)
	return msg, err
}

// ReadControlMessageWithFrameDeadline preserves the caller's idle deadline until
// the first byte, then allows five seconds to finish the entire frame. Callers
// with a stricter total deadline (such as authentication) use the plain reader.
// The completion deadline is cleared before returning a complete message.
func ReadControlMessageWithFrameDeadline(r io.Reader, setDeadline func(time.Time) error) (*pb.ControlMessage, int, error) {
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		return nil, 0, fmt.Errorf("protocol: read length: %w", err)
	}
	if err := setDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, 0, fmt.Errorf("protocol: set frame deadline: %w", err)
	}
	msg, size, err := ReadControlMessageWithSize(io.MultiReader(bytes.NewReader(first[:]), r))
	if err != nil {
		return nil, size, err
	}
	if err := setDeadline(time.Time{}); err != nil {
		return nil, size, fmt.Errorf("protocol: clear frame deadline: %w", err)
	}
	return msg, size, nil
}

// ReadControlMessageWithSize reads a control message and reports its encoded
// payload size so authenticated callers can budget both semantic and parsing
// work without marshaling the message again.
func ReadControlMessageWithSize(r io.Reader) (*pb.ControlMessage, int, error) {
	// Read length prefix
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return nil, 0, fmt.Errorf("protocol: read length: %w", err)
	}
	length := binary.BigEndian.Uint32(lenBuf)
	if length > MaxControlMessage {
		return nil, MaxControlMessage + 1, fmt.Errorf("protocol: message too large: %d bytes", length)
	}

	// Read payload
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, int(length), fmt.Errorf("protocol: read payload: %w", err)
	}
	if err := validateControlEnvelope(data); err != nil {
		return nil, int(length), err
	}

	msg := &pb.ControlMessage{}
	if err := json.Unmarshal(data, msg); err != nil {
		return nil, int(length), fmt.Errorf("protocol: unmarshal: %w", err)
	}
	if err := validateLiveSessions(msg); err != nil {
		return nil, int(length), err
	}
	return msg, int(length), nil
}

// ValidateLiveSessions checks a complete ID-only snapshot. Nil is unsupported,
// never authority to retire replay history or admit incoming voice.
func ValidateLiveSessions(ids []uint32, channels []pb.ChannelInfo) error {
	if ids == nil {
		return errors.New("protocol: missing live session roster")
	}
	if len(ids) > model.MaxServerSessions {
		return errors.New("protocol: live session roster too large")
	}
	live := make(map[uint32]bool, len(ids))
	for _, id := range ids {
		if id == 0 || id == VoiceRegistrationMagic || live[id] {
			return errors.New("protocol: invalid live session ID")
		}
		live[id] = true
	}
	seen := make(map[uint32]bool)
	for _, channel := range channels {
		for _, user := range channel.Users {
			if !live[user.SessionID] || seen[user.SessionID] {
				return errors.New("protocol: inconsistent live session membership")
			}
			seen[user.SessionID] = true
		}
	}
	return nil
}

func validateLiveSessions(msg *pb.ControlMessage) error {
	if msg == nil {
		return errors.New("protocol: invalid control envelope: expected JSON object")
	}
	// Nested fields of existing allowlisted envelopes, not new message types.
	if msg.AuthResponse != nil && msg.AuthResponse.LiveSessionIDs != nil {
		return ValidateLiveSessions(msg.AuthResponse.LiveSessionIDs, msg.AuthResponse.Channels)
	}
	if msg.ServerStateEvent != nil && msg.ServerStateEvent.LiveSessionIDs != nil {
		return ValidateLiveSessions(msg.ServerStateEvent.LiveSessionIDs, msg.ServerStateEvent.Channels)
	}
	return nil
}

func validateControlEnvelope(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	token, err := dec.Token()
	if err != nil {
		return fmt.Errorf("protocol: invalid control envelope: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("protocol: invalid control envelope: expected JSON object")
	}

	fields := 0
	seen := make(map[string]struct{})
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return fmt.Errorf("protocol: invalid control envelope: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("protocol: invalid control envelope: expected string key")
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("protocol: invalid control envelope: duplicate field %q", key)
		}
		seen[key] = struct{}{}
		if _, ok := controlMessageFields[key]; !ok {
			return fmt.Errorf("protocol: invalid control envelope: unknown field %q", key)
		}
		fields++
		value, err := dec.Token()
		if err != nil {
			return fmt.Errorf("protocol: invalid control envelope: field %q: %w", key, err)
		}
		if value == nil {
			return fmt.Errorf("protocol: invalid control envelope: field %q is null", key)
		}
		switch v := value.(type) {
		case json.Delim:
			if v == '{' || v == '[' {
				if err := skipJSONContainer(dec); err != nil {
					return fmt.Errorf("protocol: invalid control envelope: field %q: %w", key, err)
				}
			}
		}
	}
	if fields != 1 {
		return fmt.Errorf("protocol: invalid control envelope: got %d fields, want exactly one", fields)
	}
	closing, err := dec.Token()
	if err != nil {
		return fmt.Errorf("protocol: invalid control envelope: %w", err)
	}
	if delim, ok := closing.(json.Delim); !ok || delim != '}' {
		return fmt.Errorf("protocol: invalid control envelope: expected closing object delimiter")
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("protocol: invalid control envelope: unexpected data after object")
	}
	return nil
}

// maxControlJSONDepth includes the envelope object.
const maxControlJSONDepth = 32

// skipJSONContainer consumes an opened payload container without recursion.
func skipJSONContainer(dec *json.Decoder) error {
	depth := 2 // envelope and payload
	for depth > 1 {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
				if depth > maxControlJSONDepth {
					return errors.New("control JSON nesting too deep")
				}
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}
