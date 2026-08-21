package server

import (
	"bytes"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

func TestVoiceEndpointRequiresAuthenticatedRegistration(t *testing.T) {
	srv, _, _ := newTestServer(t)
	session := mustCreateSession(t, srv.sessions, 1, "speaker", model.RoleUser)
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
	session := mustCreateSession(t, srv.sessions, 1, "speaker", model.RoleUser)
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

func TestSessionManagerAcceptVoiceSequenceIsAtomicAndLifecycleBound(t *testing.T) {
	manager := NewSessionManager()
	session := mustCreateSession(t, manager, 1, "speaker", model.RoleUser)
	remote := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40000}
	manager.mu.Lock()
	manager.sessions[session.ID].UDPAddr = cloneUDPAddr(remote)
	manager.mu.Unlock()

	var accepted atomic.Int32
	var workers sync.WaitGroup
	for range 64 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, ok := manager.AcceptVoiceSequence(session.ID, remote, 7); ok {
				accepted.Add(1)
			}
		}()
	}
	workers.Wait()
	if got := accepted.Load(); got != 1 {
		t.Fatalf("concurrent accepts = %d, want exactly 1", got)
	}

	manager.Remove(session.ID)
	if _, ok := manager.AcceptVoiceSequence(session.ID, remote, 8); ok {
		t.Fatal("sequence accepted after session removal")
	}
	if _, ok := manager.voiceReplay[session.ID]; ok {
		t.Fatal("session removal retained replay state")
	}
}

func TestAcceptVoicePacketRejectsEndpointRebindAfterAuthentication(t *testing.T) {
	srv, _, _ := newTestServer(t)
	key := bytes.Repeat([]byte{0x42}, 16)
	cipher, err := gospeakCrypto.NewVoiceCipher(key)
	if err != nil {
		t.Fatalf("NewVoiceCipher: %v", err)
	}
	srv.voiceCipher = cipher

	session := mustCreateSession(t, srv.sessions, 1, "speaker", model.RoleUser)
	oldEndpoint := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40000}
	newEndpoint := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40001}
	registeredAt := time.Now()
	registration1, err := protocol.MarshalVoiceRegistration(session.ID, 1, session.VoiceRegistrationKey)
	if err != nil {
		t.Fatalf("MarshalVoiceRegistration(1): %v", err)
	}
	if !srv.handleVoiceRegistration(registration1, oldEndpoint, registeredAt) {
		t.Fatal("initial registration failed")
	}
	const channelID int64 = 7
	srv.channels.Join(session.ID, channelID)
	srv.sessions.SetChannel(session.ID, channelID)
	packet := &protocol.VoicePacket{SessionID: session.ID, SeqNum: 1, ChannelID: uint64(channelID)}
	packet.Payload = cipher.Encrypt(packet.SessionID, packet.SeqNum, packet.MarshalHeader(), []byte("opus"))

	reached := make(chan struct{})
	release := make(chan struct{})
	srv.voiceReplayHook = func() {
		close(reached)
		<-release
	}
	result := make(chan bool, 1)
	go func() {
		_, ok := srv.acceptVoicePacket(packet, oldEndpoint)
		result <- ok
	}()
	select {
	case <-reached:
	case <-time.After(time.Second):
		t.Fatal("packet did not reach post-authentication boundary")
	}

	registration2, err := protocol.MarshalVoiceRegistration(session.ID, 2, session.VoiceRegistrationKey)
	if err != nil {
		t.Fatalf("MarshalVoiceRegistration(2): %v", err)
	}
	if !srv.handleVoiceRegistration(registration2, newEndpoint, registeredAt.Add(voiceRebindInterval)) {
		t.Fatal("authenticated endpoint rebind failed")
	}
	close(release)
	select {
	case ok := <-result:
		if ok {
			t.Fatal("packet from retired endpoint committed after rebind")
		}
	case <-time.After(time.Second):
		t.Fatal("stale endpoint packet did not return")
	}
	srv.voiceReplayHook = nil

	if _, ok := srv.acceptVoicePacket(packet, newEndpoint); !ok {
		t.Fatal("stale endpoint packet poisoned replay state")
	}
}

func TestAcceptVoicePacketAuthenticatesBeforeReplayWindow(t *testing.T) {
	srv, _, _ := newTestServer(t)
	key := bytes.Repeat([]byte{0x42}, 16)
	cipher, err := gospeakCrypto.NewVoiceCipher(key)
	if err != nil {
		t.Fatalf("NewVoiceCipher: %v", err)
	}
	srv.voiceCipher = cipher

	session := mustCreateSession(t, srv.sessions, 1, "speaker", model.RoleUser)
	remote := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40000}
	registration, err := protocol.MarshalVoiceRegistration(session.ID, 1, session.VoiceRegistrationKey)
	if err != nil {
		t.Fatalf("MarshalVoiceRegistration: %v", err)
	}
	if !srv.handleVoiceRegistration(registration, remote, time.Now()) {
		t.Fatal("voice registration failed")
	}
	const channelID int64 = 7
	srv.channels.Join(session.ID, channelID)
	srv.sessions.SetChannel(session.ID, channelID)

	packet := func(sequence uint32) *protocol.VoicePacket {
		pkt := &protocol.VoicePacket{
			SessionID: session.ID,
			SeqNum:    sequence,
			Timestamp: sequence * 960,
			ChannelID: uint64(channelID),
		}
		pkt.Payload = cipher.Encrypt(pkt.SessionID, pkt.SeqNum, pkt.MarshalHeader(), []byte("opus"))
		return pkt
	}

	for _, sequence := range []uint32{10, 12, 11} {
		if got, ok := srv.acceptVoicePacket(packet(sequence), remote); !ok || got != channelID {
			t.Fatalf("acceptVoicePacket(%d) = (%d, %v), want (%d, true)", sequence, got, ok, channelID)
		}
	}
	if _, ok := srv.acceptVoicePacket(packet(10), remote); ok {
		t.Fatal("replayed voice packet was accepted")
	}

	forged := packet(1000)
	forged.Payload[0] ^= 0xff
	if _, ok := srv.acceptVoicePacket(forged, remote); ok {
		t.Fatal("forged voice packet was accepted")
	}
	if _, ok := srv.acceptVoicePacket(packet(13), remote); !ok {
		t.Fatal("forged high sequence poisoned replay state")
	}
}
