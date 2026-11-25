package protocol

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// VoiceRegistrationMagic versions the coordinated UDP registration protocol.
	// Session IDs must never use this value.
	VoiceRegistrationMagic uint32 = 0x47535232 // "GSR2"

	VoiceRegistrationKeySize = 32
	// [magic(4)|sessionID(4)|counter(8)|kind(1)|challenge(16)|HMAC-SHA-256(32)].
	VoiceRegistrationPacketSize      = 65
	VoiceRegistrationRequest    byte = 0
	VoiceRegistrationChallenge  byte = 1
	VoiceRegistrationResponse   byte = 2
)

// VoiceRegistration authenticates key possession; only a returned challenge
// proves reachability of the source address observed by the server.
type VoiceRegistration struct {
	SessionID uint32
	Counter   uint64
	Kind      byte
	Challenge [16]byte
	proof     [sha256.Size]byte
}

// MarshalVoiceRegistration creates an authenticated request with no challenge.
func MarshalVoiceRegistration(sessionID uint32, counter uint64, key []byte) ([]byte, error) {
	return (&VoiceRegistration{SessionID: sessionID, Counter: counter}).Marshal(key)
}

// Marshal authenticates all registration fields, including direction and challenge.
func (r *VoiceRegistration) Marshal(key []byte) ([]byte, error) {
	if len(key) != VoiceRegistrationKeySize {
		return nil, fmt.Errorf("protocol: invalid voice registration key length: %d", len(key))
	}
	if !r.valid() {
		return nil, errors.New("protocol: invalid voice registration fields")
	}
	packet := r.header()
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(packet)
	return append(packet, mac.Sum(nil)...), nil
}

func (r *VoiceRegistration) valid() bool {
	return r != nil && r.SessionID != 0 && r.SessionID != VoiceRegistrationMagic && r.Counter != 0 && r.Kind <= VoiceRegistrationResponse && (r.Kind != VoiceRegistrationRequest || r.Challenge == [16]byte{})
}

func (r *VoiceRegistration) header() []byte {
	packet := make([]byte, VoiceRegistrationPacketSize-sha256.Size)
	binary.BigEndian.PutUint32(packet[:4], VoiceRegistrationMagic)
	binary.BigEndian.PutUint32(packet[4:8], r.SessionID)
	binary.BigEndian.PutUint64(packet[8:16], r.Counter)
	packet[16] = r.Kind
	copy(packet[17:], r.Challenge[:])
	return packet
}

// IsVoiceRegistration reports whether data has the GSR2 marker and exact size.
func IsVoiceRegistration(data []byte) bool {
	return len(data) == VoiceRegistrationPacketSize && binary.BigEndian.Uint32(data[:4]) == VoiceRegistrationMagic
}

// UnmarshalVoiceRegistration parses a registration datagram. Verify before use.
func UnmarshalVoiceRegistration(data []byte) (*VoiceRegistration, error) {
	if !IsVoiceRegistration(data) {
		return nil, errors.New("protocol: invalid voice registration packet")
	}
	r := &VoiceRegistration{SessionID: binary.BigEndian.Uint32(data[4:8]), Counter: binary.BigEndian.Uint64(data[8:16]), Kind: data[16]}
	copy(r.Challenge[:], data[17:33])
	copy(r.proof[:], data[33:])
	if !r.valid() {
		return nil, errors.New("protocol: invalid voice registration fields")
	}
	return r, nil
}

// Verify authenticates the current fields, not a separately retained header.
func (r *VoiceRegistration) Verify(key []byte) bool {
	if !r.valid() || len(key) != VoiceRegistrationKeySize {
		return false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(r.header())
	return hmac.Equal(r.proof[:], mac.Sum(nil))
}
