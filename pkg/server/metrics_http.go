package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// StartMetricsHTTP starts a lightweight HTTP server that exposes /metrics
// in Prometheus text exposition format. It runs in the background and
// shuts down when the server context is cancelled.
//
// Bind address is :9602 by default — configurable via Config.MetricsAddr.
func (s *Server) StartMetricsHTTP() {
	addr := s.cfg.MetricsAddr
	if addr == "" {
		return // metrics endpoint disabled
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("metrics HTTP listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("metrics HTTP error", "err", err)
		}
	}()

	go func() {
		<-s.ctx.Done()
		_ = srv.Close()
	}()
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
