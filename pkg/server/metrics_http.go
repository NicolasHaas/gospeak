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
	preAuth := s.preAuthCapacitySnapshot()
	authUsage := s.authLimiter.usageSnapshot()
	provisionUsage := s.accountProvisionLimiter.usageSnapshot()
	sessionUsage := s.sessions.CapacitySnapshot()
	controlUsage := s.controlBudgetSnapshot()
	authHighWater := max(m.AuthRateLimitSourceHighWater.Load(), int64(authUsage.maxUsage))
	provisionHighWater := max(m.AccountProvisionSourceHighWater.Load(), int64(provisionUsage.maxUsage))

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
	writeLabeled := func(name, labels string, value int64) {
		_, _ = fmt.Fprintf(w, "%s{%s} %d\n", name, labels, value)
	}
	writeHeader := func(name, help, mtype string) {
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n", name, help)
		_, _ = fmt.Fprintf(w, "# TYPE %s %s\n", name, mtype)
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

	writeHeader("gospeak_preauth_connections", "Current unauthenticated TCP connections by plane.", "gauge")
	writeHeader("gospeak_preauth_connections_high_water", "Highest unauthenticated connection occupancy since server start by plane.", "gauge")
	writeHeader("gospeak_preauth_connection_limit", "Configured global unauthenticated connection limit by plane.", "gauge")
	writeHeader("gospeak_preauth_active_sources", "Current source keys with unauthenticated connections by plane.", "gauge")
	writeHeader("gospeak_preauth_source_max_connections", "Current highest unauthenticated connection occupancy for one source key by plane.", "gauge")
	writeHeader("gospeak_preauth_source_max_connections_high_water", "Highest single-source unauthenticated connection occupancy since server start by plane.", "gauge")
	writeHeader("gospeak_preauth_source_connection_limit", "Configured unauthenticated connection limit for one source key by plane.", "gauge")
	writeHeader("gospeak_preauth_rejections_total", "Unauthenticated connections rejected at a capacity limit.", "counter")
	for _, plane := range []preAuthPlane{preAuthControl, preAuthScreen} {
		planeLabel := fmt.Sprintf(`plane=%q`, plane)
		writeLabeled("gospeak_preauth_connections", planeLabel, int64(preAuth.current[plane]))
		writeLabeled("gospeak_preauth_connection_limit", planeLabel, int64(s.cfg.MaxPreAuthConnections))
		writeLabeled("gospeak_preauth_active_sources", planeLabel, int64(preAuth.activeSources[plane]))
		writeLabeled("gospeak_preauth_source_max_connections", planeLabel, int64(preAuth.maxBySource[plane]))
		writeLabeled("gospeak_preauth_source_connection_limit", planeLabel, maxPreAuthConnectionsPerIP)

		var globalRejections, sourceRejections, globalHighWater, sourceHighWater int64
		if plane == preAuthControl {
			globalRejections = m.PreAuthControlGlobalRejections.Load()
			sourceRejections = m.PreAuthControlSourceRejections.Load()
			globalHighWater = m.PreAuthControlHighWater.Load()
			sourceHighWater = m.PreAuthControlSourceHighWater.Load()
		} else {
			globalRejections = m.PreAuthScreenGlobalRejections.Load()
			sourceRejections = m.PreAuthScreenSourceRejections.Load()
			globalHighWater = m.PreAuthScreenHighWater.Load()
			sourceHighWater = m.PreAuthScreenSourceHighWater.Load()
		}
		writeLabeled("gospeak_preauth_connections_high_water", planeLabel, globalHighWater)
		writeLabeled("gospeak_preauth_source_max_connections_high_water", planeLabel, sourceHighWater)
		writeLabeled("gospeak_preauth_rejections_total", planeLabel+`,reason="global"`, globalRejections)
		writeLabeled("gospeak_preauth_rejections_total", planeLabel+`,reason="source"`, sourceRejections)
	}

	write("gospeak_auth_rate_limit_source_max_usage", "Current highest authentication budget usage for one source key.", "gauge", int64(authUsage.maxUsage))
	write("gospeak_auth_rate_limit_source_high_water_usage", "Highest single-source authentication budget usage since server start.", "gauge", authHighWater)
	write("gospeak_auth_rate_limit_active_sources", "Current source keys tracked by the authentication limiter.", "gauge", int64(authUsage.activeSources))
	write("gospeak_auth_rate_limit_source_limit", "Configured authentication attempt limit for one source key.", "gauge", int64(authUsage.sourceLimit))
	write("gospeak_auth_rate_limit_tracker_limit", "Configured maximum source keys tracked by the authentication limiter.", "gauge", int64(authUsage.trackerLimit))
	writeHeader("gospeak_auth_rate_limit_rejections_total", "Authentication attempts rejected by the rate limiter.", "counter")
	writeLabeled("gospeak_auth_rate_limit_rejections_total", `reason="source"`, m.AuthRateLimitSourceRejections.Load())
	writeLabeled("gospeak_auth_rate_limit_rejections_total", `reason="tracker_capacity"`, m.AuthRateLimitTrackerRejections.Load())
	writeLabeled("gospeak_auth_rate_limit_rejections_total", `reason="window_transition"`, m.AuthRateLimitWindowRejections.Load())

	write("gospeak_account_provisioning_source_max_usage", "Current highest account provisioning budget usage for one source key.", "gauge", int64(provisionUsage.maxUsage))
	write("gospeak_account_provisioning_source_high_water_usage", "Highest single-source account provisioning budget usage since server start.", "gauge", provisionHighWater)
	write("gospeak_account_provisioning_active_sources", "Current source keys tracked by the account provisioning limiter.", "gauge", int64(provisionUsage.activeSources))
	write("gospeak_account_provisioning_source_limit", "Configured successful account provisioning limit for one source key.", "gauge", int64(provisionUsage.sourceLimit))
	write("gospeak_account_provisioning_tracker_limit", "Configured maximum source keys tracked by the account provisioning limiter.", "gauge", int64(provisionUsage.trackerLimit))
	writeHeader("gospeak_account_provisioning_rejections_total", "Account provisioning attempts rejected by the limiter.", "counter")
	writeLabeled("gospeak_account_provisioning_rejections_total", `reason="source"`, m.AccountProvisionSourceRejections.Load())
	writeLabeled("gospeak_account_provisioning_rejections_total", `reason="tracker_capacity"`, m.AccountProvisionTrackerRejections.Load())
	writeLabeled("gospeak_account_provisioning_rejections_total", `reason="window_transition"`, m.AccountProvisionWindowRejections.Load())

	write("gospeak_sessions", "Current active authenticated sessions.", "gauge", int64(sessionUsage.Active))
	write("gospeak_session_capacity_used", "Current active sessions plus pending authenticated-session capacity claims.", "gauge", int64(sessionUsage.CapacityUsed))
	write("gospeak_session_capacity_high_water", "Highest authenticated-session capacity occupancy since server start, including pending claims.", "gauge", int64(sessionUsage.CapacityHighWater))
	write("gospeak_session_limit", "Configured global authenticated-session capacity.", "gauge", int64(sessionUsage.GlobalLimit))
	write("gospeak_session_user_capacity_max", "Current highest active plus pending session-capacity claims for one user.", "gauge", int64(sessionUsage.MaxUserCapacity))
	write("gospeak_session_user_capacity_high_water", "Highest single-user session-capacity occupancy since server start, including pending claims.", "gauge", int64(sessionUsage.UserCapacityHighWater))
	write("gospeak_session_user_limit", "Configured authenticated-session capacity for one user.", "gauge", int64(sessionUsage.PerUserLimit))
	writeHeader("gospeak_session_rejections_total", "Authenticated-session capacity claims rejected at a limit.", "counter")
	writeLabeled("gospeak_session_rejections_total", `reason="global"`, m.SessionGlobalRejections.Load())
	writeLabeled("gospeak_session_rejections_total", `reason="user"`, m.SessionUserRejections.Load())

	write("gospeak_control_budget_active_sessions", "Current sessions tracked by control-message budgets.", "gauge", int64(controlUsage.activeSessions))
	write("gospeak_control_budget_active_users", "Current users with active control-message budgets.", "gauge", int64(controlUsage.activeUsers))
	write("gospeak_control_budget_tracked_users", "Current active or refill-pending user control-message budgets.", "gauge", int64(controlUsage.trackedUsers))
	write("gospeak_control_budget_session_max_usage", "Current highest control-message budget usage for one session.", "gauge", int64(controlUsage.maxSessionUse))
	write("gospeak_control_budget_user_max_usage", "Current highest aggregate control-message budget usage for one user.", "gauge", int64(controlUsage.maxUserUse))
	write("gospeak_control_budget_global_usage", "Current server-wide control-message budget usage.", "gauge", int64(controlUsage.globalUse))
	write("gospeak_control_budget_session_high_water_usage", "Highest single-session control-message budget usage since server start.", "gauge", max(m.ControlSessionBudgetHighWater.Load(), int64(controlUsage.maxSessionUse)))
	write("gospeak_control_budget_user_high_water_usage", "Highest aggregate single-user control-message budget usage since server start.", "gauge", max(m.ControlUserBudgetHighWater.Load(), int64(controlUsage.maxUserUse)))
	write("gospeak_control_budget_global_high_water_usage", "Highest server-wide control-message budget usage since server start.", "gauge", max(m.ControlGlobalBudgetHighWater.Load(), int64(controlUsage.globalUse)))
	write("gospeak_control_budget_limit", "Configured per-session and per-user control-message burst capacity.", "gauge", int64(s.cfg.ControlMessageBurst))
	write("gospeak_control_budget_refill_per_second", "Configured per-session and per-user control-message cost replenished per second.", "gauge", int64(s.cfg.ControlMessagesPerSec))
	write("gospeak_control_budget_global_limit", "Configured server-wide control-message burst capacity.", "gauge", int64(s.cfg.ControlGlobalBurst))
	write("gospeak_control_budget_global_refill_per_second", "Configured server-wide control-message cost replenished per second.", "gauge", int64(s.cfg.ControlGlobalMessagesPerSec))
	write("gospeak_control_budget_bytes_per_cost", "Encoded control-message bytes charged as one budget point.", "gauge", controlBytesPerCost)
	write("gospeak_control_budget_user_tracker_limit", "Maximum active or refill-pending user control-message budgets retained.", "gauge", int64(controlUsage.userTrackLimit))
	writeHeader("gospeak_control_budget_rejections_total", "Control messages rejected by budget scope and cost class.", "counter")
	writeLabeled("gospeak_control_budget_rejections_total", `scope="session",reason="mutation"`, m.ControlSessionMutationRejections.Load())
	writeLabeled("gospeak_control_budget_rejections_total", `scope="session",reason="chat"`, m.ControlSessionChatRejections.Load())
	writeLabeled("gospeak_control_budget_rejections_total", `scope="session",reason="expensive"`, m.ControlSessionExpensiveRejections.Load())
	writeLabeled("gospeak_control_budget_rejections_total", `scope="session",reason="bytes"`, m.ControlSessionByteRejections.Load())
	writeLabeled("gospeak_control_budget_rejections_total", `scope="user",reason="mutation"`, m.ControlUserMutationRejections.Load())
	writeLabeled("gospeak_control_budget_rejections_total", `scope="user",reason="chat"`, m.ControlUserChatRejections.Load())
	writeLabeled("gospeak_control_budget_rejections_total", `scope="user",reason="expensive"`, m.ControlUserExpensiveRejections.Load())
	writeLabeled("gospeak_control_budget_rejections_total", `scope="user",reason="bytes"`, m.ControlUserByteRejections.Load())
	writeLabeled("gospeak_control_budget_rejections_total", `scope="user",reason="tracker_capacity"`, m.ControlUserTrackerRejections.Load())
	writeLabeled("gospeak_control_budget_rejections_total", `scope="global",reason="mutation"`, m.ControlGlobalMutationRejections.Load())
	writeLabeled("gospeak_control_budget_rejections_total", `scope="global",reason="chat"`, m.ControlGlobalChatRejections.Load())
	writeLabeled("gospeak_control_budget_rejections_total", `scope="global",reason="expensive"`, m.ControlGlobalExpensiveRejections.Load())
	writeLabeled("gospeak_control_budget_rejections_total", `scope="global",reason="bytes"`, m.ControlGlobalByteRejections.Load())
	write("gospeak_control_invalid_messages_total", "Control messages rejected during framing or parsing.", "counter", m.ControlInvalidMessages.Load())

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
	writeHeader("gospeak_screen_auth_rejections_total", "Screen-plane authentication attempts rejected by bounded reason.", "counter")
	writeLabeled("gospeak_screen_auth_rejections_total", `reason="invalid_message"`, m.ScreenAuthInvalidRejections.Load())
	writeLabeled("gospeak_screen_auth_rejections_total", `reason="authentication"`, m.ScreenAuthCredentialRejections.Load())
	write("gospeak_screen_invalid_packets_total", "Authenticated screen-plane packets rejected during framing or parsing.", "counter", m.ScreenInvalidPackets.Load())

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
