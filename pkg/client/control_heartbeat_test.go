package client

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

type heartbeatWriteProbe struct {
	net.Conn
	entered chan struct{}
}

func (c heartbeatWriteProbe) Write(p []byte) (int, error) {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	return c.Conn.Write(p)
}
func TestControlHeartbeatBlockedWriteStopsWithGeneration(t *testing.T) {
	g := newConnectionGeneration()
	server, peer := net.Pipe()
	defer server.Close() //nolint:errcheck
	entered := make(chan struct{}, 1)
	control := &ControlClient{conn: heartbeatWriteProbe{peer, entered}}
	g.control = control
	defer g.closeResources()
	ticks := make(chan time.Time, 1)
	g.run(func(_ context.Context) { g.controlHeartbeat(control, ticks) })
	ticks <- time.Now()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("heartbeat write did not start")
	}
	g.closeResources()
	done := make(chan struct{})
	go func() { g.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked heartbeat held generation teardown")
	}
}

func TestControlHeartbeatIsGenerationOwnedAndCancellable(t *testing.T) {
	g := newConnectionGeneration()
	defer g.cancel()
	server, peer := net.Pipe()
	defer server.Close() //nolint:errcheck
	defer peer.Close()   //nolint:errcheck
	control := &ControlClient{conn: peer}
	ticks := make(chan time.Time, 1)
	done := make(chan struct{})
	go func() { defer close(done); g.controlHeartbeat(control, ticks) }()
	instant := time.Unix(123, 0)
	ticks <- instant
	if err := server.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	msg, err := protocol.ReadControlMessage(server)
	if err != nil || msg.Ping == nil || msg.Ping.Timestamp != instant.UnixMilli() {
		t.Fatalf("heartbeat=%#v %v", msg, err)
	}
	g.cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not stop")
	}
}
