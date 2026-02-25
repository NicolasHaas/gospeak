package server

import (
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

// Run starts the server and blocks until shutdown signal.
func (s *Server) Run() error {
	if s.store == nil {
		return fmt.Errorf("server: missing store dependency")
	}
	st := s.store
	defer func() { _ = st.NonTx().Close() }()

	// Generate shared voice encryption key
	voiceKey, err := crypto.GenerateKey()
	if err != nil {
		return fmt.Errorf("server: generate voice key: %w", err)
	}
	s.voiceKey = voiceKey

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

	slog.Info("GoSpeak server running",
		"control", s.cfg.ControlAddr,
		"voice", s.cfg.VoiceAddr,
	)

	// Start Prometheus metrics HTTP endpoint
	s.StartMetricsHTTP()

	// Start periodic metrics logging (every 60s)
	s.metrics.StartPeriodicLog(60*time.Second, s.ctx.Done())

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.Info("shutting down...")
	s.Shutdown()
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown() {
	s.cancel()
	if s.controlConn != nil {
		_ = s.controlConn.Close()
	}
	if s.voiceConn != nil {
		_ = s.voiceConn.Close()
	}
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
