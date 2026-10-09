package server

import (
	"sync/atomic"
	"time"
)

// Metrics tracks server runtime statistics.
// All counters use atomic operations for lock-free concurrent access.
type Metrics struct {
	startTime time.Time

	// Connection counters
	TotalConnections  atomic.Int64 // lifetime TCP control connections accepted
	ActiveConnections atomic.Int64 // current active control connections
	FailedAuths       atomic.Int64 // failed authentication attempts
	SuccessfulAuths   atomic.Int64 // successful authentication attempts
	TotalDisconnects  atomic.Int64 // total client disconnects (clean + unclean)

	// Capacity-limit counters and startup-lifetime high-water marks. Current
	// occupancy and configured limits are read from owning server state when
	// Prometheus scrapes.
	PreAuthControlGlobalRejections    atomic.Int64
	PreAuthControlSourceRejections    atomic.Int64
	PreAuthScreenGlobalRejections     atomic.Int64
	PreAuthScreenSourceRejections     atomic.Int64
	PreAuthControlHighWater           atomic.Int64
	PreAuthControlSourceHighWater     atomic.Int64
	PreAuthScreenHighWater            atomic.Int64
	PreAuthScreenSourceHighWater      atomic.Int64
	AuthRateLimitSourceRejections     atomic.Int64
	AuthRateLimitTrackerRejections    atomic.Int64
	AuthRateLimitWindowRejections     atomic.Int64
	AuthRateLimitSourceHighWater      atomic.Int64
	AccountProvisionSourceRejections  atomic.Int64
	AccountProvisionTrackerRejections atomic.Int64
	AccountProvisionWindowRejections  atomic.Int64
	AccountProvisionSourceHighWater   atomic.Int64
	SessionGlobalRejections           atomic.Int64
	SessionUserRejections             atomic.Int64
	ControlSessionMutationRejections  atomic.Int64
	ControlSessionChatRejections      atomic.Int64
	ControlSessionExpensiveRejections atomic.Int64
	ControlSessionByteRejections      atomic.Int64
	ControlUserMutationRejections     atomic.Int64
	ControlUserChatRejections         atomic.Int64
	ControlUserExpensiveRejections    atomic.Int64
	ControlUserByteRejections         atomic.Int64
	ControlUserTrackerRejections      atomic.Int64
	ControlGlobalMutationRejections   atomic.Int64
	ControlGlobalChatRejections       atomic.Int64
	ControlGlobalExpensiveRejections  atomic.Int64
	ControlGlobalByteRejections       atomic.Int64
	ControlInvalidMessages            atomic.Int64
	ControlSessionBudgetHighWater     atomic.Int64
	ControlUserBudgetHighWater        atomic.Int64
	ControlGlobalBudgetHighWater      atomic.Int64

	// Voice counters
	VoicePacketsIn      atomic.Int64 // total UDP voice packets received
	VoicePacketsOut     atomic.Int64 // total UDP voice packets forwarded
	VoicePacketsDropped atomic.Int64 // dropped packets (muted, spoofed, unknown)
	VoiceBytesIn        atomic.Int64 // total voice bytes received
	VoiceBytesOut       atomic.Int64 // total voice bytes forwarded

	// Chat counters
	ChatMessagesSent atomic.Int64 // total chat messages relayed

	// Screen sharing counters
	ScreenSharesStarted            atomic.Int64 // total screen shares started
	ScreenSharesStopped            atomic.Int64 // total screen shares stopped
	ScreenShareFramesIn            atomic.Int64 // total screen share frames received from sharers
	ScreenShareFramesOut           atomic.Int64 // total screen share frames forwarded to viewers
	ScreenShareBytesIn             atomic.Int64 // total screen share bytes received
	ScreenShareBytesOut            atomic.Int64 // total screen share bytes forwarded
	ScreenShareSubscribers         atomic.Int64 // current active subscribers across all shares
	ScreenAuthInvalidRejections    atomic.Int64
	ScreenAuthCredentialRejections atomic.Int64
	ScreenInvalidPackets           atomic.Int64

	// Channel counters
	ChannelsCreated atomic.Int64 // channels created during this run
	ChannelsDeleted atomic.Int64 // channels deleted during this run

	// Admin counters
	TokensCreated atomic.Int64 // invite tokens created
	KickCount     atomic.Int64 // users kicked
	BanCount      atomic.Int64 // users banned
}

// NewMetrics creates a new Metrics instance with the start time set to now.
func NewMetrics() *Metrics {
	return &Metrics{
		startTime: time.Now(),
	}
}
