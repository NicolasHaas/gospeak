package main

import (
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
	flag.StringVar(&cfg.DBPath, "db", cfg.DBPath, "SQLite database file path")
	flag.StringVar(&cfg.CertFile, "cert", "", "TLS certificate file (auto-generated if empty)")
	flag.StringVar(&cfg.KeyFile, "key", "", "TLS private key file (auto-generated if empty)")
	flag.StringVar(&cfg.DataDir, "data", ".", "Data directory for generated files")
	flag.BoolVar(&cfg.AllowNoToken, "open", false, "Allow users to join without a token (open server)")
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

	// Handle export commands (run and exit)
	if cfg.ExportUsers || cfg.ExportChannels {
		st, err := datastore.NewProviderFactory(cfg.DBPath)
		if err != nil {
			slog.Error("open database", "err", err)
			os.Exit(1)
		}
		defer st.Close()

		if cfg.ExportUsers {
			data, err := server.ExportUsersYAML(st)
			if err != nil {
				slog.Error("export users", "err", err)
				os.Exit(1)
			}
			fmt.Print(string(data))
		}
		if cfg.ExportChannels {
			data, err := server.ExportChannelsYAML(st)
			if err != nil {
				slog.Error("export channels", "err", err)
				os.Exit(1)
			}
			fmt.Print(string(data))
		}
		return
	}

	st, err := datastore.NewProviderFactory(cfg.DBPath)
	if err != nil {
		slog.Error("open database", "err", err)
		os.Exit(1)
	}

	srv := server.New(cfg, server.Dependencies{Store: st})
	if err := srv.Run(); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
