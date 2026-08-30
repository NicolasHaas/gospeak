package server

import (
	"encoding/json"
	"log/slog"
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

	// Voice counters
	VoicePacketsIn      atomic.Int64 // total UDP voice packets received
	VoicePacketsOut     atomic.Int64 // total UDP voice packets forwarded
	VoicePacketsDropped atomic.Int64 // dropped packets (muted, spoofed, unknown)
	VoiceBytesIn        atomic.Int64 // total voice bytes received
	VoiceBytesOut       atomic.Int64 // total voice bytes forwarded

	// Chat counters
	ChatMessagesSent atomic.Int64 // total chat messages relayed

	// Screen sharing counters
	ScreenSharesStarted    atomic.Int64 // total screen shares started
	ScreenSharesStopped    atomic.Int64 // total screen shares stopped
	ScreenShareFramesIn    atomic.Int64 // total screen share frames received from sharers
	ScreenShareFramesOut   atomic.Int64 // total screen share frames forwarded to viewers
	ScreenShareBytesIn     atomic.Int64 // total screen share bytes received
	ScreenShareBytesOut    atomic.Int64 // total screen share bytes forwarded
	ScreenShareSubscribers atomic.Int64 // current active subscribers across all shares

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

// Snapshot returns a point-in-time view of all metrics as a serializable struct.
type MetricsSnapshot struct {
	Uptime        string `json:"uptime"`
	UptimeSeconds int64  `json:"uptime_seconds"`

	ActiveConnections int64 `json:"active_connections"`
	TotalConnections  int64 `json:"total_connections"`
	SuccessfulAuths   int64 `json:"successful_auths"`
	FailedAuths       int64 `json:"failed_auths"`
	TotalDisconnects  int64 `json:"total_disconnects"`

	PreAuthControlGlobalRejections    int64 `json:"preauth_control_global_rejections"`
	PreAuthControlSourceRejections    int64 `json:"preauth_control_source_rejections"`
	PreAuthScreenGlobalRejections     int64 `json:"preauth_screen_global_rejections"`
	PreAuthScreenSourceRejections     int64 `json:"preauth_screen_source_rejections"`
	PreAuthControlHighWater           int64 `json:"preauth_control_high_water"`
	PreAuthControlSourceHighWater     int64 `json:"preauth_control_source_high_water"`
	PreAuthScreenHighWater            int64 `json:"preauth_screen_high_water"`
	PreAuthScreenSourceHighWater      int64 `json:"preauth_screen_source_high_water"`
	AuthRateLimitSourceRejections     int64 `json:"auth_rate_limit_source_rejections"`
	AuthRateLimitTrackerRejections    int64 `json:"auth_rate_limit_tracker_rejections"`
	AuthRateLimitWindowRejections     int64 `json:"auth_rate_limit_window_rejections"`
	AuthRateLimitSourceHighWater      int64 `json:"auth_rate_limit_source_high_water"`
	AccountProvisionSourceRejections  int64 `json:"account_provision_source_rejections"`
	AccountProvisionTrackerRejections int64 `json:"account_provision_tracker_rejections"`
	AccountProvisionWindowRejections  int64 `json:"account_provision_window_rejections"`
	AccountProvisionSourceHighWater   int64 `json:"account_provision_source_high_water"`

	VoicePacketsIn      int64 `json:"voice_packets_in"`
	VoicePacketsOut     int64 `json:"voice_packets_out"`
	VoicePacketsDropped int64 `json:"voice_packets_dropped"`
	VoiceBytesIn        int64 `json:"voice_bytes_in"`
	VoiceBytesOut       int64 `json:"voice_bytes_out"`

	ChatMessagesSent int64 `json:"chat_messages_sent"`

	ScreenSharesStarted    int64 `json:"screen_shares_started"`
	ScreenSharesStopped    int64 `json:"screen_shares_stopped"`
	ScreenShareFramesIn    int64 `json:"screen_share_frames_in"`
	ScreenShareFramesOut   int64 `json:"screen_share_frames_out"`
	ScreenShareBytesIn     int64 `json:"screen_share_bytes_in"`
	ScreenShareBytesOut    int64 `json:"screen_share_bytes_out"`
	ScreenShareSubscribers int64 `json:"screen_share_subscribers"`

	ChannelsCreated int64 `json:"channels_created"`
	ChannelsDeleted int64 `json:"channels_deleted"`

	TokensCreated int64 `json:"tokens_created"`
	KickCount     int64 `json:"kick_count"`
	BanCount      int64 `json:"ban_count"`
}

// Snapshot returns a read-consistent snapshot of all metrics.
func (m *Metrics) Snapshot() MetricsSnapshot {
	uptime := time.Since(m.startTime)
	return MetricsSnapshot{
		Uptime:                            uptime.Truncate(time.Second).String(),
		UptimeSeconds:                     int64(uptime.Seconds()),
		ActiveConnections:                 m.ActiveConnections.Load(),
		TotalConnections:                  m.TotalConnections.Load(),
		SuccessfulAuths:                   m.SuccessfulAuths.Load(),
		FailedAuths:                       m.FailedAuths.Load(),
		TotalDisconnects:                  m.TotalDisconnects.Load(),
		PreAuthControlGlobalRejections:    m.PreAuthControlGlobalRejections.Load(),
		PreAuthControlSourceRejections:    m.PreAuthControlSourceRejections.Load(),
		PreAuthScreenGlobalRejections:     m.PreAuthScreenGlobalRejections.Load(),
		PreAuthScreenSourceRejections:     m.PreAuthScreenSourceRejections.Load(),
		PreAuthControlHighWater:           m.PreAuthControlHighWater.Load(),
		PreAuthControlSourceHighWater:     m.PreAuthControlSourceHighWater.Load(),
		PreAuthScreenHighWater:            m.PreAuthScreenHighWater.Load(),
		PreAuthScreenSourceHighWater:      m.PreAuthScreenSourceHighWater.Load(),
		AuthRateLimitSourceRejections:     m.AuthRateLimitSourceRejections.Load(),
		AuthRateLimitTrackerRejections:    m.AuthRateLimitTrackerRejections.Load(),
		AuthRateLimitWindowRejections:     m.AuthRateLimitWindowRejections.Load(),
		AuthRateLimitSourceHighWater:      m.AuthRateLimitSourceHighWater.Load(),
		AccountProvisionSourceRejections:  m.AccountProvisionSourceRejections.Load(),
		AccountProvisionTrackerRejections: m.AccountProvisionTrackerRejections.Load(),
		AccountProvisionWindowRejections:  m.AccountProvisionWindowRejections.Load(),
		AccountProvisionSourceHighWater:   m.AccountProvisionSourceHighWater.Load(),
		VoicePacketsIn:                    m.VoicePacketsIn.Load(),
		VoicePacketsOut:                   m.VoicePacketsOut.Load(),
		VoicePacketsDropped:               m.VoicePacketsDropped.Load(),
		VoiceBytesIn:                      m.VoiceBytesIn.Load(),
		VoiceBytesOut:                     m.VoiceBytesOut.Load(),
		ChatMessagesSent:                  m.ChatMessagesSent.Load(),
		ScreenSharesStarted:               m.ScreenSharesStarted.Load(),
		ScreenSharesStopped:               m.ScreenSharesStopped.Load(),
		ScreenShareFramesIn:               m.ScreenShareFramesIn.Load(),
		ScreenShareFramesOut:              m.ScreenShareFramesOut.Load(),
		ScreenShareBytesIn:                m.ScreenShareBytesIn.Load(),
		ScreenShareBytesOut:               m.ScreenShareBytesOut.Load(),
		ScreenShareSubscribers:            m.ScreenShareSubscribers.Load(),
		ChannelsCreated:                   m.ChannelsCreated.Load(),
		ChannelsDeleted:                   m.ChannelsDeleted.Load(),
		TokensCreated:                     m.TokensCreated.Load(),
		KickCount:                         m.KickCount.Load(),
		BanCount:                          m.BanCount.Load(),
	}
}

// JSON returns the metrics snapshot as a JSON string.
func (m *Metrics) JSON() string {
	data, err := json.MarshalIndent(m.Snapshot(), "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

// LogSummary writes a periodic metrics summary to the logger.
func (m *Metrics) LogSummary() {
	s := m.Snapshot()
	slog.Info("metrics",
		"uptime", s.Uptime,
		"connections", s.ActiveConnections,
		"total_connections", s.TotalConnections,
		"voice_pkts_in", s.VoicePacketsIn,
		"voice_pkts_out", s.VoicePacketsOut,
		"voice_pkts_dropped", s.VoicePacketsDropped,
		"chat_msgs", s.ChatMessagesSent,
		"screen_shares_started", s.ScreenSharesStarted,
		"screen_share_frames_out", s.ScreenShareFramesOut,
		"screen_share_subscribers", s.ScreenShareSubscribers,
	)
}

// StartPeriodicLog starts a goroutine that logs metrics every interval.
// It stops when the done channel is closed.
func (m *Metrics) StartPeriodicLog(interval time.Duration, done <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				m.LogSummary()
			}
		}
	}()
}
