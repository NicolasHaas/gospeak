package server

import (
	"net"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestSendAndCloseClientsFlushesConcurrently(t *testing.T) {
	clients := make(map[uint32]*controlClient)
	queued := make(chan queuedControlMessage, 3)
	release := make(chan struct{})
	for id := uint32(1); id <= 3; id++ {
		conn, peer := net.Pipe()
		t.Cleanup(func() { _ = conn.Close(); _ = peer.Close() })
		client := newControlClient(id, conn)
		clients[id] = client
		go func() {
			item := <-client.sendQueue
			queued <- item
			<-release
			item.result <- nil
		}()
	}
	done := make(chan struct{})
	msg := &pb.ControlMessage{ErrorResponse: &pb.ErrorResponse{Code: 99, Message: "you have been banned"}}
	go func() { sendAndCloseClients(clients, msg); close(done) }()
	defer func() {
		close(release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("terminal close did not finish after flush")
		}
		for id, client := range clients {
			select {
			case <-client.done:
			default:
				t.Errorf("client%d not closed", id)
			}
		}
	}()
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	for range clients {
		select {
		case item := <-queued:
			if item.message != msg || item.result == nil {
				t.Fatal("terminal notice did not request acknowledged flush")
			}
		case <-timer.C:
			t.Fatal("terminal flush serialized behind a stalled target")
		}
	}
	select {
	case <-done:
		t.Fatal("closed targets before terminal flush completed")
	default:
	}
}
