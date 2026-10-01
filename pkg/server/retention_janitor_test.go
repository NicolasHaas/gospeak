package server

import (
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
)

type observedBanPruneFactory struct {
	datastore.DataProviderFactory
	swept chan int
}
type observedBanPruneStore struct {
	datastore.DataStore
	swept chan int
}

func (f observedBanPruneFactory) NonTx() datastore.DataStore {
	return observedBanPruneStore{f.DataProviderFactory.NonTx(), f.swept}
}
func (st observedBanPruneStore) PruneExpiredBans(limit int) (int64, error) {
	st.swept <- limit
	return 0, nil
}
func TestRetentionJanitorSweepsBansWithoutChat(t *testing.T) {
	srv, st, _ := newTestServerWithConfig(t, func(cfg *Config) { cfg.ChatHistoryLimit = 0; cfg.ChatMaxAge = 0 })
	ticks := make(chan time.Time)
	swept := make(chan int, 1)
	done := make(chan struct{})
	go func() { defer close(done); srv.runRetentionJanitor(observedBanPruneFactory{st, swept}, ticks) }()
	ticks <- time.Now()
	select {
	case limit := <-swept:
		if limit != datastore.MaxBanPageSize {
			t.Fatal(limit)
		}
	case <-time.After(time.Second):
		t.Fatal("ban sweep not scheduled")
	}
	srv.cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("janitor did not stop")
	}
}
