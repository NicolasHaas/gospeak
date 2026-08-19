package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/server"
)

func TestRunRejectsInvalidChannelsFileBeforeOpeningDatabase(t *testing.T) {
	dir := t.TempDir()
	channelsPath := filepath.Join(dir, "channels.yaml")
	if err := os.WriteFile(channelsPath, []byte("channels:\n  - unknown: true\n"), 0o600); err != nil {
		t.Fatalf("write channels config: %v", err)
	}
	cfg := server.DefaultConfig()
	cfg.ChannelsFile = channelsPath
	cfg.DBPath = filepath.Join(dir, "gospeak.db")

	err := run(cfg)
	if err == nil || !strings.Contains(err.Error(), "validate channels config") {
		t.Fatalf("run() error = %v, want channels validation error", err)
	}
	for _, path := range []string{cfg.DBPath, cfg.DBPath + "-wal", cfg.DBPath + "-shm"} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("database artifact %q exists after validation failure (stat error %v)", path, statErr)
		}
	}
}
