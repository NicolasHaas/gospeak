package server

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestPersonalTokenRetirementDrainsActualVoiceRelay(t *testing.T) {
	srv, st, handler := newTestServer(t)
	_, admin := personalMutationActor(t, srv, st, handler, "admin", model.RoleAdmin, false)
	target, c := personalMutationActor(t, srv, st, handler, "speaker", model.RoleUser, false)
	_, viewer := personalMutationActor(t, srv, st, handler, "listener", model.RoleUser, false)
	receiver, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	relay, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	senderAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40001}
	for _, entry := range []struct {
		id       uint32
		endpoint *net.UDPAddr
	}{{c.sessionID, senderAddr}, {viewer.sessionID, receiver.LocalAddr().(*net.UDPAddr)}} {
		srv.sessions.mu.RLock()
		key := append([]byte(nil), srv.sessions.sessions[entry.id].VoiceRegistrationKey...)
		srv.sessions.mu.RUnlock()
		registration, err := protocol.MarshalVoiceRegistration(entry.id, 1, key)
		if err != nil {
			t.Fatal(err)
		}
		if !registerVoiceForTest(t, srv, registration, entry.endpoint, time.Now()) {
			t.Fatal("registration refused")
		}
		srv.sessions.SetChannel(entry.id, 1)
		srv.channels.Join(entry.id, 1)
	}
	srv.voiceCipher, err = crypto.NewVoiceCipher(make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	pkt := &protocol.VoicePacket{SessionID: c.sessionID, SeqNum: 1, ChannelID: 1}
	pkt.Payload = srv.voiceCipher.Encrypt(pkt.SessionID, pkt.SeqNum, pkt.MarshalHeader(), []byte("fixture-opus"))
	reached, release, relayed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	srv.voiceReplayHook = func() { close(reached); <-release }
	go func() { defer close(relayed); srv.relayVoicePacket(relay, pkt, senderAddr, pkt.Marshal()) }()
	select {
	case <-reached:
	case <-time.After(time.Second):
		t.Fatal("actual relay did not reach accepted-work barrier")
	}
	version, _ := st.NonTx().PersonalTokenVersion(target.ID)
	completed := make(chan *pb.ControlMessage, 1)
	go func() {
		completed <- personalMutation(t, srv, st, handler, admin, &pb.ControlMessage{RevokePersonalTokenReq: &pb.RevokePersonalTokenRequest{TargetID: target.ID, Version: version}})
	}()
	select {
	case <-completed:
		t.Fatal("revocation completed while accepted relay retained work authority")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-relayed:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not join")
	}
	select {
	case r := <-completed:
		if r.RevokePersonalTokenResp.Error != "" {
			t.Fatal(r.RevokePersonalTokenResp.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retirement did not drain")
	}
	srv.voiceReplayHook = nil
	data := make([]byte, 2048)
	_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err := receiver.ReadFromUDP(data)
	if err != nil || !bytes.Equal(data[:n], pkt.Marshal()) {
		t.Fatal("accepted ciphertext was not relayed unchanged before the drain")
	}
	before := srv.metrics.VoicePacketsOut.Load()
	pkt.SeqNum++
	pkt.Payload = srv.voiceCipher.Encrypt(pkt.SessionID, pkt.SeqNum, pkt.MarshalHeader(), []byte("retired"))
	srv.relayVoicePacket(relay, pkt, senderAddr, pkt.Marshal())
	if srv.metrics.VoicePacketsOut.Load() != before {
		t.Fatal("retired voice emitted after completion")
	}
	sessionKey := make([]byte, protocol.VoiceRegistrationKeySize)
	registration, _ := protocol.MarshalVoiceRegistration(c.sessionID, 2, sessionKey)
	if registerVoiceForTest(t, srv, registration, senderAddr, time.Now()) {
		t.Fatal("retired session re-registered voice")
	}
}

func TestPersonalTokenOwnerSelfRotationPreservesFinalizedMarker(t *testing.T) {
	srv, st, handler := newTestServer(t)
	owner, c := personalMutationActor(t, srv, st, handler, "owner", model.RoleAdmin, true)
	version, _ := st.NonTx().PersonalTokenVersion(owner.ID)
	candidate, err := crypto.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	r := personalMutation(t, srv, st, handler, c, &pb.ControlMessage{RotatePersonalTokenReq: &pb.RotatePersonalTokenRequest{Version: version, CurrentToken: "personal-owner", Replacement: candidate}})
	if r.RotatePersonalTokenResp.Error != "" {
		t.Fatal(r.RotatePersonalTokenResp.Error)
	}
	marked, err := st.NonTx().IsBootstrapUser(owner.ID)
	if err != nil || !marked {
		t.Fatal("owner marker lost")
	}
	state, err := st.NonTx().BootstrapTokenState()
	if err != nil || state != datastore.BootstrapTokenFinalized {
		t.Fatal("setup credential revived")
	}
	got, err := st.NonTx().GetUserByPersonalTokenHash(crypto.HashToken(candidate))
	if err != nil || got == nil || got.ID != owner.ID || got.Role != model.RoleAdmin || got.Username != owner.Username || got.ChannelScope != owner.ChannelScope {
		t.Fatal("owner identity or authority changed")
	}
}
