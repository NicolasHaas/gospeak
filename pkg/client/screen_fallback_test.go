package client

import (
	"context"
	"crypto/tls"
	"errors"
	"image"
	"net"
	"net/http/httptest"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

// This loopback peer keeps control and UDP alive independently of the screen port.
func screenFallbackPeer(t *testing.T, cfg *tls.Config, screenAddr string) (string, net.PacketConn, <-chan *pb.ControlMessage) {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	udp, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	incoming := make(chan *pb.ControlMessage, 16)
	done := make(chan struct{})
	var mu sync.Mutex
	var current net.Conn
	closed := false
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		closed = true
		if current != nil {
			_ = current.Close()
		}
		mu.Unlock()
		_ = udp.Close()
		waitForSignal(t, done, "control peer leaked")
	})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		if closed {
			mu.Unlock()
			_ = conn.Close()
			return
		}
		current = conn
		mu.Unlock()
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := protocol.ReadControlMessage(conn); err != nil {
			return
		}
		if err := protocol.WriteControlMessage(conn, &pb.ControlMessage{AuthResponse: &pb.AuthResponse{
			SessionID: 1, Username: "fixture-user", Role: "user", MediaCipher: "aes128",
			EncryptionKey: make([]byte, 16), VoiceRegistrationKey: make([]byte, 32),
			ScreenShareEnabled: true, ScreenAddr: screenAddr, ScreenAuthToken: "fixture-screen",
		}}); err != nil {
			return
		}
		// Re-advertising enabled must not undo local transport degradation.
		if err := protocol.WriteControlMessage(conn, &pb.ControlMessage{ServerStateEvent: &pb.ServerStateEvent{ScreenShareEnabled: true}}); err != nil {
			return
		}
		for {
			msg, err := protocol.ReadControlMessage(conn)
			if err != nil {
				return
			}
			incoming <- msg
		}
	}()
	return listener.Addr().String(), udp, incoming
}

