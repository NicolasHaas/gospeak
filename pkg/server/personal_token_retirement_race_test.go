package server

import (
	"testing"
	"testing/synctest"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestPersonalTokenRetirementConcurrentRealWriter(t *testing.T) {
	srv, _, handler := newTestServer(t)
	session := mustCreateSession(t, srv.sessions, 7, "drain-target", model.RoleUser)
	synctest.Test(t, func(t *testing.T) {
		c := newControlClient(session.ID, &bufferConn{})
		handler.mu.Lock()
		handler.connMap[session.ID] = c
		handler.mu.Unlock()
		received, releaseWriter, writerDone, detached := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
		c.sendQueue <- &queuedControlMessage{message: &pb.ControlMessage{Pong: &pb.Pong{}}}
		c.afterQueueReceive = func() { close(received); <-releaseWriter }
		c.beforeCredentialDrain = func() {
			c.beforeCredentialDrain = nil
			go func() { defer close(writerDone); c.writeLoop() }()
			<-received // The real writer has stolen the last item; no timing inference.
		}
		go func() {
			srv.credentialWorkMu.Lock()
			srv.remoteModerationMu.Lock()
			srv.detachControlSession(handler, c.sessionID)
			srv.remoteModerationMu.Unlock()
			srv.credentialWorkMu.Unlock()
			close(detached)
		}()
		synctest.Wait() // All workers either finished or are durably channel-blocked.
		blocked := false
		select {
		case <-detached:
		default:
			blocked = true
		}
		if blocked {
			c.sendQueue <- &queuedControlMessage{message: &pb.ControlMessage{Pong: &pb.Pong{}}}
		}
		<-detached
		close(releaseWriter)
		<-writerDone
		if blocked {
			t.Fatal("retirement blocked after the real writer consumed the last queued item")
		}
		if !c.terminal || len(c.sendQueue) != 0 {
			t.Fatal("retirement retained queue authority")
		}
	})
}
