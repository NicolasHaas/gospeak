// Package protocol defines the voice packet format and control message framing.
package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

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
	"auth_request": {}, "auth_response": {},
	"channel_list_request": {}, "channel_list_response": {},
	"join_channel_request": {}, "channel_join_response": {},
	"leave_channel_request": {}, "channel_joined_event": {}, "channel_left_event": {},
	"user_state_update": {}, "server_state_event": {},
	"create_channel_request": {}, "delete_channel_request": {},
	"create_token_request": {}, "create_token_response": {},
	"kick_user_request": {}, "ban_user_request": {},
	"chat_message": {}, "chat_event": {},
	"screen_share_start_request": {}, "screen_share_stop_request": {},
	"screen_share_subscribe_request": {}, "screen_share_share_request": {},
	"screen_share_unsubscribe_request": {}, "screen_share_event": {},
	"screen_share_frame":    {},
	"set_user_role_request": {}, "set_user_role_response": {},
	"export_data_request": {}, "export_data_response": {},
	"import_channels_request": {}, "import_channels_response": {},
	"error_response": {}, "ping": {}, "pong": {},
}

// VoicePacket represents a voice data packet sent over UDP.
type VoicePacket struct {
	SessionID uint32 // 4 bytes: identifies the sender session
	SeqNum    uint32 // 4 bytes: sequence number for ordering (prevents AES-GCM nonce reuse)
	Timestamp uint32 // 4 bytes: RTP-style timestamp
	ChannelID uint64 // 8 bytes: target channel
	Payload   []byte // encrypted Opus frame + GCM auth tag
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
	// Read length prefix
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return nil, fmt.Errorf("protocol: read length: %w", err)
	}
	length := binary.BigEndian.Uint32(lenBuf)
	if length > MaxControlMessage {
		return nil, fmt.Errorf("protocol: message too large: %d bytes", length)
	}

	// Read payload
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("protocol: read payload: %w", err)
	}
	if err := validateControlEnvelope(data); err != nil {
		return nil, err
	}

	msg := &pb.ControlMessage{}
	if err := json.Unmarshal(data, msg); err != nil {
		return nil, fmt.Errorf("protocol: unmarshal: %w", err)
	}
	return msg, nil
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
				for dec.More() {
					if err := skipJSONValue(dec); err != nil {
						return fmt.Errorf("protocol: invalid control envelope: field %q: %w", key, err)
					}
				}
				if _, err := dec.Token(); err != nil {
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

// skipJSONValue consumes one JSON value from the decoder, ensuring its raw
// bytes are fully parsed and validated by the standard library.
func skipJSONValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); ok {
		if delim == '{' || delim == '[' {
			for dec.More() {
				if err := skipJSONValue(dec); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
		}
	}
	return nil
}
