package server

import (
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

const capacityLogInterval = 10 * time.Second

type capacityUsageSnapshot struct {
	maxUsage      int
	activeSources int
	sourceLimit   int
	trackerLimit  int
}

type preAuthCapacitySnapshot struct {
	current       map[preAuthPlane]int
	maxBySource   map[preAuthPlane]int
	activeSources map[preAuthPlane]int
}

func storeHighWater(mark *atomic.Int64, value int) {
	candidate := int64(value)
	for current := mark.Load(); candidate > current; current = mark.Load() {
		if mark.CompareAndSwap(current, candidate) {
			return
		}
	}
}

func (s *Server) observePreAuthUsage(plane preAuthPlane, current, sourceCurrent int) {
	if plane == preAuthControl {
		storeHighWater(&s.metrics.PreAuthControlHighWater, current)
		storeHighWater(&s.metrics.PreAuthControlSourceHighWater, sourceCurrent)
		return
	}
	storeHighWater(&s.metrics.PreAuthScreenHighWater, current)
	storeHighWater(&s.metrics.PreAuthScreenSourceHighWater, sourceCurrent)
}

func (s *Server) preAuthCapacitySnapshot() preAuthCapacitySnapshot {
	s.preAuthMu.Lock()
	defer s.preAuthMu.Unlock()

	snapshot := preAuthCapacitySnapshot{
		current:       make(map[preAuthPlane]int, 2),
		maxBySource:   make(map[preAuthPlane]int, 2),
		activeSources: make(map[preAuthPlane]int, 2),
	}
	for _, plane := range []preAuthPlane{preAuthControl, preAuthScreen} {
		snapshot.current[plane] = s.preAuthCount[plane]
		snapshot.activeSources[plane] = len(s.preAuthByIP[plane])
		for _, count := range s.preAuthByIP[plane] {
			if count > snapshot.maxBySource[plane] {
				snapshot.maxBySource[plane] = count
			}
		}
	}
	return snapshot
}

func (rl *authRateLimiter) usageSnapshot() capacityUsageSnapshot {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	snapshot := capacityUsageSnapshot{
		sourceLimit:  rl.maxAttempts,
		trackerLimit: rl.maxEntries,
	}
	for _, entry := range rl.entries {
		if !now.Before(entry.resetAt) && entry.inFlight == 0 {
			continue
		}
		snapshot.activeSources++
		if usage := entry.count + entry.inFlight; usage > snapshot.maxUsage {
			snapshot.maxUsage = usage
		}
	}
	return snapshot
}

func (rl *accountProvisionLimiter) usageSnapshot() capacityUsageSnapshot {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	snapshot := capacityUsageSnapshot{
		sourceLimit:  rl.maxSuccess,
		trackerLimit: rl.maxEntries,
	}
	for _, entry := range rl.entries {
		if !now.Before(entry.resetAt) && entry.inFlight == 0 {
			continue
		}
		snapshot.activeSources++
		if usage := entry.successes + entry.inFlight; usage > snapshot.maxUsage {
			snapshot.maxUsage = usage
		}
	}
	return snapshot
}

func (s *Server) allowAuthWithObservability(key string) (allowed bool, reason string, current, limit int) {
	s.authLimiter.mu.Lock()
	defer s.authLimiter.mu.Unlock()
	allowed, reason, current, limit = s.authLimiter.allowWithDetailsLocked(key)
	if allowed {
		storeHighWater(&s.metrics.AuthRateLimitSourceHighWater, current)
	}
	return
}

func (s *Server) reserveAccountWithObservability(key string) (reserved bool, reason string, current, limit int) {
	s.accountProvisionLimiter.mu.Lock()
	defer s.accountProvisionLimiter.mu.Unlock()
	reserved, reason, current, limit = s.accountProvisionLimiter.reserveWithDetailsLocked(key)
	if reserved {
		storeHighWater(&s.metrics.AccountProvisionSourceHighWater, current)
	}
	return
}

func (s *Server) recordAuthRateLimitRejection(remote, reason string, current, limit int) {
	switch reason {
	case "source":
		s.metrics.AuthRateLimitSourceRejections.Add(1)
	case "tracker_capacity":
		s.metrics.AuthRateLimitTrackerRejections.Add(1)
	case "window_transition":
		s.metrics.AuthRateLimitWindowRejections.Add(1)
	default:
		return
	}
	s.logCapacityLimit("auth", "", reason, remote, current, limit)
}

func (s *Server) recordAccountProvisionRejection(remote, reason string, current, limit int) {
	switch reason {
	case "source":
		s.metrics.AccountProvisionSourceRejections.Add(1)
	case "tracker_capacity":
		s.metrics.AccountProvisionTrackerRejections.Add(1)
	case "window_transition":
		s.metrics.AccountProvisionWindowRejections.Add(1)
	default:
		return
	}
	s.logCapacityLimit("account_provisioning", "", reason, remote, current, limit)
}

func (s *Server) recordPreAuthRejection(plane preAuthPlane, reason, remote string, current, limit int) {
	switch {
	case plane == preAuthControl && reason == "global":
		s.metrics.PreAuthControlGlobalRejections.Add(1)
	case plane == preAuthControl && reason == "source":
		s.metrics.PreAuthControlSourceRejections.Add(1)
	case plane == preAuthScreen && reason == "global":
		s.metrics.PreAuthScreenGlobalRejections.Add(1)
	case plane == preAuthScreen && reason == "source":
		s.metrics.PreAuthScreenSourceRejections.Add(1)
	default:
		return
	}
	s.logCapacityLimit("preauth", string(plane), reason, remote, current, limit)
}

// logCapacityLimit logs the first rejection immediately and then at most once
// per interval for each bounded kind/plane/reason tuple. Metrics still count
// every rejection, so an attacker cannot amplify logs by repeatedly hitting a
// limit.
func (s *Server) logCapacityLimit(kind, plane, reason, remote string, current, limit int) {
	now := time.Now()
	if s.capacityLogNow != nil {
		now = s.capacityLogNow()
	}
	key := fmt.Sprintf("%s|%s|%s", kind, plane, reason)

	s.capacityLogMu.Lock()
	last := s.capacityLogLast[key]
	elapsed := now.Sub(last)
	if !last.IsZero() && elapsed >= 0 && elapsed < capacityLogInterval {
		s.capacityLogDrop[key]++
		s.capacityLogMu.Unlock()
		return
	}
	suppressed := s.capacityLogDrop[key]
	s.capacityLogLast[key] = now
	s.capacityLogDrop[key] = 0
	s.capacityLogMu.Unlock()

	attrs := []any{
		"kind", kind,
		"reason", reason,
		"remote", remote,
		"current", current,
		"limit", limit,
		"suppressed", suppressed,
	}
	if plane != "" {
		attrs = append(attrs, "plane", plane)
	}
	slog.Warn("capacity limit reached", attrs...)
}
