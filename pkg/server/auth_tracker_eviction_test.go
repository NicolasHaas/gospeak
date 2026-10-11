package server

import (
	"testing"
	"time"
)

// A full tracker must not lock out fresh sources while it holds only idle
// unexpired entries: an unauthenticated IPv6 /64 rotation must not starve
// legitimate reconnects for a whole window.
func TestAuthTrackerEvictsIdleEntryAtCapacity(t *testing.T) {
	limiter := newAuthRateLimiter(30, time.Minute)
	limiter.maxEntries = 1
	now := time.Unix(1_700_000_000, 0)
	limiter.now = func() time.Time { return now }

	// First source occupies the tracker and fails, leaving an idle entry.
	if !limiter.Allow("2001:db8:1::/64") {
		t.Fatal("first source rejected")
	}
	limiter.RecordFailure("2001:db8:1::/64")

	// A different source arrives while the entry is still unexpired.
	allowed, reason, _, _ := limiter.AllowWithDetails("2001:db8:2::/64")
	if !allowed {
		t.Fatalf("fresh source rejected with %q at tracker capacity", reason)
	}
}

// All-in-flight capacity still rejects: concurrent reservations are bounded
// by the pre-auth admission caps, so this remains the hard ceiling.
func TestAuthTrackerRejectsWhenAllEntriesInFlight(t *testing.T) {
	limiter := newAuthRateLimiter(30, time.Minute)
	limiter.maxEntries = 1

	if !limiter.Allow("2001:db8:1::/64") {
		t.Fatal("first source rejected")
	}
	allowed, reason, current, limit := limiter.AllowWithDetails("2001:db8:2::/64")
	if allowed || reason != "tracker_capacity" || current != 1 || limit != 1 {
		t.Fatalf("tracker rejection = (%t, %q, %d, %d), want (false, tracker_capacity, 1, 1)", allowed, reason, current, limit)
	}
}
