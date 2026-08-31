package server

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

var errControlUserBudgetCapacity = errors.New("server: control user budget capacity reached")

const (
	controlMutationCost       = 1
	controlChatCost           = 2
	controlExpensiveCost      = 5
	controlBytesPerCost       = 16 * 1024
	maxControlUserBudgetCount = 4096
)

type controlBudgetDecision struct {
	Allowed   bool
	Reason    string
	Cost      int
	Remaining float64
	Limit     int
	Usage     int
}

type controlMessageLimiter struct {
	mu        sync.Mutex
	capacity  int
	refill    float64
	tokens    float64
	updated   time.Time
	now       func() time.Time
	highWater *atomic.Int64
}

func newControlMessageLimiter(capacity, refillPerSecond int, now func() time.Time) *controlMessageLimiter {
	if now == nil {
		now = time.Now
	}
	current := now()
	return &controlMessageLimiter{
		capacity: capacity,
		refill:   float64(refillPerSecond),
		tokens:   float64(capacity),
		updated:  current,
		now:      now,
	}
}

func (rl *controlMessageLimiter) Allow(message *pb.ControlMessage) controlBudgetDecision {
	return rl.AllowSized(message, 0)
}

func (rl *controlMessageLimiter) AllowSized(message *pb.ControlMessage, payloadBytes int) controlBudgetDecision {
	reason, cost := controlMessageCost(message)
	if byteCost := controlMessageByteCost(payloadBytes); byteCost > cost {
		reason = "bytes"
		cost = byteCost
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.refillLocked()

	decision := controlBudgetDecision{Reason: reason, Cost: cost, Remaining: rl.tokens, Limit: rl.capacity}
	if rl.tokens < float64(cost) {
		decision.Usage = int(math.Ceil(float64(rl.capacity) - rl.tokens))
		return decision
	}
	rl.tokens -= float64(cost)
	decision.Allowed = true
	decision.Remaining = rl.tokens
	decision.Usage = int(math.Ceil(float64(rl.capacity) - rl.tokens))
	if rl.highWater != nil {
		storeHighWater(rl.highWater, decision.Usage)
	}
	return decision
}

func (rl *controlMessageLimiter) usage() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.refillLocked()
	return int(math.Ceil(float64(rl.capacity) - rl.tokens))
}

func (rl *controlMessageLimiter) full() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.refillLocked()
	return rl.tokens >= float64(rl.capacity)
}

func (rl *controlMessageLimiter) refillLocked() {
	now := rl.now()
	if elapsed := now.Sub(rl.updated).Seconds(); elapsed > 0 {
		rl.tokens = min(float64(rl.capacity), rl.tokens+elapsed*rl.refill)
		rl.updated = now
	}
}

func controlMessageByteCost(payloadBytes int) int {
	if payloadBytes <= 0 {
		return 1
	}
	return (payloadBytes + controlBytesPerCost - 1) / controlBytesPerCost
}

func controlMessageCost(message *pb.ControlMessage) (string, int) {
	if message == nil {
		return "mutation", controlMutationCost
	}
	switch {
	case message.ChannelListRequest != nil,
		message.JoinChannelRequest != nil,
		message.LeaveChannelRequest != nil,
		message.UserStateUpdate != nil,
		message.CreateChannelReq != nil,
		message.DeleteChannelReq != nil,
		message.CreateTokenReq != nil,
		message.KickUserReq != nil,
		message.BanUserReq != nil,
		message.SetUserRoleReq != nil,
		message.ExportDataReq != nil,
		message.ImportChannelsReq != nil,
		message.ScreenShareStartReq != nil,
		message.ScreenShareStopReq != nil,
		message.ScreenShareSubReq != nil,
		message.ScreenShareShareReq != nil:
		return "expensive", controlExpensiveCost
	case message.ChatMsg != nil:
		return "chat", controlChatCost
	default:
		return "mutation", controlMutationCost
	}
}

type controlUserBudgetEntry struct {
	limiter *controlMessageLimiter
	active  int
}

type controlUserBudgetManager struct {
	mu         sync.Mutex
	entries    map[int64]*controlUserBudgetEntry
	capacity   int
	refill     int
	maxEntries int
	now        func() time.Time
	highWater  *atomic.Int64
}

func newControlUserBudgetManager(capacity, refill int, now func() time.Time) *controlUserBudgetManager {
	if now == nil {
		now = time.Now
	}
	return &controlUserBudgetManager{
		entries:    make(map[int64]*controlUserBudgetEntry),
		capacity:   capacity,
		refill:     refill,
		maxEntries: maxControlUserBudgetCount,
		now:        now,
	}
}

func (manager *controlUserBudgetManager) Acquire(userID int64) (*controlMessageLimiter, int, int, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	manager.pruneLocked()
	if entry := manager.entries[userID]; entry != nil {
		entry.active++
		return entry.limiter, len(manager.entries), manager.maxEntries, true
	}
	if len(manager.entries) >= manager.maxEntries {
		return nil, len(manager.entries), manager.maxEntries, false
	}
	limiter := newControlMessageLimiter(manager.capacity, manager.refill, manager.now)
	limiter.highWater = manager.highWater
	manager.entries[userID] = &controlUserBudgetEntry{limiter: limiter, active: 1}
	return limiter, len(manager.entries), manager.maxEntries, true
}

func (manager *controlUserBudgetManager) Release(userID int64) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	entry := manager.entries[userID]
	if entry == nil {
		return
	}
	if entry.active > 0 {
		entry.active--
	}
	if entry.active == 0 && entry.limiter.full() {
		delete(manager.entries, userID)
	}
}

func (manager *controlUserBudgetManager) snapshot() (tracked, active, maxUsage, limit int) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.pruneLocked()
	for _, entry := range manager.entries {
		if entry.active > 0 {
			active++
		}
		if usage := entry.limiter.usage(); usage > maxUsage {
			maxUsage = usage
		}
	}
	return len(manager.entries), active, maxUsage, manager.maxEntries
}

func (manager *controlUserBudgetManager) pruneLocked() {
	for userID, entry := range manager.entries {
		if entry.active == 0 && entry.limiter.full() {
			delete(manager.entries, userID)
		}
	}
}
