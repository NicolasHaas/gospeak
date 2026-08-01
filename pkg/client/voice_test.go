package client

import (
	"bytes"
	"errors"
	"math"
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

func TestSendVoiceRefusesSequenceWrap(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer listener.Close()

	registrationKey := bytes.Repeat([]byte{0x42}, protocol.VoiceRegistrationKeySize)
	voiceKey := bytes.Repeat([]byte{0x24}, 16)
	client, err := NewVoiceClient(listener.LocalAddr().String(), 1234, voiceKey, registrationKey)
	if err != nil {
		t.Fatalf("NewVoiceClient: %v", err)
	}
	defer client.Close()

	client.seqNum = math.MaxUint32
	if err := client.SendVoice([]byte("frame"), 1); !errors.Is(err, ErrVoiceSequenceExhausted) {
		t.Fatalf("SendVoice error = %v, want %v", err, ErrVoiceSequenceExhausted)
	}
}

func TestNewVoiceClientRegistersEndpointImmediately(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer listener.Close()

	registrationKey := bytes.Repeat([]byte{0x42}, protocol.VoiceRegistrationKeySize)
	voiceKey := bytes.Repeat([]byte{0x24}, 16)
	client, err := NewVoiceClient(listener.LocalAddr().String(), 1234, voiceKey, registrationKey)
	if err != nil {
		t.Fatalf("NewVoiceClient: %v", err)
	}
	defer client.Close()

	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, protocol.VoiceRegistrationPacketSize)
	n, _, err := listener.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("ReadFromUDP: %v", err)
	}
	registration, err := protocol.UnmarshalVoiceRegistration(buf[:n])
	if err != nil {
		t.Fatalf("UnmarshalVoiceRegistration: %v", err)
	}
	if registration.SessionID != 1234 || registration.Counter != 1 || !registration.Verify(registrationKey) {
		t.Fatalf("invalid initial registration: %#v", registration)
	}

	if err := client.SendRegistration(); err != nil {
		t.Fatalf("SendRegistration: %v", err)
	}
	n, _, err = listener.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("ReadFromUDP(refresh): %v", err)
	}
	registration, err = protocol.UnmarshalVoiceRegistration(buf[:n])
	if err != nil {
		t.Fatalf("UnmarshalVoiceRegistration(refresh): %v", err)
	}
	if registration.Counter != 2 || !registration.Verify(registrationKey) {
		t.Fatalf("invalid refreshed registration: %#v", registration)
	}
}
