package client

import (
	"bytes"
	"errors"
	"io"
	"math"
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

type voiceBlockedWriteConn struct {
	net.Conn
	entered chan struct{}
}

func (c *voiceBlockedWriteConn) Write(p []byte) (int, error) {
	close(c.entered)
	return c.Conn.Write(p)
}

func TestVoiceCloseUnblocksRegistrationWrites(t *testing.T) {
	for _, response := range []bool{false, true} {
		name := "request"
		if response {
			name = "response"
		}
		t.Run(name, func(t *testing.T) {
			v := &VoiceClient{sessionID: 1, registrationKey: make([]byte, 32), registrationCounter: 1, registrationPending: true}
			challenge := &protocol.VoiceRegistration{SessionID: 1, Counter: 1, Kind: protocol.VoiceRegistrationChallenge, Challenge: [16]byte{1}}
			data, err := challenge.Marshal(v.registrationKey)
			if err != nil {
				t.Fatal(err)
			}
			local, peer := net.Pipe()
			conn := &voiceBlockedWriteConn{Conn: local, entered: make(chan struct{})}
			v.conn = conn
			written, closed := make(chan struct{}), make(chan struct{})
			var writeErr, closeErr error
			go func() {
				defer close(written)
				if response {
					writeErr = v.answerRegistrationChallenge(data)
				} else {
					writeErr = v.SendRegistration()
				}
			}()
			go func() {
				defer close(closed)
				select {
				case <-conn.entered:
					closeErr = v.Close()
				case <-written:
					closeErr = errors.New("registration returned before Write")
				}
			}()
			t.Cleanup(func() {
				_ = local.Close()
				_ = peer.Close()
				<-written
				<-closed
			})
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("Close waited for the registration write instead of interrupting it")
			}
			<-written
			if closeErr != nil || !errors.Is(writeErr, io.ErrClosedPipe) {
				t.Fatalf("Close = %v, write = %v", closeErr, writeErr)
			}
			if !v.closed || v.registrationPending {
				t.Fatal("Close did not clear registration state")
			}
		})
	}
}

func TestSendVoiceRefusesSequenceWrap(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer listener.Close()

	registrationKey := bytes.Repeat([]byte{0x42}, protocol.VoiceRegistrationKeySize)
	voiceKey := bytes.Repeat([]byte{0x24}, 16)
	client, err := NewVoiceClient(listener.LocalAddr().String(), 1234, voiceKey, registrationKey, "aes128")
	if err != nil {
		t.Fatalf("NewVoiceClient: %v", err)
	}
	defer client.Close()

	client.seqNum = math.MaxUint32
	if err := client.SendVoice([]byte("frame"), 1); !errors.Is(err, ErrVoiceSequenceExhausted) {
		t.Fatalf("SendVoice error = %v, want %v", err, ErrVoiceSequenceExhausted)
	}
}

func TestRegistrationCounterNeverWraps(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := NewVoiceClient(listener.LocalAddr().String(), 1, make([]byte, 16), make([]byte, 32), "aes128")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.registrationCounter = math.MaxUint64
	for range 2 {
		if err := client.SendRegistration(); err == nil {
			t.Fatal("registration counter wrapped back into service")
		}
	}
	if client.registrationCounter != math.MaxUint64 {
		t.Fatal("exhaustion mutated counter")
	}
}

func TestNewVoiceClientRegistersEndpointImmediately(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer listener.Close()

	registrationKey := bytes.Repeat([]byte{0x42}, protocol.VoiceRegistrationKeySize)
	voiceKey := bytes.Repeat([]byte{0x24}, 16)
	client, err := NewVoiceClient(listener.LocalAddr().String(), 1234, voiceKey, registrationKey, "aes128")
	if err != nil {
		t.Fatalf("NewVoiceClient: %v", err)
	}
	defer client.Close()

	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, protocol.VoiceRegistrationPacketSize)
	n, _, err := listener.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("ReadFromUDP: %v", err)
	}
	registration, err := protocol.UnmarshalVoiceRegistration(buf[:n])
	if err != nil {
		t.Fatalf("UnmarshalVoiceRegistration: %v", err)
	}
	if registration.SessionID != 1234 || registration.Counter != 1 || !registration.Verify(registrationKey) {
		t.Fatalf("invalid initial registration: %#v", registration)
	}

	if err := client.SendRegistration(); err != nil {
		t.Fatalf("SendRegistration: %v", err)
	}
	n, _, err = listener.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("ReadFromUDP(refresh): %v", err)
	}
	registration, err = protocol.UnmarshalVoiceRegistration(buf[:n])
	if err != nil {
		t.Fatalf("UnmarshalVoiceRegistration(refresh): %v", err)
	}
	if registration.Counter != 2 || !registration.Verify(registrationKey) {
		t.Fatalf("invalid refreshed registration: %#v", registration)
	}
	client.StartReceiving()
	defer func() { _ = client.Close(); <-client.done }()
	challenge := &protocol.VoiceRegistration{SessionID: 1234, Counter: 1, Kind: protocol.VoiceRegistrationChallenge, Challenge: [16]byte{1}}
	// The stale challenge and a challenge for a replacement session must not
	// elicit a response, even when signed with a known fixture key.
	for _, mutate := range []func(){func() {}, func() { challenge.Counter = 2; challenge.SessionID++ }} {
		mutate()
		data, err := challenge.Marshal(registrationKey)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.answerRegistrationChallenge(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := listener.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listener.ReadFromUDP(buf); err == nil {
		t.Fatal("stale/cross-session challenge elicited a response")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatal(err)
	}
	challenge.SessionID = 1234
	data, err := challenge.Marshal(registrationKey)
	if err != nil {
		t.Fatal(err)
	}
	// Use the production receiver, not just its parser.
	if _, err := listener.WriteToUDP(data, client.conn.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, _, err = listener.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	response, err := protocol.UnmarshalVoiceRegistration(buf[:n])
	if err != nil || response.Kind != protocol.VoiceRegistrationResponse || response.Counter != 2 || response.Challenge != challenge.Challenge || !response.Verify(registrationKey) {
		t.Fatalf("invalid returned proof: %#v %v", response, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.answerRegistrationChallenge(data); err != nil {
		t.Fatal(err)
	}
	if err := client.SendRegistration(); !errors.Is(err, net.ErrClosed) {
		t.Fatal("closed client registered again")
	}
}
