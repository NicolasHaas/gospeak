package protocol

import (
	"bytes"
	"testing"
)

func TestVoiceRegistrationRoundTripAndAuthentication(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, VoiceRegistrationKeySize)

	data, err := MarshalVoiceRegistration(1234, 7, key)
	if err != nil {
		t.Fatalf("MarshalVoiceRegistration: %v", err)
	}
	registration, err := UnmarshalVoiceRegistration(data)
	if err != nil {
		t.Fatalf("UnmarshalVoiceRegistration: %v", err)
	}
	if registration.SessionID != 1234 || registration.Counter != 7 {
		t.Fatalf("registration mismatch: got session=%d counter=%d", registration.SessionID, registration.Counter)
	}
	if !registration.Verify(key) {
		t.Fatal("valid registration proof was rejected")
	}

	data[len(data)-1] ^= 0xff
	tampered, err := UnmarshalVoiceRegistration(data)
	if err != nil {
		t.Fatalf("UnmarshalVoiceRegistration(tampered): %v", err)
	}
	if tampered.Verify(key) {
		t.Fatal("tampered registration proof was accepted")
	}
}

func TestVoiceRegistrationRejectsWrongKeyAndMalformedPackets(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, VoiceRegistrationKeySize)
	wrongKey := bytes.Repeat([]byte{0x22}, VoiceRegistrationKeySize)
	data, err := MarshalVoiceRegistration(99, 1, key)
	if err != nil {
		t.Fatalf("MarshalVoiceRegistration: %v", err)
	}
	registration, err := UnmarshalVoiceRegistration(data)
	if err != nil {
		t.Fatalf("UnmarshalVoiceRegistration: %v", err)
	}
	if registration.Verify(wrongKey) {
		t.Fatal("registration proof verified with the wrong session key")
	}

	wrongSession := append([]byte(nil), data...)
	wrongSession[7] ^= 0x01
	wrongSessionRegistration, err := UnmarshalVoiceRegistration(wrongSession)
	if err != nil {
		t.Fatalf("UnmarshalVoiceRegistration(wrong session): %v", err)
	}
	if wrongSessionRegistration.Verify(key) {
		t.Fatal("registration proof verified after changing the session ID")
	}

	if _, err := UnmarshalVoiceRegistration(data[:len(data)-1]); err == nil {
		t.Fatal("short registration packet was accepted")
	}
	if _, err := MarshalVoiceRegistration(99, 1, key[:len(key)-1]); err == nil {
		t.Fatal("invalid registration key length was accepted")
	}
}