func TestScreenTransportFallbackKeepsControlAndVoice(t *testing.T) {
	certificate := httptest.NewTLSServer(nil)
	defer certificate.Close()
	port, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	screenAddr := port.Addr().String()
	_ = port.Close()
	control, udp, incoming := screenFallbackPeer(t, certificate.TLS, screenAddr)
	e := NewEngine()
	defer e.Disconnect()
	e.initAudioFn = func() (*audioResources, error) { return nil, errors.New("fixture has no audio") }
	warnings := make(chan error, 4)
	e.OnError = func(err error) { warnings <- err }
	stateSeen := make(chan struct{}, 4)
	e.OnChannelsUpdate = func([]pb.ChannelInfo) { stateSeen <- struct{}{} }
	if err := e.Connect(control, udp.LocalAddr().String(), "fixture-invite", "fixture-user", SPKIFingerprint(certificate.Certificate())); err != nil {
		t.Fatal(err)
	}
	select {
	case warning := <-warnings:
		if warning == nil {
			t.Fatal("missing visible warning")
		}
	case <-time.After(time.Second):
		t.Fatal("no screen warning")
	}
	// Connect publishes an initial channel callback too; wait for both deliveries.
	for range 2 {
		select {
		case <-stateSeen:
		case <-time.After(time.Second):
			t.Fatal("state callback missing")
		}
	}
	if e.GetState() != StateConnected || e.IsScreenShareEnabled() {
		t.Fatal("state event re-enabled unavailable screen plane")
	}
	g, _, voice, screen := e.connectionClients()
	if voice == nil || screen != nil || g.ctx.Err() != nil {
		t.Fatal("wrong degraded resources")
	}
	if err := voice.SendRegistration(); err != nil {
		t.Fatal(err)
	}
	if err := udp.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if n, _, err := udp.ReadFrom(make([]byte, 4096)); err != nil || n == 0 {
		t.Fatalf("voice registration did not reach loopback peer: bytes=%d err=%v", n, err)
	}
	if err := e.SendChat(1, "still connected"); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-incoming:
		if msg.ChatMsg == nil || msg.ChatMsg.Text != "still connected" {
			t.Fatalf("wrong control message: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("chat did not reach control peer")
	}
	if err := e.StartScreenShare(0); err == nil {
		t.Fatal("unavailable screen capture started")
	}
	select {
	case err := <-warnings:
		t.Fatalf("repeated warning: %v", err)
	default:
	}
	e.Disconnect()
	if e.IsScreenShareEnabled() {
		t.Fatal("screen capability survived disconnect")
	}
	// A fresh successful screen dial on the same engine clears the degraded state.
	listener, err := tls.Listen("tcp", "127.0.0.1:0", certificate.TLS)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	screenDone := make(chan struct{})
	go func() {
		defer close(screenDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err = protocol.ReadScreenAuth(conn); err == nil {
			_, _ = protocol.ReadScreenPacket(conn)
		}
	}()
	control, udp, _ = screenFallbackPeer(t, certificate.TLS, listener.Addr().String())
	if err = e.Connect(control, udp.LocalAddr().String(), "fixture-invite", "fixture-user", SPKIFingerprint(certificate.Certificate())); err != nil {
		t.Fatal(err)
	}
	if !e.IsScreenShareEnabled() {
		t.Fatal("reconnect retained degradation")
	}
	e.Disconnect()
	waitForSignal(t, screenDone, "screen peer leaked")
}

func TestScreenTransportFallbackTrustAndCancellationStayFatal(t *testing.T) {
	for _, mode := range []string{"changed-key", "protocol", "cancel", "control-cancel", "bad-address", "zero-port"} {
		t.Run(mode, func(t *testing.T) {
			certificate := httptest.NewTLSServer(nil)
			defer certificate.Close()
			otherCert, otherKey := newTestCertificate(t, nil, nil, true, "other.test", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
			otherTLS := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{otherCert.Raw}, PrivateKey: otherKey}}}
			listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			entered, done := make(chan struct{}), make(chan struct{})
			var mu sync.Mutex
			var conn net.Conn
			closed := false
			t.Cleanup(func() {
				_ = listener.Close()
				mu.Lock()
				closed = true
				if conn != nil {
					_ = conn.Close()
				}
				mu.Unlock()
				waitForSignal(t, done, "screen peer leaked")
			})
			go func() {
				defer close(done)
				c, err := listener.Accept()
				if err != nil {
					return
				}
				mu.Lock()
				if closed {
					mu.Unlock()
					_ = c.Close()
					return
				}
				conn = c
				mu.Unlock()
				defer c.Close()
				close(entered)
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				switch mode {
				case "changed-key":
					_ = tls.Server(c, otherTLS).HandshakeContext(context.Background())
				case "protocol":
					_, _ = c.Write([]byte("not TLS\n"))
				case "cancel", "control-cancel":
					b := make([]byte, 4096)
					for {
						if _, err = c.Read(b); err != nil {
							return
						}
					}
				}
			}()
			addr := listener.Addr().String()
			if mode == "bad-address" {
				addr = "invalid-address"
			}
			if mode == "zero-port" {
				addr = "127.0.0.1:0"
			}
			control, udp, _ := screenFallbackPeer(t, certificate.TLS, addr)
			if mode == "control-cancel" {
				control = addr
			}
			e := NewEngine()
			defer e.Disconnect()
			result := make(chan error, 1)
			go func() {
				result <- e.Connect(control, udp.LocalAddr().String(), "fixture-invite", "fixture-user", SPKIFingerprint(certificate.Certificate()))
			}()
			if mode == "cancel" || mode == "control-cancel" {
				waitForSignal(t, entered, "screen dial not reached")
				e.Disconnect()
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("fatal screen failure became success")
				}
				if mode == "changed-key" {
					var changed *ServerIdentityChangedError
					if !errors.As(err, &changed) {
						t.Fatalf("lost trust error: %v", err)
					}
				}
			case <-time.After(2 * time.Second):
				t.Fatal("connection did not finish")
			}
			if e.GetState() != StateDisconnected {
				t.Fatal("failed attempt remained connected")
			}
		})
	}
}

func TestScreenDialUnavailableClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"refused", &net.OpError{Op: "dial", Err: screenConnectionRefused}, true},
		{"network unreachable", &net.OpError{Op: "dial", Err: screenNetworkUnreachable}, true},
		{"host unreachable", &net.OpError{Op: "dial", Err: screenHostUnreachable}, true},
		{"dial timeout", &net.OpError{Op: "dial", Err: context.DeadlineExceeded}, true},
		{"cancelled", &net.OpError{Op: "dial", Err: context.Canceled}, false},
		{"DNS timeout", &net.OpError{Op: "dial", Err: &net.DNSError{IsTimeout: true}}, false},
		{"DNS missing host", &net.OpError{Op: "dial", Err: &net.DNSError{IsNotFound: true}}, false},
		{"permission", &net.OpError{Op: "dial", Err: syscall.EACCES}, false},
		{"TLS read timeout", &net.OpError{Op: "read", Err: context.DeadlineExceeded}, false},
		{"bare deadline", context.DeadlineExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := screenDialUnavailable(tc.err); got != tc.want {
				t.Fatalf("screenDialUnavailable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestScreenStartCannotPublishAfterScreenLoss(t *testing.T) {
	e := NewEngine()
	g := newConnectionGeneration()
	control := &recordingConn{}
	screen, peer := net.Pipe()
	defer peer.Close()
	g.control = &ControlClient{conn: control, done: make(chan struct{})}
	g.screen = &ScreenClient{conn: screen, done: make(chan struct{})}
	e.generation, e.state = g, StateConnected
	e.channelID, e.sessionID = 1, 10
	e.screenShareEnabled, e.mediaCipher = true, "aes128"
	entered, release := make(chan struct{}), make(chan struct{})
	var captureOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() { unblock(); e.Disconnect() }()
	e.captureScreenFn = func(int) (image.Image, error) {
		captureOnce.Do(func() { close(entered); <-release })
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
	}
	result := make(chan error, 1)
	go func() { result <- e.StartScreenShare(0) }()
	waitForSignal(t, entered, "capture did not start")
	e.handleScreenDisconnectGeneration(g)
	unblock()
	select {
	case err := <-result:
		if err == nil {
			t.Error("screen start succeeded after screen loss")
		}
	case <-time.After(time.Second):
		t.Fatal("screen start did not finish")
	}
	control.mu.Lock()
	written := control.b.Len()
	control.mu.Unlock()
	if written != 0 {
		t.Error("screen start sent control bytes after screen loss")
	}
	e.screenMu.Lock()
	pending := e.screenSharePending
	e.screenMu.Unlock()
	if pending {
		t.Error("screen start restored pending state after screen loss")
	}
	event := &pb.ScreenShareEvent{Active: true, ChannelID: 1, SessionID: 10, MediaCipher: "aes128", EncryptionKey: make([]byte, 16)}
	e.handleEventGeneration(g, &pb.ControlMessage{ScreenShareEvent: event})
	e.screenMu.Lock()
	resurrected := e.screenShareRunning || e.activeScreenShare != nil || e.screenCipher != nil
	e.screenMu.Unlock()
	if resurrected || e.IsScreenShareEnabled() {
		t.Error("late share event restored unavailable screen state/worker")
	}
	// Degradation must not hide a malformed authenticated media key.
	invalid := *event
	invalid.EncryptionKey = make([]byte, 15)
	e.handleEventGeneration(g, &pb.ControlMessage{ScreenShareEvent: &invalid})
	waitForSignal(t, g.done, "invalid screen key became nonfatal while degraded")
}
