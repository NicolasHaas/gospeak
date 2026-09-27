package server

import (
	"sync"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
)

type blockedChatPruneFactory struct {
	datastore.DataProviderFactory
	entered chan struct{}
	release chan struct{}
}

func (f blockedChatPruneFactory) NonTx() datastore.DataStore {
	return blockedChatPruneStore{DataStore: f.DataProviderFactory.NonTx(), entered: f.entered, release: f.release}
}

type blockedChatPruneStore struct {
	datastore.DataStore
	entered chan struct{}
	release chan struct{}
}

func (s blockedChatPruneStore) PruneExpiredMessages(age time.Duration, limit int) (int64, error) {
	close(s.entered)
	<-s.release
	return s.DataStore.PruneExpiredMessages(age, limit)
}

func TestShutdownDuringChatPruneReturnsCleanly(t *testing.T) {
	srv, st, _ := newTestServerWithConfig(t, func(cfg *Config) {
		cfg.ControlAddr = "127.0.0.1:0"
		cfg.VoiceAddr = "127.0.0.1:0"
		cfg.DataDir = t.TempDir()
	})
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releasePrune := func() { releaseOnce.Do(func() { close(release) }) }
	srv.store = blockedChatPruneFactory{DataProviderFactory: st, entered: entered, release: release}
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		done <- srv.Run()
	}()
	// Registered after the store's cleanup, so Run exits before the provider closes even on failure.
	t.Cleanup(func() {
		releasePrune()
		<-exited
	})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		srv.Shutdown()
		t.Fatal("Run did not reach the startup chat prune")
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned while startup prune was blocked: %v", err)
	default:
	}
	srv.Shutdown()
	select {
	case err := <-done:
		t.Fatalf("Run returned before startup prune completed: %v", err)
	default:
	}
	releasePrune()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after Shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}
}
