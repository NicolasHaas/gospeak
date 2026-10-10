package client

import (
	"crypto/tls"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestConnectionBoundAutoTokenUsesOrderedCallbackQueue(t *testing.T) {
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
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := protocol.ReadControlMessage(conn); err != nil {
			return
		}
		if err := protocol.WriteControlMessage(conn, &pb.ControlMessage{AuthResponse: &pb.AuthResponse{SessionID: 1, Username: "fixture-user", Role: "user", MediaCipher: "aes128", EncryptionKey: make([]byte, 16), VoiceRegistrationKey: make([]byte, 32), AutoToken: "fixture-personal"}}); err != nil {
			return
		}
		for {
			if _, err := protocol.ReadControlMessage(conn); err != nil {
				return
			}
		}
	}()
	e := NewEngine()
	defer func() { e.Disconnect(); _ = listener.Close(); <-peerDone }()
	e.initAudioFn = func() (*audioResources, error) { return nil, errors.New("fixture has no audio") }
	events := make(chan string, 3)
	e.OnStateChange = func(state State) {
		if state == StateConnected {
			events <- "connected"
		}
	}
	e.OnAutoToken = func(string) { events <- "wrong callback" }
	callback := func(token string) { events <- token }
	if err := e.ConnectWithAutoToken(listener.Addr().String(), "127.0.0.1:9601", "fixture-invite", "fixture-user", SPKIFingerprint(certificate.Certificate()), callback); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"connected", "fixture-personal"} {
		select {
		case got := <-events:
			if got != want {
				t.Fatal("connection credential callback lost ownership or event order")
			}
		case <-time.After(time.Second):
			t.Fatal("connection credential event was not delivered")
		}
	}
}
