package server

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type stateCountingFactory struct {
	datastore.DataProviderFactory
	builds           atomic.Int64
	entered, release chan struct{}
	fail             atomic.Bool
}

func (f *stateCountingFactory) NonTx() datastore.DataStore {
	return stateCountingStore{f.DataProviderFactory.NonTx(), f}
}

type stateCountingStore struct {
	datastore.DataStore
	f *stateCountingFactory
}

func (s stateCountingStore) ListChannels() ([]model.Channel, error) {
	n := s.f.builds.Add(1)
	if n == 1 && s.f.entered != nil {
		close(s.f.entered)
		<-s.f.release
	}
	if s.f.fail.Load() {
		return nil, errControlClientClosed
	}
	return s.DataStore.ListChannels()
}

func TestStatePublicationDeterministicFlushAndMutation(t *testing.T) {
	srv, st, h := newTestServer(t)
	h.statePublisher.Do(func() {}) // drive flushes explicitly instead of wall clock
	c := newControlClient(1, &nopConn{})
	h.connMap[1] = c
	f := &stateCountingFactory{DataProviderFactory: st, entered: make(chan struct{}), release: make(chan struct{})}
	for range 1000 {
		srv.broadcastServerState(f, h)
	}
	if f.builds.Load() != 0 || len(c.sendQueue) != 0 {
		t.Fatal("dirty marks built or sent inline")
	}
	done := make(chan struct{})
	go func() { srv.flushServerState(f, h); close(done) }()
	<-f.entered
	ch := &model.Channel{Name: "during-flush"}
	if err := st.NonTx().CreateChannel(ch); err != nil {
		close(f.release)
		<-done
		t.Fatal(err)
	}
	srv.broadcastServerState(f, h)
	close(f.release)
	<-done
	if !h.stateDirty.Load() {
		t.Fatal("mutation during flush lost")
	}
	srv.flushServerState(f, h)
	srv.flushServerState(f, h)
	if f.builds.Load() != 2 || len(c.sendQueue) != 1 {
		t.Fatalf("builds=%d queue=%d", f.builds.Load(), len(c.sendQueue))
	}
	item := <-c.sendQueue
	found := false
	for _, info := range item.message.ServerStateEvent.Channels {
		if info.ID == ch.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("latest mutation never published")
	}
	f.fail.Store(true)
	srv.broadcastServerState(f, h)
	srv.flushServerState(f, h)
	if !h.stateDirty.Load() {
		t.Fatal("failed build lost dirty state")
	}
}

func TestStatePublisherCancelJoinsAndRejectsStaleFlush(t *testing.T) {
	srv, st, h := newTestServer(t)
	ticks := make(chan time.Time)
	exited := make(chan struct{})
	if !srv.startWorker(func() { srv.runStatePublisher(st, h, ticks); close(exited) }) {
		t.Fatal("publisher refused")
	}
	h.statePublisher.Do(func() {})
	srv.broadcastServerState(st, h)
	c := newControlClient(1, &nopConn{})
	h.connMap[1] = c
	ticks <- time.Time{}
	select {
	case <-c.sendQueue:
	case <-time.After(time.Second):
		t.Fatal("injected tick did not publish dirty state")
	}
	srv.Shutdown()
	select {
	case <-exited:
	default:
		t.Fatal("shutdown did not join publisher")
	}
	f := &stateCountingFactory{DataProviderFactory: st}
	srv.broadcastServerState(f, h)
	srv.flushServerState(f, h)
	if f.builds.Load() != 0 {
		t.Fatal("stale publisher built after shutdown")
	}
	if srv.startWorker(func() { t.Error("worker after shutdown") }) {
		t.Fatal("shutdown admitted worker")
	}
}

func TestStateQueueRejectsRetiredClient(t *testing.T) {
	c := newControlClient(1, &nopConn{})
	_ = c.Close()
	if err := c.send(&pb.ControlMessage{ServerStateEvent: &pb.ServerStateEvent{}}); err != errControlClientClosed {
		t.Fatalf("retired client accepted state: %v", err)
	}
}
