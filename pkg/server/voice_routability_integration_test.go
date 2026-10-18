package server

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/client"
	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

// Real UDP sockets and production client/server code; no codec or audio devices.
func TestH1LoopbackVoiceRequiresReturnedProof(t *testing.T) {
	srv, _, _ := newTestServerWithConfig(t, func(cfg *Config) { cfg.VoiceAddr = "127.0.0.1:0" })
	key := bytes.Repeat([]byte{0x42}, 16)
	var err error
	srv.voiceCipher, err = gospeakCrypto.NewVoiceCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.StartVoice(); err != nil {
		t.Fatal(err)
	}
	serverAddr := srv.voiceConn.LocalAddr().String()
	senderSession := mustCreateSession(t, srv.sessions, 1, "sender", model.RoleUser)
	recipientSession := mustCreateSession(t, srv.sessions, 2, "recipient", model.RoleUser)
	spoofSession := mustCreateSession(t, srv.sessions, 3, "spoofing-holder", model.RoleUser)
	for _, id := range []uint32{senderSession.ID, recipientSession.ID, spoofSession.ID} {
		srv.sessions.SetChannel(id, 7)
		srv.channels.Join(id, 7)
	}
	sender, err := client.NewVoiceClient(serverAddr, senderSession.ID, key, senderSession.VoiceRegistrationKey, "aes128")
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	recipient, err := client.NewVoiceClient(serverAddr, recipientSession.ID, key, recipientSession.VoiceRegistrationKey, "aes128")
	if err != nil {
		t.Fatal(err)
	}
	defer recipient.Close()
	sender.StartReceiving()
	recipient.StartReceiving()
	sender.SetChannel(7)
	waitEndpoint := func(id uint32) {
		t.Helper()
		deadline := time.NewTimer(time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			if snapshot, _ := srv.sessions.GetSnapshot(id); snapshot.UDPAddr != nil {
				return
			}
			select {
			case <-tick.C:
			case <-deadline.C:
				t.Fatal("loopback challenge exchange did not finish")
			}
		}
	}
	waitEndpoint(senderSession.ID)
	waitEndpoint(recipientSession.ID)
	victim, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer victim.Close()
	request, err := protocol.MarshalVoiceRegistration(spoofSession.ID, 1, spoofSession.VoiceRegistrationKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := victim.WriteToUDP(request, srv.voiceConn.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	if err := victim.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1500)
	n, _, err := victim.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	challenge := mustUnmarshalRegistration(t, buf[:n])
	if challenge.Kind != protocol.VoiceRegistrationChallenge {
		t.Fatal("victim got voice before returning proof")
	}
	// Do not answer. The next authenticated voice packet must reach only the
	// genuine client, even though the spoofing holder signed a valid request.
	if err := sender.SendVoice([]byte("loopback-opus-fixture"), 0); err != nil {
		t.Fatal(err)
	}
	select {
	case packet := <-recipient.IncomingPackets:
		plaintext, err := srv.voiceCipher.Decrypt(packet.SessionID, packet.SeqNum, packet.MarshalHeader(), packet.Payload)
		if err != nil || !bytes.Equal(plaintext, []byte("loopback-opus-fixture")) {
			t.Fatalf("forwarded payload: %q, %v", plaintext, err)
		}
	case <-time.After(time.Second):
		t.Fatal("proven client did not receive voice")
	}
	if err := victim.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n, _, err := victim.ReadFromUDP(buf); err == nil {
		t.Fatalf("unanswered victim received %d voice bytes", n)
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatal(err)
	}
	// Expire the real recipient without a janitor, then require a new exchange.
	srv.sessions.mu.Lock()
	srv.sessions.sessions[recipientSession.ID].VoiceEndpointLastSeen = time.Now().Add(-voiceEndpointTTL)
	srv.sessions.mu.Unlock()
	if err := sender.SendVoice([]byte("expired"), 960); err != nil {
		t.Fatal(err)
	}
	select {
	case <-recipient.IncomingPackets:
		t.Fatal("expired recipient received voice")
	case <-time.After(30 * time.Millisecond):
	}
	if err := recipient.SendRegistration(); err != nil {
		t.Fatal(err)
	}
	waitEndpoint(recipientSession.ID)
	if err := sender.SendVoice([]byte("restored"), 1920); err != nil {
		t.Fatal(err)
	}
	select {
	case packet := <-recipient.IncomingPackets:
		if packet.SeqNum != 3 || packet.SessionID != senderSession.ID {
			t.Fatal("delayed packet mistaken for restored forwarding")
		}
	case <-time.After(time.Second):
		t.Fatal("fresh proof did not restore actual forwarding")
	}
}
