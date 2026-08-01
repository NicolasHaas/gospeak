package server

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

func mustCreateSession(t *testing.T, sessions *SessionManager, userID int64, username string, role model.Role) *model.Session {
	t.Helper()
	session, err := sessions.Create(userID, username, role)
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	return session
}

func mustCreateScopedSession(t *testing.T, sessions *SessionManager, userID int64, username string, role model.Role, channelScope int64) *model.Session {
	t.Helper()
	session, err := sessions.CreateWithChannelScope(userID, username, role, channelScope)
	if err != nil {
		t.Fatalf("Create scoped session: %v", err)
	}
	return session
}

func TestSessionIDAllocatorSkipsReservedValues(t *testing.T) {
	sessions := NewSessionManager()
	sessions.sessionIDSeeded = true
	sessions.nextSessionID = ^uint32(0)

	last, err := sessions.nextIDLocked()
	if err != nil {
		t.Fatalf("allocate max session ID: %v", err)
	}
	first, err := sessions.nextIDLocked()
	if err != nil {
		t.Fatalf("allocate after wrap: %v", err)
	}
	if last != ^uint32(0) || first != 1 {
		t.Fatalf("IDs across wrap = (%d, %d), want (%d, 1)", last, first, uint32(^uint32(0)))
	}

	sessions.nextSessionID = protocol.VoiceRegistrationMagic
	next, err := sessions.nextIDLocked()
	if err != nil {
		t.Fatalf("allocate after registration magic: %v", err)
	}
	if next != protocol.VoiceRegistrationMagic+1 {
		t.Fatalf("ID after registration magic = %d, want %d", next, protocol.VoiceRegistrationMagic+1)
	}
}

func TestSessionIDExhaustionFailsClosed(t *testing.T) {
	sessions := NewSessionManager()
	sessions.sessionIDSeeded = true
	sessions.issuedSessionIDs = usableSessionIDs

	_, err := sessions.Create(1, "user", model.RoleUser)
	if !errors.Is(err, ErrSessionIDExhausted) {
		t.Fatalf("Create error = %v, want %v", err, ErrSessionIDExhausted)
	}
}

func TestSessionIDsAreNotReusedAfterRemoval(t *testing.T) {
	originalReader := rand.Reader
	t.Cleanup(func() { rand.Reader = originalReader })

	// The old allocator read 52 random bytes per session: four for the ID,
	// 32 for voice registration, and 16 for screen authentication. Repeating
	// that input reproduces a historical session-ID collision deterministically.
	chunk := make([]byte, 52)
	binary.BigEndian.PutUint32(chunk[:4], 42)
	rand.Reader = bytes.NewReader(append(append([]byte(nil), chunk...), chunk...))

	sessions := NewSessionManager()
	first := mustCreateSession(t, sessions, 1, "first", model.RoleUser)
	sessions.Remove(first.ID)
	second := mustCreateSession(t, sessions, 2, "second", model.RoleUser)

	if first.ID == second.ID {
		t.Fatalf("session ID %d was reused while the voice key could still be active", first.ID)
	}
}
