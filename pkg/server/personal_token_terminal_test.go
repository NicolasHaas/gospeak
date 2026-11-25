package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type personalTerminalConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *personalTerminalConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(p)
}

func TestPersonalTokenTerminalTransportIsBoundedWithoutAuthority(t *testing.T) {
	srv, st, handler := newTestServer(t)
	target, old := personalMutationActor(t, srv, st, handler, "self", model.RoleUser, false)
	handler.removeConn(old.sessionID)
	server, peer := net.Pipe()
	defer peer.Close()
	transport := &personalTerminalConn{Conn: server, started: make(chan struct{})}
	c := handler.setConn(old.sessionID, transport)
	c.credentialHash = crypto.HashToken("personal-self")
	candidate, err := crypto.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	version, _ := st.NonTx().PersonalTokenVersion(target.ID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handleMessage(handler, c.sessionID, &pb.ControlMessage{RotatePersonalTokenReq: &pb.RotatePersonalTokenRequest{Version: version, CurrentToken: "personal-self", Replacement: candidate}}, st, c)
	}()
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("terminal response did not start")
	}
	if _, live := srv.sessions.GetSnapshot(c.sessionID); live {
		t.Fatal("terminal response retained session authority")
	}
	if err := c.enqueue(queuedControlMessage{message: &pb.ControlMessage{Pong: &pb.Pong{}}}); err == nil {
		t.Fatal("terminal transport accepted ordinary publication")
	}
	sink := &bufferConn{}
	srv.handleMessage(handler, c.sessionID, &pb.ControlMessage{Ping: &pb.Ping{}}, st, sink)
	if sink.buffer.Len() != 0 {
		t.Fatal("terminal transport retained command authority")
	}
	// Peer intentionally never reads: the real net.Pipe write deadline must retire it.
	select {
	case <-done:
	case <-time.After(controlWriteTimeout + time.Second):
		_ = peer.Close()
		<-done
		t.Fatal("terminal response exceeded its bounded transport deadline")
	}
}

func TestPersonalTokenCompletionAndLogsDoNotEchoSecrets(t *testing.T) {
	srv, st, handler := newTestServer(t)
	target, c := personalMutationActor(t, srv, st, handler, "secret-target", model.RoleUser, false)
	file, err := os.CreateTemp(t.TempDir(), "credential-logs")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(file, nil)))
	defer slog.SetDefault(previous)
	candidate, err := crypto.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	version, _ := st.NonTx().PersonalTokenVersion(target.ID)
	response := personalMutation(t, srv, st, handler, c, &pb.ControlMessage{RotatePersonalTokenReq: &pb.RotatePersonalTokenRequest{Version: version, CurrentToken: "personal-secret-target", Replacement: candidate}})
	srv.Shutdown()
	if response.RotatePersonalTokenResp.Error != "" {
		t.Fatal("rotation failed")
	}
	wire, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{candidate, "personal-secret-target"} {
		if bytes.Contains(wire, []byte(secret)) || bytes.Contains(logs, []byte(secret)) {
			t.Fatal("personal secret echoed in completion or server log")
		}
	}
}
