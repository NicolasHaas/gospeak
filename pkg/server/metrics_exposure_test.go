package server

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestMetricsWarnOnlyWhenBoundNonLoopback(t *testing.T) {
	for _, addr := range []string{"", "127.0.0.1:0", "localhost:0", "[::1]:0", ":0"} {
		t.Run(addr, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(previous)
			srv := New(DefaultConfig(), Dependencies{})
			srv.cfg.MetricsAddr = addr
			if err := srv.startMetricsHTTP(); err != nil {
				t.Fatal(err)
			}
			srv.Shutdown()
			warned := strings.Contains(logs.String(), "unauthenticated metrics exposed")
			if warned != (addr == ":0") {
				t.Fatalf("warning=%v logs=%s", warned, logs.String())
			}
		})
	}
}
