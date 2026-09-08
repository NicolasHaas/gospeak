package server

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

func writeScreenPacketPrefix(t *testing.T, conn net.Conn, length, sessionID, sequence uint32) {
	t.Helper()
	var prefix [4 + protocol.ScreenHeaderSize]byte
	binary.BigEndian.PutUint32(prefix[0:4], length)
	binary.BigEndian.PutUint32(prefix[4:8], sessionID)
	binary.BigEndian.PutUint32(prefix[8:12], sequence)
	if _, err := conn.Write(prefix[:]); err != nil {
		t.Fatalf("write screen packet prefix: %v", err)
	}
}

func TestScreenIngressRejectsInactiveSharerBeforeReadingFrameBody(t *testing.T) {
	srv := New(DefaultConfig(), Dependencies{})
	session := mustCreateSession(t, srv.sessions, 1, "viewer", 0)
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		srv.closeScreenConns()
	})

	done := make(chan struct{})
	go func() {
		srv.handleScreenConn(serverConn)
		close(done)
	}()
	if err := protocol.WriteScreenAuth(clientConn, &protocol.ScreenAuth{
		SessionID: session.ID,
		Token:     session.ScreenAuthToken,
	}); err != nil {
		t.Fatalf("write screen auth: %v", err)
	}
	writeScreenPacketPrefix(t, clientConn, protocol.MaxScreenPacket, session.ID, 1)

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("inactive sharer body was read before ingress authorization")
	}
}

func TestScreenIngressDrainsRateLimitedBodyWithoutLosingFraming(t *testing.T) {
	srv := New(DefaultConfig(), Dependencies{})
	srv.screenShare.frameIngressNow = func() time.Time { return time.Unix(1_700_000_000, 0) }
	session := mustCreateSession(t, srv.sessions, 1, "sharer", 0)
	srv.sessions.SetChannel(session.ID, 1)
	started, err := srv.screenShare.Start(1, session.ID, session.UserID, session.Username, 800, 600)
	if err != nil {
		t.Fatalf("start screen share: %v", err)
	}
	cipher, err := gospeakCrypto.NewVoiceCipher(started.EncryptionKey)
	if err != nil {
		t.Fatalf("initialize screen cipher: %v", err)
	}
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		srv.closeScreenConns()
	})

	done := make(chan struct{})
	go func() {
		srv.handleScreenConn(serverConn)
		close(done)
	}()
	if err := protocol.WriteScreenAuth(clientConn, &protocol.ScreenAuth{
		SessionID: session.ID,
		Token:     session.ScreenAuthToken,
	}); err != nil {
		t.Fatalf("write screen auth: %v", err)
	}
	first := &protocol.ScreenPacket{SessionID: session.ID, SeqNum: 1}
	first.Payload = cipher.Encrypt(first.SessionID, first.SeqNum, first.MarshalHeader(), []byte("valid-frame"))
	if err := protocol.WriteScreenPacket(clientConn, first); err != nil {
		t.Fatalf("write first screen packet: %v", err)
	}
	if err := protocol.WriteScreenPacket(clientConn, &protocol.ScreenPacket{
		SessionID: session.ID,
		SeqNum:    2,
		Payload:   []byte("discarded-without-allocation"),
	}); err != nil {
		t.Fatalf("write rate-limited screen packet: %v", err)
	}
	writeScreenPacketPrefix(t, clientConn, protocol.MaxScreenPacket, session.ID+1, 3)

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("screen framing was not synchronized after draining a rate-limited body")
	}
	if got := srv.metrics.ScreenShareFramesIn.Load(); got != 1 {
		t.Fatalf("accepted screen frames = %d, want 1", got)
	}
}
