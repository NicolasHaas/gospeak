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
	voiceCipher, err := crypto.NewVoiceCipher(voiceKey)
	if err != nil {
		return fmt.Errorf("server: initialize voice cipher: %w", err)
	}
	s.voiceKey = voiceKey
	s.voiceCipher = voiceCipher

	// Enable voice debug counters before listeners start so voiceLoop reads a
	// stable value (avoids a data race with the assignment below).
	s.voiceDebugEnabled = slog.Default().Enabled(context.Background(), slog.LevelDebug)

	var channelConfig *ChannelsConfig
	if s.cfg.ChannelsFile != "" {
		data, err := readChannelsYAML(s.cfg.ChannelsFile)
		if err != nil {
			return fmt.Errorf("server: load channels config: %w", err)
		}
		cfg, err := parseChannelsYAML(data)
		if err != nil {
			return fmt.Errorf("server: load channels config: %w", err)
		}
		channelConfig = &cfg
	}

	if err := initializeChannels(channelConfig, st); err != nil {
		return fmt.Errorf("server: initialize channels: %w", err)
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

	slog.Info("GoSpeak server running", s.runningLogArgs()...)

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

func (s *Server) runningLogArgs() []any {
	args := []any{
		"control", s.cfg.ControlAddr,
		"voice", s.cfg.VoiceAddr,
	}
	if s.cfg.EnableScreenShare {
		args = append(args, "screen", s.cfg.ScreenAddr)
	}
	return args
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
