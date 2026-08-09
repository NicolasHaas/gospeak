package server

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// StartMetricsHTTP starts the metrics endpoint and logs startup failures.
// Run uses startMetricsHTTP directly so startup failures can be returned.
func (s *Server) StartMetricsHTTP() {
	if err := s.startMetricsHTTP(); err != nil {
		slog.Error("start metrics HTTP", "err", err)
	}
}

func (s *Server) startMetricsHTTP() error {
	if !s.beginTask() {
		return fmt.Errorf("server: start metrics: %w", s.ctx.Err())
	}
	defer s.endTask()

	addr := s.cfg.MetricsAddr
	if addr == "" {
		return nil // metrics endpoint disabled
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           s.trackHTTPHandler(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(s.ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("server: listen metrics: %w", err)
	}

	s.metricsMu.Lock()
	if err := s.ctx.Err(); err != nil {
		s.metricsMu.Unlock()
		_ = ln.Close()
		return fmt.Errorf("server: start metrics: %w", err)
	}
	if s.metricsHTTP != nil {
		s.metricsMu.Unlock()
		_ = ln.Close()
		return fmt.Errorf("server: metrics already started")
	}
	s.metricsHTTP = srv
	s.metricsConn = ln
	s.metricsMu.Unlock()

	if !s.startWorker(func() {
		slog.Info("metrics HTTP listening", "addr", ln.Addr().String())
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("metrics HTTP error", "err", err)
		}
	}) {
		s.metricsMu.Lock()
		s.metricsHTTP = nil
		s.metricsConn = nil
		s.metricsMu.Unlock()
		_ = ln.Close()
		return fmt.Errorf("server: start metrics worker: %w", s.ctx.Err())
	}
	return nil
}

// handleMetrics writes all metrics in Prometheus text exposition format.
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	m := s.metrics
	uptime := time.Since(m.startTime).Seconds()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	// Helper for gauge/counter lines.
	// Write errors to http.ResponseWriter are non-actionable; suppress errcheck.
	write := func(name, help, mtype string, value int64) {
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n", name, help)
		_, _ = fmt.Fprintf(w, "# TYPE %s %s\n", name, mtype)
		_, _ = fmt.Fprintf(w, "%s %d\n", name, value)
	}
	writeFloat := func(name, help, mtype string, value float64) {
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n", name, help)
		_, _ = fmt.Fprintf(w, "# TYPE %s %s\n", name, mtype)
		_, _ = fmt.Fprintf(w, "%s %f\n", name, value)
	}

	writeFloat("gospeak_uptime_seconds", "Server uptime in seconds.", "gauge", uptime)

	write("gospeak_connections_active", "Current active control connections.", "gauge",
		m.ActiveConnections.Load())
	write("gospeak_connections_total", "Lifetime TCP control connections accepted.", "counter",
		m.TotalConnections.Load())
	write("gospeak_disconnects_total", "Total client disconnects.", "counter",
		m.TotalDisconnects.Load())

	write("gospeak_auth_success_total", "Successful authentication attempts.", "counter",
		m.SuccessfulAuths.Load())
	write("gospeak_auth_failed_total", "Failed authentication attempts.", "counter",
		m.FailedAuths.Load())

	write("gospeak_voice_packets_in_total", "Total UDP voice packets received.", "counter",
		m.VoicePacketsIn.Load())
	write("gospeak_voice_packets_out_total", "Total UDP voice packets forwarded.", "counter",
		m.VoicePacketsOut.Load())
	write("gospeak_voice_packets_dropped_total", "Dropped voice packets.", "counter",
		m.VoicePacketsDropped.Load())
	write("gospeak_voice_bytes_in_total", "Total voice bytes received.", "counter",
		m.VoiceBytesIn.Load())
	write("gospeak_voice_bytes_out_total", "Total voice bytes forwarded.", "counter",
		m.VoiceBytesOut.Load())

	write("gospeak_chat_messages_total", "Total chat messages relayed.", "counter",
		m.ChatMessagesSent.Load())

	write("gospeak_screen_shares_started_total", "Screen shares started.", "counter",
		m.ScreenSharesStarted.Load())
	write("gospeak_screen_shares_stopped_total", "Screen shares stopped.", "counter",
		m.ScreenSharesStopped.Load())
	write("gospeak_screen_share_frames_in_total", "Screen share frames received from sharers.", "counter",
		m.ScreenShareFramesIn.Load())
	write("gospeak_screen_share_frames_out_total", "Screen share frames forwarded to viewers.", "counter",
		m.ScreenShareFramesOut.Load())
	write("gospeak_screen_share_bytes_in_total", "Screen share bytes received from sharers.", "counter",
		m.ScreenShareBytesIn.Load())
	write("gospeak_screen_share_bytes_out_total", "Screen share bytes forwarded to viewers.", "counter",
		m.ScreenShareBytesOut.Load())
	write("gospeak_screen_share_subscribers", "Current active screen share subscribers.", "gauge",
		m.ScreenShareSubscribers.Load())

	write("gospeak_channels_created_total", "Channels created.", "counter",
		m.ChannelsCreated.Load())
	write("gospeak_channels_deleted_total", "Channels deleted.", "counter",
		m.ChannelsDeleted.Load())

	write("gospeak_tokens_created_total", "Invite tokens created.", "counter",
		m.TokensCreated.Load())
	write("gospeak_kicks_total", "Users kicked.", "counter",
		m.KickCount.Load())
	write("gospeak_bans_total", "Users banned.", "counter",
		m.BanCount.Load())
}
