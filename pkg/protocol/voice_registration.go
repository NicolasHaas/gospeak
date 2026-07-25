package protocol

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// VoiceRegistrationMagic distinguishes endpoint-registration datagrams from voice packets.
	// Session IDs must never use this value.
	VoiceRegistrationMagic uint32 = 0x47535231 // "GSR1"

	// VoiceRegistrationKeySize is the size of the per-session registration secret.
	VoiceRegistrationKeySize = 32

	// VoiceRegistrationPacketSize is [magic(4)|sessionID(4)|counter(8)|HMAC-SHA-256(32)].
	VoiceRegistrationPacketSize = 48
)

// VoiceRegistration proves that a UDP endpoint belongs to an authenticated control session.
type VoiceRegistration struct {
	SessionID uint32
	Counter   uint64
	proof     [sha256.Size]byte
	signed    [16]byte
}

// MarshalVoiceRegistration creates an authenticated endpoint-registration datagram.
func MarshalVoiceRegistration(sessionID uint32, counter uint64, key []byte) ([]byte, error) {
	if len(key) != VoiceRegistrationKeySize {
		return nil, fmt.Errorf("protocol: invalid voice registration key length: %d", len(key))
	}
	if sessionID == 0 || sessionID == VoiceRegistrationMagic {
		return nil, errors.New("protocol: invalid voice registration session ID")
	}
	if counter == 0 {
		return nil, errors.New("protocol: invalid voice registration counter")
	}

	packet := make([]byte, VoiceRegistrationPacketSize)
	binary.BigEndian.PutUint32(packet[0:4], VoiceRegistrationMagic)
	binary.BigEndian.PutUint32(packet[4:8], sessionID)
	binary.BigEndian.PutUint64(packet[8:16], counter)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(packet[:16])
	copy(packet[16:], mac.Sum(nil))
	return packet, nil
}

// IsVoiceRegistration reports whether data has the registration datagram marker and size.
func IsVoiceRegistration(data []byte) bool {
	return len(data) == VoiceRegistrationPacketSize && binary.BigEndian.Uint32(data[:4]) == VoiceRegistrationMagic
}

// UnmarshalVoiceRegistration parses a registration datagram. Call Verify before trusting it.
func UnmarshalVoiceRegistration(data []byte) (*VoiceRegistration, error) {
	if !IsVoiceRegistration(data) {
		return nil, errors.New("protocol: invalid voice registration packet")
	}
	registration := &VoiceRegistration{
		SessionID: binary.BigEndian.Uint32(data[4:8]),
		Counter:   binary.BigEndian.Uint64(data[8:16]),
	}
	if registration.SessionID == 0 || registration.SessionID == VoiceRegistrationMagic || registration.Counter == 0 {
		return nil, errors.New("protocol: invalid voice registration fields")
	}
	copy(registration.signed[:], data[:16])
	copy(registration.proof[:], data[16:])
	return registration, nil
}

// Verify authenticates the registration with the session secret.
func (r *VoiceRegistration) Verify(key []byte) bool {
	if r == nil || len(key) != VoiceRegistrationKeySize {
		return false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(r.signed[:])
	return hmac.Equal(r.proof[:], mac.Sum(nil))
}
