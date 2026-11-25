package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/server"
)

func TestRunRejectsPartialTLSBeforePersistentStartup(t *testing.T) {
	for _, name := range []string{"certificate only", "key only"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := server.DefaultConfig()
			cfg.DBPath = filepath.Join(dir, "database", "gospeak.db")
			cfg.DataDir = filepath.Join(dir, "data")
			if name == "certificate only" {
				cfg.CertFile = "custom.crt"
			} else {
				cfg.KeyFile = "custom.key"
			}
			if err := run(cfg); err == nil || !strings.Contains(err.Error(), "TLS certificate and key must be configured together") {
				t.Fatalf("run() = %v, want incomplete TLS pair error", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("invalid TLS configuration created persistent state: %v", entries)
			}
		})
	}
}

func TestRunExportsIgnoreTLSConfiguration(t *testing.T) {
	for _, flags := range []string{"users", "channels", "both"} {
		t.Run(flags, func(t *testing.T) {
			cfg := server.DefaultConfig()
			cfg.DBPath = filepath.Join(t.TempDir(), "gospeak.db")
			cfg.CertFile = "unused.crt"
			cfg.ExportUsers = flags != "channels"
			cfg.ExportChannels = flags != "users"
			st, err := datastore.NewProviderFactory(cfg.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			if err := run(cfg); err != nil {
				t.Fatalf("export requires unused TLS configuration: %v", err)
			}
		})
	}
}
