package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
)

const metricsShutdownTimeout = 5 * time.Second

// Run starts the server and blocks until shutdown signal.
func (s *Server) Run() error {
	if s.store == nil {
		return fmt.Errorf("server: missing store dependency")
	}
	defer s.Shutdown()
	st := s.store

	// Generate shared voice encryption key
	voiceKey, err := crypto.GenerateKey()
	if err != nil {
		return fmt.Errorf("server: generate voice key: %w", err)
	}
	s.voiceKey = voiceKey

	// Enable voice debug counters before listeners start so voiceLoop reads a
	// stable value (avoids a data race with the assignment below).
	s.voiceDebugEnabled = slog.Default().Enabled(context.Background(), slog.LevelDebug)

	// Ensure default "Lobby" channel exists
	channels, _ := st.NonTx().ListChannels()
	if len(channels) == 0 {
		if err := st.NonTx().CreateChannel(model.NewChannel()); err != nil {
			return fmt.Errorf("server: create lobby: %w", err)
		}
		slog.Info("created default Lobby channel")
	}

	// Load channels from YAML config if provided
	if s.cfg.ChannelsFile != "" {
		if err := LoadChannelsFromYAML(s.cfg.ChannelsFile, st); err != nil {
			slog.Error("failed to load channels config", "err", err)
		}
	}

	// Ensure at least one admin token exists
	if err := s.ensureAdminToken(st); err != nil {
		return err
	}

	// Start listeners
	if err := s.StartControl(st); err != nil {
		return err
	}
	if err := s.StartVoice(); err != nil {
		return err
	}
	if s.cfg.EnableScreenShare {
		if err := s.StartScreen(); err != nil {
			return err
		}
	}

	if s.cfg.MetricsAddr != "" {
		// Start Prometheus metrics HTTP endpoint
		if err := s.startMetricsHTTP(); err != nil {
			return err
		}
	}

	slog.Info("GoSpeak server running",
		"control", s.cfg.ControlAddr,
		"voice", s.cfg.VoiceAddr,
		"screen", s.cfg.ScreenAddr,
	)

	// Start periodic voice debug logging (only when log level is debug)
	if s.voiceDebugEnabled {
		s.startVoiceDebugLogging(10 * time.Second)
	}

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	select {
	case <-sigCh:
		slog.Info("shutting down...")
	case <-s.ctx.Done():
	}
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown() {
	s.shutdownOnce.Do(func() {
		s.workerMu.Lock()
		s.stopping = true
		s.cancel()
		s.workerMu.Unlock()

		s.metricsMu.Lock()
		metricsHTTP := s.metricsHTTP
		metricsConn := s.metricsConn
		s.metricsHTTP = nil
		s.metricsConn = nil
		s.metricsMu.Unlock()
		if metricsHTTP != nil {
			ctx, cancel := context.WithTimeout(context.Background(), metricsShutdownTimeout)
			if err := metricsHTTP.Shutdown(ctx); err != nil {
				_ = metricsHTTP.Close()
			}
			cancel()
		}
		if metricsConn != nil {
			_ = metricsConn.Close()
		}

		s.listenerMu.Lock()
		screenConn := s.screenConn
		voiceConn := s.voiceConn
		controlConn := s.controlConn
		s.listenerMu.Unlock()
		if screenConn != nil {
			_ = screenConn.Close()
		}
		if voiceConn != nil {
			_ = voiceConn.Close()
		}
		if controlConn != nil {
			_ = controlConn.Close()
		}
		s.closeAcceptedConns()
		s.closeScreenConns()
		s.workers.Wait()
	})
}

// ensureAdminToken creates an admin token only on first run (no tokens exist).
func (s *Server) ensureAdminToken(st datastore.DataProviderFactory) error {
	hasTokens, err := st.NonTx().HasTokens()
	if err != nil {
		return fmt.Errorf("server: check tokens: %w", err)
	}
	if hasTokens {
		return nil // tokens already exist, don't generate more
	}

	rawToken, err := crypto.GenerateToken()
	if err != nil {
		return fmt.Errorf("server: generate admin token: %w", err)
	}

	hash := crypto.HashToken(rawToken)
	if err := st.NonTx().CreateToken(hash, model.RoleAdmin, 0, 0, 0 /* unlimited uses, no expiry */, st.NonTx().ZeroTime()); err != nil {
		return fmt.Errorf("server: store admin token: %w", err)
	}

	slog.Info("========================================")
	slog.Info("ADMIN TOKEN (save this!):", "token", rawToken)
	slog.Info("========================================")
	return nil
}
