package client

import (
	"context"
	"crypto/tls"
	"net"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestAutoTokenSurvivesMediaFailureAndCallbackPressure(t *testing.T) {
	for _, backlog := range []int{0, maxCallbackQueue, maxReliableCallbackQueue} {
		t.Run(strconv.Itoa(backlog), func(t *testing.T) {
			certificate := httptest.NewTLSServer(nil)
			defer certificate.Close()
			listener, err := tls.Listen("tcp", "127.0.0.1:0", certificate.TLS)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			// A closed screen port reliably fails after successful auth.
			var listenConfig net.ListenConfig
			screen, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			screenAddr := screen.Addr().String()
			_ = screen.Close()
			authed := make(chan bool, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					authed <- false
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				if _, err := protocol.ReadControlMessage(conn); err != nil {
					authed <- false
					return
				}
				authed <- true
				_ = protocol.WriteControlMessage(conn, &pb.ControlMessage{AuthResponse: &pb.AuthResponse{
					SessionID: 1, Username: "fixture-user", Role: "user", MediaCipher: "aes128",
					EncryptionKey: make([]byte, 16), VoiceRegistrationKey: make([]byte, 32), AutoToken: "fixture-personal",
					ScreenShareEnabled: true, ScreenAddr: screenAddr, ScreenAuthToken: "fixture-screen",
				}})
			}()
			e := NewEngine()
			defer e.Disconnect()
			blocked, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			e.invokeCallback(func() { close(blocked); <-release })
			<-blocked
			defer unblock()
			for range backlog {
				e.invokeCallback(func() {})
			}
			tokens := make(chan string, 1)
			err = e.ConnectWithAutoToken(listener.Addr().String(), "127.0.0.1:9601", "fixture-invite", "fixture-user", SPKIFingerprint(certificate.Certificate()), func(token string) { tokens <- token })
			if err == nil {
				t.Fatal("media failure unexpectedly connected")
			}
			select {
			case authenticated := <-authed:
				if authenticated != (backlog < maxReliableCallbackQueue) {
					t.Fatal("reliable backlog did not stop authentication before provisioning")
				}
			case <-time.After(4 * time.Second):
				t.Fatal("auth peer did not finish")
			}
			// Drain manually after releasing the blocked runner, keeping cleanup safe.
			unblock()
			if backlog < maxReliableCallbackQueue {
				select {
				case token := <-tokens:
					if token != "fixture-personal" {
						t.Fatal("wrong credential delivered")
					}
				case <-time.After(time.Second):
					t.Fatal("post-auth failure lost personal token")
				}
			} else if len(tokens) != 0 {
				t.Fatal("unauthed connection delivered a credential")
			}
		})
	}
}
