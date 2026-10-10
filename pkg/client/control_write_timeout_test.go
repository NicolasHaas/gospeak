package client

import (
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type controlWriteDeadlineConn struct {
	net.Conn
	deadlines []time.Time
	shorten   bool
	setErr    error
	clearErr  error
}

func (c *controlWriteDeadlineConn) SetWriteDeadline(deadline time.Time) error {
	c.deadlines = append(c.deadlines, deadline)
	if deadline.IsZero() {
		if c.clearErr != nil {
			return c.clearErr
		}
	} else {
		if c.setErr != nil {
			return c.setErr
		}
		if c.shorten {
			deadline = time.Now().Add(20 * time.Millisecond)
		}
	}
	return c.Conn.SetWriteDeadline(deadline)
}

func TestControlSendBoundsStalledWrite(t *testing.T) {
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	transport := &controlWriteDeadlineConn{Conn: conn, shorten: true}
	client := &ControlClient{conn: transport}
	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- client.Send(&pb.ControlMessage{Ping: &pb.Ping{}}) }()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("stalled send err=%v", err)
		}
		if len(transport.deadlines) != 2 || transport.deadlines[0].Before(started.Add(connectTimeout)) || !transport.deadlines[1].IsZero() {
			t.Fatal("send did not set the existing timeout then clear its write deadline")
		}
	case <-time.After(200 * time.Millisecond):
		_ = conn.Close()
		<-done
		t.Fatal("Send remained blocked without a write deadline")
	}
}

func TestControlSendDeadlineFailuresAndSerialization(t *testing.T) {
	failure := errors.New("synthetic deadline failure")
	for _, clear := range []bool{false, true} {
		recorded := &recordingConn{}
		transport := &controlWriteDeadlineConn{Conn: recorded}
		if clear {
			transport.clearErr = failure
		} else {
			transport.setErr = failure
		}
		client := &ControlClient{conn: transport}
		if err := client.Send(&pb.ControlMessage{Ping: &pb.Ping{}}); !errors.Is(err, failure) {
			t.Fatalf("deadline failure was lost: %v", err)
		}
		if !clear && recorded.b.Len() != 0 {
			t.Fatal("wrote after refusing write deadline")
		}
	}
	recorded := &recordingConn{}
	transport := &controlWriteDeadlineConn{Conn: recorded}
	client := &ControlClient{conn: transport}
	var senders sync.WaitGroup
	for range 2 {
		senders.Go(func() {
			for range 10 {
				if err := client.Send(&pb.ControlMessage{Ping: &pb.Ping{}}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	senders.Wait()
	if len(transport.deadlines) != 40 {
		t.Fatalf("deadline calls=%d", len(transport.deadlines))
	}
	for i := 0; i < len(transport.deadlines); i += 2 {
		if transport.deadlines[i].IsZero() || !transport.deadlines[i+1].IsZero() {
			t.Fatal("send deadline/clear calls interleaved")
		}
		if msg, err := protocol.ReadControlMessage(&recorded.b); err != nil || msg.Ping == nil {
			t.Fatalf("concurrent frame corrupted: %v", err)
		}
	}
}
