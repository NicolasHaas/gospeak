package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/logging"
	"github.com/NicolasHaas/gospeak/pkg/server"
)

func main() {
	cfg := server.DefaultConfig()

	flag.StringVar(&cfg.ControlAddr, "control", cfg.ControlAddr, "TCP/TLS control plane bind address")
	flag.StringVar(&cfg.VoiceAddr, "voice", cfg.VoiceAddr, "UDP voice plane bind address")
	flag.StringVar(&cfg.ScreenAddr, "screen", cfg.ScreenAddr, "TCP/TLS screen-share bind address")
	flag.StringVar(&cfg.DBPath, "db", cfg.DBPath, "SQLite database file path")
	flag.StringVar(&cfg.CertFile, "cert", "", "TLS certificate file; requires -key (empty pair enables automatic self-signed mode)")
	flag.StringVar(&cfg.KeyFile, "key", "", "TLS private key file; requires -cert (empty pair enables automatic self-signed mode)")
	flag.StringVar(&cfg.DataDir, "data", ".", "Data directory for generated files")
	flag.BoolVar(&cfg.AllowNoToken, "open", false, "Allow users to join without a token (open server)")
	flag.BoolVar(&cfg.EnableScreenShare, "screen-share", false, "Enable basic per-channel screen sharing")
	flag.StringVar(&cfg.ChannelsFile, "channels-file", "", "YAML file defining channels to create on startup")
	flag.StringVar(&cfg.MetricsAddr, "metrics", cfg.MetricsAddr, "HTTP bind address for Prometheus /metrics (empty to disable)")
	flag.BoolVar(&cfg.ExportUsers, "export-users", false, "Export all users as YAML and exit")
	flag.BoolVar(&cfg.ExportChannels, "export-channels", false, "Export all channels as YAML and exit")

	logLevel := flag.String("log-level", "info", "Log level: "+logging.LevelNames())
	logFormat := flag.String("log-format", "text", "Log format: text or json")
	flag.Parse()

	// Configure structured logging
	if err := logging.Setup(logging.Options{
		Level:  *logLevel,
		Format: *logFormat,
		Output: os.Stdout,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "invalid logging config: %v\n", err)
		os.Exit(1)
	}

	if err := run(cfg); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}

func run(cfg server.Config) (err error) {
	st, err := datastore.NewProviderFactory(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close database: %w", closeErr))
		}
	}()

	if cfg.ExportUsers {
		data, exportErr := server.ExportUsersYAML(st)
		if exportErr != nil {
			return fmt.Errorf("export users: %w", exportErr)
		}
		fmt.Print(string(data))
	}
	if cfg.ExportChannels {
		data, exportErr := server.ExportChannelsYAML(st)
		if exportErr != nil {
			return fmt.Errorf("export channels: %w", exportErr)
		}
		fmt.Print(string(data))
	}
	if cfg.ExportUsers || cfg.ExportChannels {
		return nil
	}

	srv := server.New(cfg, server.Dependencies{Store: st})
	return srv.Run()
}
