package server

import (
	"net"
	"testing"
	"time"

	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

func registerVoiceForTest(t *testing.T, srv *Server, data []byte, addr *net.UDPAddr, now time.Time) bool {
	t.Helper()
	registration, err := protocol.UnmarshalVoiceRegistration(data)
	if err != nil {
		return false
	}
	return registerUDPForTest(t, srv.sessions, registration.SessionID, registration, addr, now, voiceRebindInterval)
}

func registerUDPForTest(t *testing.T, sm *SessionManager, id uint32, request *protocol.VoiceRegistration, addr *net.UDPAddr, now time.Time, interval time.Duration) bool {
	t.Helper()
	reply, ok := sm.RegisterUDPAddr(id, request, addr, now, interval)
	if !ok || reply == nil {
		return ok
	}
	challenge := mustUnmarshalRegistration(t, reply)
	challenge.Kind = protocol.VoiceRegistrationResponse
	sm.mu.RLock()
	key := append([]byte(nil), sm.sessions[id].VoiceRegistrationKey...)
	sm.mu.RUnlock()
	response, err := challenge.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	_, ok = sm.RegisterUDPAddr(id, mustUnmarshalRegistration(t, response), addr, now, interval)
	return ok
}

func TestH1UnansweredRegistrationCannotReceiveVoice(t *testing.T) {
	srv, _, _ := newTestServer(t)
	session := mustCreateSession(t, srv.sessions, 1, "spoofing-key-holder", 0)
	victim := &net.UDPAddr{IP: net.ParseIP("192.0.2.99"), Port: 40000}
	request, err := protocol.MarshalVoiceRegistration(session.ID, 1, session.VoiceRegistrationKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	reply, ok := srv.handleVoiceRegistration(request, victim, now)
	if !ok || len(reply) != len(request) {
		t.Fatal("missing or amplifying initial challenge")
	}
	if snapshot, _ := srv.sessions.GetSnapshot(session.ID); snapshot.UDPAddr != nil {
		t.Fatal("session key possession committed an unanswered spoofed destination")
	}
	// Pending requests overwrite one slot and share the existing 100-token burst.
	accepted := 1
	for counter := uint64(2); counter <= 102; counter++ {
		request, err := protocol.MarshalVoiceRegistration(session.ID, counter, session.VoiceRegistrationKey)
		if err != nil {
			t.Fatal(err)
		}
		if reply, ok := srv.handleVoiceRegistration(request, victim, now); ok {
			accepted++
			if len(reply) != len(request) {
				t.Fatal("challenge amplified request")
			}
		}
	}
	if accepted != voicePacketBurst || len(srv.sessions.voiceReplay) != 1 || session.UDPAddr != nil {
		t.Fatal("pending request flood bypassed bounded state/budget or installed a recipient")
	}
}

func TestH1ChallengeBindingReplayAndLifecycle(t *testing.T) {
	srv, _, _ := newTestServer(t)
	session := mustCreateSession(t, srv.sessions, 1, "holder", 0)
	other := mustCreateSession(t, srv.sessions, 2, "other", 0)
	first := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40000}
	rebound := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40001}
	now := time.Unix(1000, 0)
	challengeResponse := func(counter uint64, addr *net.UDPAddr, at time.Time) []byte {
		t.Helper()
		request, err := protocol.MarshalVoiceRegistration(session.ID, counter, session.VoiceRegistrationKey)
		if err != nil {
			t.Fatal(err)
		}
		reply, ok := srv.handleVoiceRegistration(request, addr, at)
		if !ok || len(reply) != len(request) {
			t.Fatal("missing or amplifying challenge")
		}
		challenge := mustUnmarshalRegistration(t, reply)
		if challenge.Kind != protocol.VoiceRegistrationChallenge || !challenge.Verify(session.VoiceRegistrationKey) {
			t.Fatal("unauthenticated server challenge")
		}
		// A reflected server challenge is not a client response.
		if _, ok := srv.handleVoiceRegistration(reply, addr, at); ok {
			t.Fatal("challenge reflected as response")
		}
		challenge.Kind = protocol.VoiceRegistrationResponse
		response, err := challenge.Marshal(session.VoiceRegistrationKey)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := challengeResponse(1, first, now)
	if _, ok := srv.handleVoiceRegistration(response, rebound, now); ok {
		t.Fatal("cross-address response accepted")
	}
	wrong := mustUnmarshalRegistration(t, response)
	wrong.Challenge[0] ^= 1
	wrongProof, err := wrong.Marshal(session.VoiceRegistrationKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.handleVoiceRegistration(wrongProof, first, now); ok {
		t.Fatal("wrong challenge accepted despite a valid session HMAC")
	}
	copied := mustUnmarshalRegistration(t, response)
	copied.SessionID = other.ID
	crossSession, err := copied.Marshal(other.VoiceRegistrationKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.handleVoiceRegistration(crossSession, first, now); ok {
		t.Fatal("cross-session challenge accepted")
	}
	if _, ok := srv.handleVoiceRegistration(response, first, now.Add(voiceChallengeTTL)); ok {
		t.Fatal("challenge accepted at expiry")
	}
	now = now.Add(voiceChallengeTTL + time.Second)
	fresh := challengeResponse(2, first, now)
	if _, ok := srv.handleVoiceRegistration(response, first, now); ok {
		t.Fatal("superseded challenge accepted")
	}
	if _, ok := srv.handleVoiceRegistration(fresh, first, now); !ok {
		t.Fatal("genuine registration refused")
	}
	if _, ok := srv.handleVoiceRegistration(fresh, first, now); ok {
		t.Fatal("returned proof replayed")
	}
	now = now.Add(voiceRebindInterval)
	response = challengeResponse(3, rebound, now)
	snapshot, _ := srv.sessions.getSnapshotAt(session.ID, now)
	if !udpAddrEqual(snapshot.UDPAddr, first) {
		t.Fatal("unanswered rebind displaced proven endpoint")
	}
	if _, ok := srv.handleVoiceRegistration(response, rebound, now); !ok {
		t.Fatal("genuine NAT rebind refused")
	}
	snapshot, _ = srv.sessions.getSnapshotAt(session.ID, now)
	if !udpAddrEqual(snapshot.UDPAddr, rebound) {
		t.Fatal("rebind not installed")
	}
	// Snapshots never alias the stored destination.
	snapshot.UDPAddr.Port++
	snapshot, _ = srv.sessions.getSnapshotAt(session.ID, now)
	if !udpAddrEqual(snapshot.UDPAddr, rebound) {
		t.Fatal("snapshot mutated stored destination")
	}
	now = now.Add(voiceRebindInterval)
	response = challengeResponse(^uint64(0), first, now)
	srv.sessions.Remove(session.ID)
	replacement := mustCreateSession(t, srv.sessions, 1, "reconnected", 0)
	if replacement.ID == session.ID {
		t.Fatal("reconnect reused session identity")
	}
	if _, ok := srv.handleVoiceRegistration(response, first, now); ok {
		t.Fatal("removed session completed pending proof")
	}
	// Shutdown closes the manager's shared endpoint admission, not just its socket.
	request, err := protocol.MarshalVoiceRegistration(replacement.ID, 1, replacement.VoiceRegistrationKey)
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := srv.handleVoiceRegistration(request, first, now)
	if !ok || reply == nil {
		t.Fatal("replacement did not get its own challenge")
	}
	proof := mustUnmarshalRegistration(t, reply)
	proof.Kind = protocol.VoiceRegistrationResponse
	reply, err = proof.Marshal(replacement.VoiceRegistrationKey)
	if err != nil {
		t.Fatal(err)
	}
	srv.Shutdown()
	if _, ok := srv.handleVoiceRegistration(reply, first, now); ok {
		t.Fatal("shutdown allowed pending endpoint commit")
	}
}

func TestH1EndpointExpiryValidActivityAndFreshProof(t *testing.T) {
	srv, _, _ := newTestServer(t)
	session := mustCreateSession(t, srv.sessions, 1, "silent-recipient", 0)
	addr := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40000}
	now := time.Unix(1000, 0)
	request, err := protocol.MarshalVoiceRegistration(session.ID, 1, session.VoiceRegistrationKey)
	if err != nil {
		t.Fatal(err)
	}
	if !registerVoiceForTest(t, srv, request, addr, now) {
		t.Fatal("initial proof failed")
	}
	if snapshot, _ := srv.sessions.getSnapshotAt(session.ID, now.Add(voiceEndpointTTL-time.Nanosecond)); snapshot.UDPAddr == nil {
		t.Fatal("endpoint expired early")
	}
	now = now.Add(voiceEndpointTTL)
	if snapshot, _ := srv.sessions.getSnapshotAt(session.ID, now); snapshot.UDPAddr != nil {
		t.Fatal("expiry needs a janitor to stop forwarding")
	}
	request, err = protocol.MarshalVoiceRegistration(session.ID, 2, session.VoiceRegistrationKey)
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := srv.handleVoiceRegistration(request, addr, now)
	if !ok || reply == nil {
		t.Fatal("expired endpoint bypassed fresh reachability proof")
	}
	if snapshot, _ := srv.sessions.getSnapshotAt(session.ID, now); snapshot.UDPAddr != nil {
		t.Fatal("request alone restored expired recipient")
	}
	proof := mustUnmarshalRegistration(t, reply)
	proof.Kind = protocol.VoiceRegistrationResponse
	response, err := proof.Marshal(session.VoiceRegistrationKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.handleVoiceRegistration(response, addr, now); !ok {
		t.Fatal("fresh proof did not restore endpoint")
	}
	now = now.Add(5 * time.Second)
	request, err = protocol.MarshalVoiceRegistration(session.ID, 3, session.VoiceRegistrationKey)
	if err != nil {
		t.Fatal(err)
	}
	if reply, ok := srv.handleVoiceRegistration(request, addr, now); !ok || reply != nil {
		t.Fatal("silent-client refresh required another challenge")
	}
	srv.voiceCipher, err = gospeakCrypto.NewVoiceCipher(make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	srv.channels.Join(session.ID, 7)
	srv.sessions.SetChannel(session.ID, 7)
	packet := &protocol.VoicePacket{SessionID: session.ID, SeqNum: 1, ChannelID: 7}
	packet.Payload = srv.voiceCipher.Encrypt(session.ID, 1, packet.MarshalHeader(), []byte("opus"))
	now = now.Add(5 * time.Second)
	if _, ok := srv.acceptVoicePacketAt(packet, addr, now); !ok {
		t.Fatal("valid audio failed to renew endpoint")
	}
	expiry := now.Add(voiceEndpointTTL)
	// A channel move between receive validation and the final activity commit
	// must not renew the retired channel's endpoint activity.
	srv.sessions.SetChannel(session.ID, 8)
	if srv.sessions.allowVoicePacket(session.ID, addr, 7, 1, now.Add(time.Second)) || !session.VoiceEndpointLastSeen.Equal(now) {
		t.Fatal("stale channel commit renewed activity")
	}
	srv.sessions.SetChannel(session.ID, 7)
	if _, ok := srv.acceptVoicePacketAt(packet, addr, expiry.Add(-time.Nanosecond)); ok {
		t.Fatal("replay renewed endpoint")
	}
	packet.SeqNum = 2 // authentic ciphertext with a forged header must not renew activity.
	if _, ok := srv.acceptVoicePacketAt(packet, addr, expiry.Add(-time.Nanosecond)); ok {
		t.Fatal("forgery renewed endpoint")
	}
	if snapshot, _ := srv.sessions.getSnapshotAt(session.ID, expiry); snapshot.UDPAddr != nil {
		t.Fatal("invalid activity kept recipient routable")
	}
	packet.Payload = srv.voiceCipher.Encrypt(session.ID, 2, packet.MarshalHeader(), []byte("opus"))
	if _, ok := srv.acceptVoicePacketAt(packet, addr, expiry); ok {
		t.Fatal("ordinary voice revived expired endpoint")
	}
	request, err = protocol.MarshalVoiceRegistration(session.ID, ^uint64(0), session.VoiceRegistrationKey)
	if err != nil {
		t.Fatal(err)
	}
	if !registerVoiceForTest(t, srv, request, addr, expiry) {
		t.Fatal("fresh proof at counter limit refused")
	}
	request, err = protocol.MarshalVoiceRegistration(session.ID, 1, session.VoiceRegistrationKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.handleVoiceRegistration(request, addr, expiry); ok {
		t.Fatal("wrapped registration counter accepted")
	}
	packet.SeqNum = 1
	packet.Payload = srv.voiceCipher.Encrypt(session.ID, 1, packet.MarshalHeader(), []byte("opus"))
	if _, ok := srv.acceptVoicePacketAt(packet, addr, expiry); ok {
		t.Fatal("expiry reset media replay state")
	}
	packet.SeqNum = 2
	packet.Payload = srv.voiceCipher.Encrypt(session.ID, 2, packet.MarshalHeader(), []byte("opus"))
	if _, ok := srv.acceptVoicePacketAt(packet, addr, expiry); !ok {
		t.Fatal("fresh proof did not restore valid audio")
	}
	srv.Shutdown()
	if snapshot, _ := srv.sessions.GetSnapshot(session.ID); snapshot.UDPAddr != nil {
		t.Fatal("shutdown retained forwarding eligibility")
	}
	for _, snapshot := range srv.sessions.GetAllByUserIDSnapshots(session.UserID) {
		if snapshot.UDPAddr != nil {
			t.Fatal("account snapshot bypassed shutdown endpoint gate")
		}
	}
}
