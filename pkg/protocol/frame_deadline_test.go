package protocol

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestFrameDeadlineDoesNotRenewOnPartialReads(t *testing.T) {
	for _, frame := range [][]byte{{0}, {0, 8, 0, 0, '{'}} {
		server, peer := net.Pipe()
		calls := 0
		result := make(chan error, 1)
		go func() {
			_, _, err := ReadControlMessageWithFrameDeadline(server, func(at time.Time) error {
				calls++
				if left := time.Until(at); left <= 0 || left > 5*time.Second {
					return errors.New("wrong completion deadline")
				}
				return server.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
			})
			result <- err
		}()
		if _, err := peer.Write(frame); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-result:
			if !errors.Is(err, os.ErrDeadlineExceeded) || calls != 1 {
				t.Fatalf("partial read: error=%v deadline calls=%d", err, calls)
			}
		case <-time.After(time.Second):
			t.Fatal("frame did not expire")
		}
		_ = server.Close()
		_ = peer.Close()
	}
}

func TestFrameDeadlineClearsOnlyAfterCompleteMessage(t *testing.T) {
	var frame bytes.Buffer
	if err := WriteControlMessage(&frame, &pb.ControlMessage{Ping: &pb.Ping{Timestamp: 7}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	msg, size, err := ReadControlMessageWithFrameDeadline(&frame, func(at time.Time) error {
		calls++
		if (calls == 1 && at.IsZero()) || (calls == 2 && !at.IsZero()) {
			return errors.New("wrong deadline order")
		}
		return nil
	})
	if err != nil || msg.Ping.Timestamp != 7 || size == 0 || calls != 2 {
		t.Fatalf("read: msg=%v size=%d err=%v calls=%d", msg, size, err, calls)
	}
	_, _, err = ReadControlMessageWithFrameDeadline(&frame, func(time.Time) error {
		t.Fatal("idle read changed deadline")
		return nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}
