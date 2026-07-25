package server

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

func TestVoiceEndpointRequiresAuthenticatedRegistration(t *testing.T) {
	srv, _, _ := newTestServer(t)
	session := srv.sessions.Create(1, "speaker", model.RoleUser)
	legitimate := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40000}
	attacker := &net.UDPAddr{IP: net.ParseIP("198.51.100.20"), Port: 50000}

	voice := (&protocol.VoicePacket{SessionID: session.ID, SeqNum: 1}).Marshal()
	if srv.handleVoiceRegistration(voice, attacker, time.Now()) {
		t.Fatal("ordinary voice packet registered an endpoint")
	}
	if snapshot, _ := srv.sessions.GetSnapshot(session.ID); snapshot.UDPAddr != nil {
		t.Fatalf("foreign first voice packet bound endpoint to %v", snapshot.UDPAddr)
	}

	wrongKey := bytes.Repeat([]byte{0x55}, protocol.VoiceRegistrationKeySize)
	forged, err := protocol.MarshalVoiceRegistration(session.ID, 1, wrongKey)
	if err != nil {
		t.Fatalf("MarshalVoiceRegistration(forged): %v", err)
	}
	if srv.handleVoiceRegistration(forged, attacker, time.Now()) {
		t.Fatal("forged registration was accepted")
	}

	valid, err := protocol.MarshalVoiceRegistration(session.ID, 1, session.VoiceRegistrationKey)
	if err != nil {
		t.Fatalf("MarshalVoiceRegistration(valid): %v", err)
	}
	if !srv.handleVoiceRegistration(valid, legitimate, time.Now()) {
		t.Fatal("valid registration was rejected")
	}
	if snapshot, _ := srv.sessions.GetSnapshot(session.ID); !udpAddrEqual(snapshot.UDPAddr, legitimate) {
		t.Fatalf("endpoint = %v, want %v", snapshot.UDPAddr, legitimate)
	}
}

func TestVoiceRegistrationRejectsReplayAndRateLimitsRebind(t *testing.T) {
	srv, _, _ := newTestServer(t)
	session := srv.sessions.Create(1, "speaker", model.RoleUser)
	first := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40000}
	rebound := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40001}
	now := time.Now()

	registration1, err := protocol.MarshalVoiceRegistration(session.ID, 1, session.VoiceRegistrationKey)
	if err != nil {
		t.Fatalf("MarshalVoiceRegistration(1): %v", err)
	}
	if !srv.handleVoiceRegistration(registration1, first, now) {
		t.Fatal("initial registration was rejected")
	}
	if srv.handleVoiceRegistration(registration1, rebound, now.Add(time.Second)) {
		t.Fatal("replayed registration was accepted")
	}

	registration2, err := protocol.MarshalVoiceRegistration(session.ID, 2, session.VoiceRegistrationKey)
	if err != nil {
		t.Fatalf("MarshalVoiceRegistration(2): %v", err)
	}
	if srv.handleVoiceRegistration(registration2, rebound, now.Add(time.Second)) {
		t.Fatal("rapid endpoint rebind was accepted")
	}
	if !srv.handleVoiceRegistration(registration2, rebound, now.Add(voiceRebindInterval)) {
		t.Fatal("authenticated endpoint rebind after rate limit was rejected")
	}
}
