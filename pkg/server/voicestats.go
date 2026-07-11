package server

import (
	"log/slog"
	"sync/atomic"
	"time"
)

// perSessionVoiceStat tracks voice packet counts for a single session
// over a debug interval. All fields are atomics so the voice loop and
// the debug logger can access them without a shared mutex.
type perSessionVoiceStat struct {
	packetsReceived  atomic.Int64
	packetsForwarded atomic.Int64
	lastActivity     atomic.Int64 // unix nanos
}

func (s *Server) getOrCreateStat(sessionID uint32) *perSessionVoiceStat {
	s.voiceStatsMu.Lock()
	defer s.voiceStatsMu.Unlock()

	stat, ok := s.voiceStats[sessionID]
	if !ok {
		stat = &perSessionVoiceStat{}
		s.voiceStats[sessionID] = stat
	}
	return stat
}

// removeVoiceStat deletes a session's counters. Called on disconnect so the
// map does not grow unbounded on long-running servers.
func (s *Server) removeVoiceStat(sessionID uint32) {
	s.voiceStatsMu.Lock()
	defer s.voiceStatsMu.Unlock()
	delete(s.voiceStats, sessionID)
}

// resetVoiceStats zeroes all per-session counters. It keeps the map
// entries so they are reused across intervals.
func (s *Server) resetVoiceStats() {
	s.voiceStatsMu.Lock()
	defer s.voiceStatsMu.Unlock()

	for _, stat := range s.voiceStats {
		stat.packetsReceived.Store(0)
		stat.packetsForwarded.Store(0)
		stat.lastActivity.Store(0)
	}
}

// LogVoiceDebug logs per-session voice activity for the last interval.
// It is intended to be called every 10 seconds.
func (s *Server) LogVoiceDebug() {
	s.voiceStatsMu.Lock()
	stats := make(map[uint32]*perSessionVoiceStat, len(s.voiceStats))
	for id, st := range s.voiceStats {
		cp := &perSessionVoiceStat{}
		cp.packetsReceived.Store(st.packetsReceived.Load())
		cp.packetsForwarded.Store(st.packetsForwarded.Load())
		cp.lastActivity.Store(st.lastActivity.Load())
		stats[id] = cp
	}
	s.voiceStatsMu.Unlock()

	if len(stats) == 0 {
		return
	}

	var activeSessions, silentSessions, totalRecv, totalFwd int

	for sid, stat := range stats {
		recv := stat.packetsReceived.Load()
		fwd := stat.packetsForwarded.Load()
		totalRecv += int(recv)
		totalFwd += int(fwd)

		snap, ok := s.sessions.GetSnapshot(sid)
		if !ok {
			continue
		}

		if recv > 0 {
			activeSessions++
		} else {
			silentSessions++
		}

		hints := ""
		if recv == 0 && snap.Muted {
			hints = "no packets sent, user is muted"
		} else if recv == 0 {
			hints = "no packets sent, possibly not in a channel or mic inactive"
		} else if recv > 0 && fwd == 0 {
			hints = "packets not forwarded, no other listener in channel"
		}

		attrs := []any{
			"session", sid,
			"user", snap.Username,
			"channel", snap.ChannelID,
			"pkts_recv", recv,
			"pkts_fwd", fwd,
			"muted", snap.Muted,
			"deafened", snap.Deafened,
		}
		if hints != "" {
			attrs = append(attrs, "hint", hints)
		}
		slog.Debug("voice_debug session", attrs...)
	}

	slog.Debug("voice_debug summary",
		"active_sessions", activeSessions,
		"silent_sessions", silentSessions,
		"total_pkts_recv", totalRecv,
		"total_pkts_fwd", totalFwd,
	)

	s.resetVoiceStats()
}

// startVoiceDebugLogging runs LogVoiceDebug every interval until the
// server context is cancelled.
func (s *Server) startVoiceDebugLogging(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.LogVoiceDebug()
			}
		}
	}()
}
