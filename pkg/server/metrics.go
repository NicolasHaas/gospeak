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

	// Voice counters
	VoicePacketsIn      atomic.Int64 // total UDP voice packets received
	VoicePacketsOut     atomic.Int64 // total UDP voice packets forwarded
	VoicePacketsDropped atomic.Int64 // dropped packets (muted, spoofed, unknown)
	VoiceBytesIn        atomic.Int64 // total voice bytes received
	VoiceBytesOut       atomic.Int64 // total voice bytes forwarded

	// Chat counters
	ChatMessagesSent atomic.Int64 // total chat messages relayed

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

	VoicePacketsIn      int64 `json:"voice_packets_in"`
	VoicePacketsOut     int64 `json:"voice_packets_out"`
	VoicePacketsDropped int64 `json:"voice_packets_dropped"`
	VoiceBytesIn        int64 `json:"voice_bytes_in"`
	VoiceBytesOut       int64 `json:"voice_bytes_out"`

	ChatMessagesSent int64 `json:"chat_messages_sent"`

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
		Uptime:              uptime.Truncate(time.Second).String(),
		UptimeSeconds:       int64(uptime.Seconds()),
		ActiveConnections:   m.ActiveConnections.Load(),
		TotalConnections:    m.TotalConnections.Load(),
		SuccessfulAuths:     m.SuccessfulAuths.Load(),
		FailedAuths:         m.FailedAuths.Load(),
		TotalDisconnects:    m.TotalDisconnects.Load(),
		VoicePacketsIn:      m.VoicePacketsIn.Load(),
		VoicePacketsOut:     m.VoicePacketsOut.Load(),
		VoicePacketsDropped: m.VoicePacketsDropped.Load(),
		VoiceBytesIn:        m.VoiceBytesIn.Load(),
		VoiceBytesOut:       m.VoiceBytesOut.Load(),
		ChatMessagesSent:    m.ChatMessagesSent.Load(),
		ChannelsCreated:     m.ChannelsCreated.Load(),
		ChannelsDeleted:     m.ChannelsDeleted.Load(),
		TokensCreated:       m.TokensCreated.Load(),
		KickCount:           m.KickCount.Load(),
		BanCount:            m.BanCount.Load(),
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
