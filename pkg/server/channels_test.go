package server

import (
	"sync"
	"testing"
)

func TestChannelManagerTryJoinEnforcesCapacityAtomically(t *testing.T) {
	channels := NewChannelManager()
	start := make(chan struct{})
	results := make(chan bool, 2)
	var wg sync.WaitGroup

	for _, sessionID := range []uint32{1, 2} {
		wg.Add(1)
		go func(sessionID uint32) {
			defer wg.Done()
			<-start
			_, joined := channels.TryJoin(sessionID, 10, 1)
			results <- joined
		}(sessionID)
	}
	close(start)
	wg.Wait()
	close(results)

	joined := 0
	for result := range results {
		if result {
			joined++
		}
	}
	if joined != 1 {
		t.Fatalf("successful joins = %d, want 1", joined)
	}
	if got := len(channels.Members(10)); got != 1 {
		t.Fatalf("channel members = %d, want 1", got)
	}
}
