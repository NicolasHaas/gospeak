package server

import (
	"bytes"
	"net"
	"testing"
	"time"

	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestMediaCipherAuthenticationAndScreenShare(t *testing.T) {
	for _, suite := range []string{"aes128", "aes256", "chacha20"} {
		t.Run(suite, func(t *testing.T) {
			srv, st, handler := newTestServer(t)
			srv.cfg.AllowNoToken = true
			srv.cfg.MediaCipher = suite
			srv.screenShare = NewScreenShareManager(suite)
			key, err := gospeakCrypto.GenerateMediaKey(suite)
			if err != nil {
				t.Fatal(err)
			}
			srv.voiceKey = key
			voiceCipher, err := gospeakCrypto.NewMediaCipher(suite, key)
			if err != nil {
				t.Fatal(err)
			}
			srv.voiceCipher = voiceCipher
			serverConn, clientConn := net.Pipe()
			done := make(chan struct{})
			go func() { srv.handleControlConn(handler, serverConn, st); close(done) }()
			defer func() { _ = clientConn.Close(); <-done; srv.Shutdown() }()
			if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{
				Username: "suite-" + suite, MediaCiphers: []string{"aes128", "aes256", "chacha20"},
			}}); err != nil {
				t.Fatal(err)
			}
			response, err := protocol.ReadControlMessage(clientConn)
			if err != nil || response.AuthResponse == nil {
				t.Fatalf("auth response: %#v, %v", response, err)
			}
			if response.AuthResponse.MediaCipher != suite || !bytes.Equal(response.AuthResponse.EncryptionKey, key) {
				t.Fatal("server did not bind selected suite and key to auth response")
			}
			session := mustCreateSession(t, srv.sessions, 2, "voice", model.RoleUser)
			remote := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40000}
			registration, err := protocol.MarshalVoiceRegistration(session.ID, 1, session.VoiceRegistrationKey)
			if err != nil || !srv.handleVoiceRegistration(registration, remote, time.Now()) {
				t.Fatalf("voice registration: %v", err)
			}
			srv.channels.Join(session.ID, 7)
			srv.sessions.SetChannel(session.ID, 7)
			voice := &protocol.VoicePacket{SessionID: session.ID, SeqNum: 1, ChannelID: 7}
			voice.Payload = voiceCipher.Encrypt(voice.SessionID, voice.SeqNum, voice.MarshalHeader(), []byte("opus"))
			if channel, ok := srv.acceptVoicePacket(voice, remote); !ok || channel != 7 {
				t.Fatal("selected voice AEAD was not accepted")
			}
			started, err := srv.screenShare.Start(1, response.AuthResponse.SessionID, 1, "sharer", 640, 480)
			if err != nil {
				t.Fatal(err)
			}
			if started.MediaCipher != suite || bytes.Equal(started.EncryptionKey, key) {
				t.Fatal("screen suite or per-share key is wrong")
			}
			cipher, err := gospeakCrypto.NewMediaCipher(started.MediaCipher, started.EncryptionKey)
			if err != nil {
				t.Fatal(err)
			}
			pkt := &protocol.ScreenPacket{SessionID: started.SessionID, SeqNum: 1}
			pkt.Payload = cipher.Encrypt(pkt.SessionID, pkt.SeqNum, pkt.MarshalHeader(), []byte("frame"))
			if !srv.screenShare.AcceptFrame(started.SessionID, pkt, 0) {
				t.Fatal("selected screen AEAD did not authenticate")
			}
		})
	}
}

func TestMediaCipherMismatchRejectsBeforeProvisioning(t *testing.T) {
	for _, offered := range [][]string{nil, {"aes128"}, {"AES256"}} {
		srv, st, handler := newTestServer(t)
		srv.cfg.AllowNoToken = true
		srv.cfg.MediaCipher = "aes256"
		serverConn, clientConn := net.Pipe()
		done := make(chan struct{})
		go func() { srv.handleControlConn(handler, serverConn, st); close(done) }()
		if err := protocol.WriteControlMessage(clientConn, &pb.ControlMessage{AuthRequest: &pb.AuthRequest{
			Username: "mismatch", MediaCiphers: offered,
		}}); err != nil {
			t.Fatal(err)
		}
		response, err := protocol.ReadControlMessage(clientConn)
		if err != nil || response.ErrorResponse == nil || response.AuthResponse != nil {
			t.Fatalf("mismatch accepted: %#v %v", response, err)
		}
		_ = clientConn.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("rejected control session did not close")
		}
		if srv.sessions.Count() != 0 {
			t.Fatal("mismatch provisioned a session")
		}
		srv.Shutdown()
	}
}
