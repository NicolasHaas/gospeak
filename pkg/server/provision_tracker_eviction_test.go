package server

import (
	"fmt"
	"testing"
	"time"
)

func TestProvisionTrackerEvictsEarliestIdleEntryAtCapacity(t *testing.T) {
	limiter := newAccountProvisionLimiter(120, time.Hour)
	now := time.Unix(1_700_000_000, 0)
	limiter.now = func() time.Time { return now }
	for i := range limiter.maxEntries {
		key := fmt.Sprintf("source-%d", i)
		limiter.entries[key] = &accountProvisionEntry{successes: 1, resetAt: now.Add(time.Hour)}
	}
	limiter.entries["source-0"].resetAt = now.Add(time.Minute)
	limiter.entries["source-1"].resetAt = now.Add(time.Second)
	limiter.entries["source-1"].inFlight = 1
	reserved, reason, _, _ := limiter.ReserveWithDetails("fresh")
	if !reserved {
		t.Fatalf("fresh source rejected with %q at idle tracker capacity", reason)
	}
	if len(limiter.entries) != limiter.maxEntries || limiter.entries["source-0"] != nil || limiter.entries["source-1"].inFlight != 1 {
		t.Fatal("eviction did not preserve capacity and in-flight ownership")
	}
	limiter.Commit("source-1")
	if limiter.entries["source-1"].successes != 2 || limiter.entries["source-1"].inFlight != 0 {
		t.Fatal("in-flight reservation lost its accounting")
	}
}

func TestProvisionTrackerRejectsAllInFlightCapacity(t *testing.T) {
	limiter := newAccountProvisionLimiter(120, time.Hour)
	limiter.maxEntries = 1
	if !limiter.Reserve("busy") {
		t.Fatal("initial reservation rejected")
	}
	reserved, reason, current, limit := limiter.ReserveWithDetails("fresh")
	if reserved || reason != "tracker_capacity" || current != 1 || limit != 1 {
		t.Fatalf("capacity rejection = (%t, %q, %d, %d)", reserved, reason, current, limit)
	}
}
