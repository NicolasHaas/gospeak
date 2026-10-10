package server

import (
	"bytes"
	"net"
	"testing"
	"time"

	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

func TestAuthenticatedVoiceBudgetBoundsBurst(t *testing.T) {
	srv, _, _ := newTestServer(t)
	cipher, err := gospeakCrypto.NewVoiceCipher(bytes.Repeat([]byte{0x42}, 16))
	if err != nil {
		t.Fatal(err)
	}
	srv.voiceCipher = cipher
	session := mustCreateSession(t, srv.sessions, 1, "speaker", model.RoleUser)
	remote := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40000}
	registration, err := protocol.MarshalVoiceRegistration(session.ID, 1, session.VoiceRegistrationKey)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.handleVoiceRegistration(registration, remote, time.Now()) {
		t.Fatal("registration failed")
	}
	srv.channels.Join(session.ID, 7)
	srv.sessions.SetChannel(session.ID, 7)
	packet := func(sequence uint32) *protocol.VoicePacket {
		pkt := &protocol.VoicePacket{SessionID: session.ID, SeqNum: sequence, ChannelID: 7}
		pkt.Payload = cipher.Encrypt(pkt.SessionID, pkt.SeqNum, pkt.MarshalHeader(), []byte("opus"))
		return pkt
	}
	now := time.Unix(1000, 0)
	forged := packet(777)
	forged.Payload[0] ^= 0xff
	if _, ok := srv.acceptVoicePacketAt(forged, remote, now); ok || len(srv.sessions.voiceReplay) != 0 {
		t.Fatal("forged packet created pacing/replay state")
	}
	for sequence := uint32(1); sequence <= 100; sequence++ {
		if _, ok := srv.acceptVoicePacketAt(packet(sequence), remote, now); !ok {
			t.Fatalf("bounded burst rejected packet %d", sequence)
		}
	}
	if _, ok := srv.acceptVoicePacketAt(packet(101), remote, now); ok {
		t.Fatal("unbounded authenticated burst accepted")
	}
	if _, ok := srv.acceptVoicePacketAt(packet(101), remote, now.Add(20*time.Millisecond)); ok {
		t.Fatal("rate-rejected authenticated sequence replayed after refill")
	}
	for sequence := uint32(102); sequence <= 202; sequence++ {
		now = now.Add(20 * time.Millisecond)
		if _, ok := srv.acceptVoicePacketAt(packet(sequence), remote, now); !ok {
			t.Fatalf("normal 50Hz packet %d rejected", sequence)
		}
	}
	state := srv.sessions.voiceReplay[session.ID]
	state.packets, state.bytes = 1, 0
	if srv.sessions.allowVoicePacket(session.ID, remote, protocol.VoiceHeaderSize+protocol.MaxVoicePayload, now) || state.packets != 1 {
		t.Fatal("byte-budget refusal consumed packet credit or allowed bytes")
	}
	now = now.Add(20 * time.Millisecond)
	if !srv.sessions.allowVoicePacket(session.ID, remote, protocol.VoiceHeaderSize+protocol.MaxVoicePayload, now) {
		t.Fatal("byte budget did not refill at maximum legal 50Hz rate")
	}
	srv.sessions.Remove(session.ID)
	if len(srv.sessions.voiceReplay) != 0 {
		t.Fatal("session removal leaked replay/budget state")
	}
}
