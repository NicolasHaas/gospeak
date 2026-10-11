package server

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

// Verified UDP registrations must be charged to the session's existing voice
// budget: an authenticated key holder must not bypass the pacing budget by
// flooding registration datagrams.
func TestVoiceRegistrationChargedToVoiceBudget(t *testing.T) {
	sm := NewSessionManagerWithLimits(8, 2)
	session := mustCreateSession(t, sm, 1, "speaker", 0)
	remote := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40000}
	now := time.Unix(1000, 0)

	registration := func(counter uint64) []byte {
		data, err := protocol.MarshalVoiceRegistration(session.ID, counter, session.VoiceRegistrationKey)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	for counter := uint64(1); counter <= 100; counter++ {
		if !sm.RegisterUDPAddr(session.ID, mustUnmarshalRegistration(t, registration(counter)), remote, now, 5*time.Second) {
			t.Fatalf("burst registration %d rejected", counter)
		}
	}
	if sm.RegisterUDPAddr(session.ID, mustUnmarshalRegistration(t, registration(101)), remote, now, 5*time.Second) {
		t.Fatal("registration flood above burst budget accepted")
	}
	// 20 ms refills exactly one token: one registration is admitted, a second
	// at the same instant is refused.
	if !sm.RegisterUDPAddr(session.ID, mustUnmarshalRegistration(t, registration(102)), remote, now.Add(20*time.Millisecond), 5*time.Second) {
		t.Fatal("registration after one-token refill rejected")
	}
	if sm.RegisterUDPAddr(session.ID, mustUnmarshalRegistration(t, registration(103)), remote, now.Add(20*time.Millisecond), 5*time.Second) {
		t.Fatal("second registration at the same instant bypassed the refill rate")
	}
	if !sm.RegisterUDPAddr(session.ID, mustUnmarshalRegistration(t, registration(104)), remote, now.Add(60*time.Millisecond), 5*time.Second) {
		t.Fatal("registration after sustained refill rejected")
	}
}

// The charge happens after HMAC verification: forged registrations neither
// consume budget nor mutate the session.
func TestForgedRegistrationConsumesNoBudget(t *testing.T) {
	sm := NewSessionManagerWithLimits(8, 2)
	session := mustCreateSession(t, sm, 1, "speaker", 0)
	remote := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40000}
	now := time.Unix(1000, 0)

	for counter := uint64(1); counter <= 200; counter++ {
		data, err := protocol.MarshalVoiceRegistration(session.ID, counter, bytes.Repeat([]byte{0x00}, protocol.VoiceRegistrationKeySize))
		if err != nil {
			t.Fatal(err)
		}
		if sm.RegisterUDPAddr(session.ID, mustUnmarshalRegistration(t, data), remote, now, 5*time.Second) {
			t.Fatal("forged registration accepted")
		}
	}
	data, err := protocol.MarshalVoiceRegistration(session.ID, 300, session.VoiceRegistrationKey)
	if err != nil {
		t.Fatal(err)
	}
	if !sm.RegisterUDPAddr(session.ID, mustUnmarshalRegistration(t, data), remote, now, 5*time.Second) {
		t.Fatal("legitimate registration rejected after forged flood consumed budget")
	}
}

func mustUnmarshalRegistration(t *testing.T, data []byte) *protocol.VoiceRegistration {
	t.Helper()
	registration, err := protocol.UnmarshalVoiceRegistration(data)
	if err != nil {
		t.Fatal(err)
	}
	return registration
}

// A registration refused by an exhausted budget must not consume NAT rebind
// headroom: the next attempt after refill succeeds without waiting another
// full rebind interval.
func TestRefusedRebindDoesNotConsumeRebindWindow(t *testing.T) {
	sm := NewSessionManagerWithLimits(8, 2)
	session := mustCreateSession(t, sm, 1, "speaker", 0)
	addrA := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40000}
	addrB := &net.UDPAddr{IP: net.ParseIP("192.0.2.99"), Port: 50000}
	now := time.Unix(1000, 0)

	registration := func(counter uint64, key []byte) *protocol.VoiceRegistration {
		data, err := protocol.MarshalVoiceRegistration(session.ID, counter, key)
		if err != nil {
			t.Fatal(err)
		}
		return mustUnmarshalRegistration(t, data)
	}

	// Initial endpoint registration; VoiceEndpointUpdatedAt starts at t0.
	if !sm.RegisterUDPAddr(session.ID, registration(1, session.VoiceRegistrationKey), addrA, now, 5*time.Second) {
		t.Fatal("initial registration rejected")
	}
	// Rebind interval elapses; budget refills to the burst cap.
	now = now.Add(5*time.Second + time.Millisecond)
	for counter := uint64(2); counter <= 101; counter++ {
		if !sm.RegisterUDPAddr(session.ID, registration(counter, session.VoiceRegistrationKey), addrA, now, 5*time.Second) {
			t.Fatalf("drain registration %d rejected", counter)
		}
	}
	// Budget is empty: the rebind is refused by the charge, not the gate.
	if sm.RegisterUDPAddr(session.ID, registration(102, session.VoiceRegistrationKey), addrB, now, 5*time.Second) {
		t.Fatal("rebind accepted with an exhausted budget")
	}
	// One token refills 20 ms later: the rebind must now succeed without
	// waiting another full rebind interval.
	now = now.Add(20 * time.Millisecond)
	if !sm.RegisterUDPAddr(session.ID, registration(103, session.VoiceRegistrationKey), addrB, now, 5*time.Second) {
		t.Fatal("refused rebind consumed NAT rebind headroom")
	}
	if snapshot, ok := sm.GetSnapshot(session.ID); !ok || snapshot.UDPAddr == nil || snapshot.UDPAddr.String() != addrB.String() {
		t.Fatalf("rebound endpoint = %v, want %v", snapshot.UDPAddr, addrB)
	}
}
