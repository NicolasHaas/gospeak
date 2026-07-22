package server

import (
	"bytes"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type blockingWriteConn struct {
	mu  sync.Mutex
	buf bytes.Buffer

	active       atomic.Int32
	firstWrite   sync.Once
	firstEntered chan struct{}
	releaseFirst chan struct{}
	concurrent   chan struct{}
	concurrentMu sync.Once
	closed       chan struct{}
	closeOnce    sync.Once
}

func newBlockingWriteConn() *blockingWriteConn {
	return &blockingWriteConn{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		concurrent:   make(chan struct{}),
		closed:       make(chan struct{}),
	}
}

func (c *blockingWriteConn) Read(_ []byte) (int, error) { return 0, io.EOF }

func (c *blockingWriteConn) Write(p []byte) (int, error) {
	if c.active.Add(1) > 1 {
		c.concurrentMu.Do(func() { close(c.concurrent) })
	}
	defer c.active.Add(-1)

	blocked := false
	c.firstWrite.Do(func() {
		blocked = true
		close(c.firstEntered)
	})
	if blocked {
		<-c.releaseFirst
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *blockingWriteConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}
func (c *blockingWriteConn) LocalAddr() net.Addr                { return &net.IPAddr{} }
func (c *blockingWriteConn) RemoteAddr() net.Addr               { return &net.IPAddr{} }
func (c *blockingWriteConn) SetDeadline(_ time.Time) error      { return nil }
func (c *blockingWriteConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *blockingWriteConn) SetWriteDeadline(_ time.Time) error { return nil }

func (c *blockingWriteConn) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.buf.Bytes())
}

func TestControlHandlerSerializesConcurrentWrites(t *testing.T) {
	_, _, handler := newTestServer(t)
	conn := newBlockingWriteConn()
	handler.setConn(1, conn)
	defer handler.removeConn(1)

	messages := []*pb.ControlMessage{
		{Ping: &pb.Ping{Timestamp: 1}},
		{Ping: &pb.Ping{Timestamp: 2}},
	}

	var wg sync.WaitGroup
	for _, msg := range messages {
		wg.Add(1)
		go func(msg *pb.ControlMessage) {
			defer wg.Done()
			if !handler.sendToSession(1, msg) {
				t.Errorf("sendToSession returned false")
			}
		}(msg)
		<-conn.firstEntered
	}

	select {
	case <-conn.concurrent:
		close(conn.releaseFirst)
		wg.Wait()
		t.Fatal("control connection received concurrent Write calls")
	case <-time.After(50 * time.Millisecond):
	}

	close(conn.releaseFirst)
	wg.Wait()

	deadline := time.Now().Add(time.Second)
	for {
		reader := bytes.NewReader(conn.bytes())
		first, firstErr := protocol.ReadControlMessage(reader)
		second, secondErr := protocol.ReadControlMessage(reader)
		if firstErr == nil && secondErr == nil {
			if first.Ping == nil || second.Ping == nil {
				t.Fatalf("unexpected messages: %#v %#v", first, second)
			}
			got := map[int64]bool{first.Ping.Timestamp: true, second.Ping.Timestamp: true}
			if !got[1] || !got[2] {
				t.Fatalf("timestamps = %v, want 1 and 2", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("two valid control frames not written: first=%v second=%v bytes=%d", firstErr, secondErr, len(conn.bytes()))
		}
		time.Sleep(time.Millisecond)
	}
}

func TestControlClientClosesWhenSendQueueIsFull(t *testing.T) {
	_, _, handler := newTestServer(t)
	conn := newBlockingWriteConn()
	handler.setConn(1, conn)
	defer close(conn.releaseFirst)

	if !handler.sendToSession(1, &pb.ControlMessage{Ping: &pb.Ping{Timestamp: 1}}) {
		t.Fatal("initial send failed")
	}
	<-conn.firstEntered

	for i := 0; i < controlSendQueueSize; i++ {
		if !handler.sendToSession(1, &pb.ControlMessage{Ping: &pb.Ping{Timestamp: int64(i + 2)}}) {
			t.Fatalf("queue rejected message %d before reaching capacity", i)
		}
	}
	if handler.sendToSession(1, &pb.ControlMessage{Ping: &pb.Ping{Timestamp: 999}}) {
		t.Fatal("send succeeded after queue reached capacity")
	}

	select {
	case <-conn.closed:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("slow control connection was not closed after queue overflow")
	}
}

func TestBroadcastDoesNotHoldConnectionMapLockDuringWrite(t *testing.T) {
	srv, _, handler := newTestServer(t)
	slow := newBlockingWriteConn()
	handler.setConn(1, slow)
	defer handler.removeConn(1)
	srv.channels.Join(1, 7)

	done := make(chan struct{})
	go func() {
		handler.broadcastToChannel(7, &pb.ControlMessage{Ping: &pb.Ping{Timestamp: 1}}, 0)
		close(done)
	}()
	<-slow.firstEntered

	registered := make(chan struct{})
	go func() {
		handler.setConn(2, &nopConn{})
		close(registered)
	}()

	select {
	case <-registered:
	case <-time.After(50 * time.Millisecond):
		close(slow.releaseFirst)
		t.Fatal("setConn blocked behind a slow control write")
	}

	close(slow.releaseFirst)
	<-done
	handler.removeConn(2)
}
