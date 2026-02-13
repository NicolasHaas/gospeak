// Package protocol defines the voice packet format and control message framing.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

const (
	// VoiceHeaderSize is the byte size of the voice packet header.
	// [sessionID(4) | seqNum(4) | timestamp(4) | channelID(2)] = 14 bytes
	VoiceHeaderSize = 14

	// MaxVoicePayload is the maximum encrypted Opus payload size.
	MaxVoicePayload = 1400

	// MaxControlMessage is the maximum control message size (64KB).
	MaxControlMessage = 65536

	// FrameDuration is the Opus frame duration in milliseconds.
	FrameDuration = 20

	// SampleRate is the audio sample rate in Hz.
	SampleRate = 48000

	// Channels is the number of audio channels (mono).
	AudioChannels = 1

	// FrameSize is the number of samples per frame (SampleRate * FrameDuration / 1000).
	FrameSize = SampleRate * FrameDuration / 1000 // 960
)

// VoicePacket represents a voice data packet sent over UDP.
type VoicePacket struct {
	SessionID uint32 // 4 bytes: identifies the sender session
	SeqNum    uint32 // 4 bytes: sequence number for ordering (prevents AES-GCM nonce reuse)
	Timestamp uint32 // 4 bytes: RTP-style timestamp
	ChannelID uint16 // 2 bytes: target channel
	Payload   []byte // encrypted Opus frame + GCM auth tag
}

// MarshalHeader marshals only the header portion (14 bytes).
func (p *VoicePacket) MarshalHeader() []byte {
	h := make([]byte, VoiceHeaderSize)
	binary.BigEndian.PutUint32(h[0:4], p.SessionID)
	binary.BigEndian.PutUint32(h[4:8], p.SeqNum)
	binary.BigEndian.PutUint32(h[8:12], p.Timestamp)
	binary.BigEndian.PutUint16(h[12:14], p.ChannelID)
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
		ChannelID: binary.BigEndian.Uint16(data[12:14]),
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
	if len(data) > MaxControlMessage {
		return fmt.Errorf("protocol: message too large: %d bytes", len(data))
	}

	// Write length prefix
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(data))) //nolint:gosec // length already bounds-checked above
	if _, err := w.Write(lenBuf); err != nil {
		return fmt.Errorf("protocol: write length: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("protocol: write payload: %w", err)
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

	msg := &pb.ControlMessage{}
	if err := json.Unmarshal(data, msg); err != nil {
		return nil, fmt.Errorf("protocol: unmarshal: %w", err)
	}
	return msg, nil
}
