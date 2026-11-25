package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestVoiceReceiverFailureDisconnectsOwningGeneration(t *testing.T) {
	certificate := httptest.NewTLSServer(nil)
	defer certificate.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", certificate.TLS)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				if _, err := protocol.ReadControlMessage(conn); err != nil {
					return
				}
				if err := protocol.WriteControlMessage(conn, &pb.ControlMessage{AuthResponse: &pb.AuthResponse{
					SessionID: 1, Username: "fixture-user", Role: "user", MediaCipher: "aes128",
					EncryptionKey: make([]byte, 16), VoiceRegistrationKey: make([]byte, 32),
				}}); err != nil {
					return
				}
				for {
					if _, err := protocol.ReadControlMessage(conn); err != nil {
						return
					}
				}
			}()
		}
	}()
	udp, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	e := NewEngine()
	e.initAudioFn = func() (*audioResources, error) { return nil, errors.New("fixture has no audio") }
	disconnected := make(chan string, 2)
	e.OnDisconnect = func(reason string) { disconnected <- reason }
	defer func() { e.Disconnect(); _ = listener.Close(); <-peerDone }()
	connect := func() {
		t.Helper()
		if err := e.Connect(listener.Addr().String(), udp.LocalAddr().String(), "fixture-invite", "fixture-user", SPKIFingerprint(certificate.Certificate())); err != nil {
			t.Fatal(err)
		}
	}
	connect()
	g, _, voice, _ := e.connectionClients()
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	if !g.run(func(context.Context) { <-release }) {
		t.Fatal("connected generation rejected worker")
	}
	// Expire a real UDP read while control remains healthy and no audio sends.
	if err := voice.conn.SetReadDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, voice.done, "terminal voice read did not finish")
	waitForSignal(t, g.ctx.Done(), "dead voice receiver left the generation connected")
	select {
	case <-g.done:
		t.Fatal("teardown skipped the admitted worker")
	default:
	}
	unblock()
	waitForSignal(t, g.done, "voice failure teardown self-deadlocked")
	if e.GetState() != StateDisconnected {
		t.Fatal("voice failure did not publish disconnected state")
	}
	select {
	case reason := <-disconnected:
		if reason != "voice connection lost" {
			t.Fatalf("disconnect reason = %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("voice failure did not notify the caller")
	}
	connect()
	replacement, _, _, _ := e.connectionClients()
	// Delayed old-generation teardown must not invalidate a successful reconnect.
	e.handleDisconnectGeneration(g, "stale voice failure")
	if e.GetState() != StateConnected || replacement.ctx.Err() != nil {
		t.Fatal("stale voice failure disconnected the replacement")
	}
	e.Disconnect()
	select {
	case reason := <-disconnected:
		if reason != "user disconnected" {
			t.Fatalf("expected close changed disconnect reason to %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("expected close did not finish")
	}
}
