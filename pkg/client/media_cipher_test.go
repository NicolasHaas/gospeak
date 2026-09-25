package client

import (
	"fmt"
	"net"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestAuthenticateRequiresExplicitSupportedCipher(t *testing.T) {
	for _, tc := range []struct {
		suite   string
		keySize int
		valid   bool
	}{
		{"aes128", 16, true}, {"aes256", 32, true}, {"chacha20", 32, true},
		{"", 16, false}, {"unknown", 32, false}, {"aes128", 32, false},
		{"aes256", 16, false}, {"chacha20", 16, false},
	} {
		t.Run(tc.suite+"/key", func(t *testing.T) {
			server, client := net.Pipe()
			defer server.Close()
			defer client.Close()
			done := make(chan error, 1)
			go func() {
				request, err := protocol.ReadControlMessage(server)
				if err != nil {
					done <- err
					return
				}
				if request.AuthRequest == nil || len(request.AuthRequest.MediaCiphers) != 3 {
					done <- fmt.Errorf("client did not advertise supported suites")
					return
				}
				done <- protocol.WriteControlMessage(server, &pb.ControlMessage{AuthResponse: &pb.AuthResponse{
					SessionID: 1, MediaCipher: tc.suite, EncryptionKey: make([]byte, tc.keySize),
				}})
			}()
			control := &ControlClient{conn: client}
			_, err := control.Authenticate("", "alice")
			if (err == nil) != tc.valid {
				t.Fatalf("Authenticate suite=%q key=%d error=%v", tc.suite, tc.keySize, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestScreenCipherMismatchDisconnectsWithoutInstallingKey(t *testing.T) {
	for _, suite := range []string{"", "aes128", "chacha20"} {
		e := NewEngine()
		g := newConnectionGeneration()
		e.generation = g
		e.state = StateConnected
		e.mediaCipher = "aes256"
		e.sessionID = 10
		e.handleScreenShareEvent(g, &pb.ScreenShareEvent{
			Active: true, SessionID: 10, ChannelID: 1,
			MediaCipher: suite, EncryptionKey: make([]byte, 32),
		})
		<-g.done
		if e.screenCipher != nil || e.activeScreenShare != nil {
			t.Fatalf("suite %q installed mismatched screen key", suite)
		}
		select {
		case <-g.ctx.Done():
		default:
			t.Fatalf("suite %q did not disconnect", suite)
		}
	}
}
