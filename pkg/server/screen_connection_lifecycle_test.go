package server

import (
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func waitForScreenConn(t *testing.T, srv *Server, sessionID uint32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		srv.screenMu.RLock()
		_, ok := srv.screenConns[sessionID]
		srv.screenMu.RUnlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("screen connection for session %d was not registered", sessionID)
}

func TestControlDisconnectClosesOwnedScreenConnection(t *testing.T) {
	srv, st, handler := newTestServerWithConfig(t, func(cfg *Config) {
		cfg.AllowNoToken = true
		cfg.EnableScreenShare = true
	})
	controlServer, controlClient := net.Pipe()
	controlDone := make(chan struct{})
	go func() {
		srv.handleControlConn(handler, controlServer, st)
		close(controlDone)
	}()
	if err := protocol.WriteControlMessage(controlClient, &pb.ControlMessage{
		AuthRequest: &pb.AuthRequest{Username: "screen-owner"},
	}); err != nil {
		t.Fatalf("write control auth: %v", err)
	}
	response, err := protocol.ReadControlMessage(controlClient)
	if err != nil || response.AuthResponse == nil {
		t.Fatalf("control auth response = %#v, error = %v", response, err)
	}

	screenServer, screenClient := net.Pipe()
	t.Cleanup(func() { _ = screenClient.Close() })
	screenDone := make(chan struct{})
	go func() {
		srv.handleScreenConn(screenServer)
		close(screenDone)
	}()
	if err := protocol.WriteScreenAuth(screenClient, &protocol.ScreenAuth{
		SessionID: response.AuthResponse.SessionID,
		Token:     response.AuthResponse.ScreenAuthToken,
	}); err != nil {
		t.Fatalf("write screen auth: %v", err)
	}
	waitForScreenConn(t, srv, response.AuthResponse.SessionID)
	if _, err := srv.screenShare.Start(1, response.AuthResponse.SessionID, 1, "screen-owner", 800, 600); err != nil {
		t.Fatalf("start screen share: %v", err)
	}
	writeScreenPacketPrefix(t, screenClient, protocol.MaxScreenPacket, response.AuthResponse.SessionID, 1)

	if err := controlClient.Close(); err != nil {
		t.Fatalf("close control connection: %v", err)
	}
	select {
	case <-controlDone:
	case <-time.After(time.Second):
		t.Fatal("control handler did not stop")
	}

	_ = screenClient.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var one [1]byte
	if _, err := screenClient.Read(one[:]); err == nil {
		t.Fatal("screen connection remained open after control disconnect")
	}
	select {
	case <-screenDone:
	case <-time.After(time.Second):
		t.Fatal("screen handler did not stop after control disconnect")
	}
	srv.screenMu.RLock()
	_, registered := srv.screenConns[response.AuthResponse.SessionID]
	srv.screenMu.RUnlock()
	if registered {
		t.Fatal("screen connection remained registered after control disconnect")
	}
}

func TestRetiredControlSessionCannotBindScreenConnection(t *testing.T) {
	srv := New(DefaultConfig(), Dependencies{})
	session := mustCreateSession(t, srv.sessions, 1, "retired", 0)
	srv.removeSessionAndScreenConn(session.ID)
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })

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
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("retired control session installed a screen connection")
	}
	srv.screenMu.RLock()
	_, registered := srv.screenConns[session.ID]
	srv.screenMu.RUnlock()
	if registered {
		t.Fatal("retired control session remained in screen connection map")
	}
}
