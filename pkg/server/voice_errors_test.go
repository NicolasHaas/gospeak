package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestVoiceErrorDiagnosticsArePrivate(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	srv := New(DefaultConfig(), Dependencies{})
	t.Cleanup(srv.Shutdown)
	for _, tc := range []struct {
		name   string
		err    error
		reason string
	}{
		{"wrapped socket cause", fmt.Errorf("secret-auth-media: %w", &net.OpError{Op: "write", Net: "udp", Source: &net.UDPAddr{IP: net.ParseIP("192.0.2.21"), Port: 9601}, Addr: &net.UDPAddr{IP: net.ParseIP("198.51.100.22"), Port: 45000}, Err: os.NewSyscallError("sendto", syscall.ECONNREFUSED)}), syscall.ECONNREFUSED.Error()},
		{"wrapped timeout", fmt.Errorf("secret-auth-media: %w", os.ErrDeadlineExceeded), "timeout"},
		{"short write", fmt.Errorf("secret-auth-media: %w", io.ErrShortWrite), "short_write"},
		{"unknown", errors.New("secret-auth-media 203.0.113.23:443"), "io_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			for _, level := range []slog.Level{slog.LevelError, slog.LevelDebug, slog.LevelWarn} {
				srv.logVoiceError(level, "voice test error", tc.err)
			}
			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			if len(lines) != 3 {
				t.Fatalf("got %d logs, want 3", len(lines))
			}
			for _, line := range lines {
				var record map[string]any
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatal(err)
				}
				if record["reason"] != tc.reason {
					t.Errorf("reason=%v, want %q", record["reason"], tc.reason)
				}
				for _, private := range []string{"secret-auth-media", "192.0.2.21", "198.51.100.22", "203.0.113.23", "45000"} {
					if strings.Contains(line, private) {
						t.Errorf("private value leaked: %q", private)
					}
				}
			}
		})
	}
	for _, err := range []error{fmt.Errorf("wrapped: %w", net.ErrClosed), fmt.Errorf("wrapped: %w", context.Canceled)} {
		logs.Reset()
		srv.logVoiceError(slog.LevelError, "voice read error", err)
		if logs.Len() != 0 {
			t.Error("expected local close/cancellation to be quiet")
		}
	}
	logs.Reset()
	srv.cancel()
	srv.logVoiceError(slog.LevelError, "voice read error", syscall.EIO)
	if logs.Len() != 0 {
		t.Error("expected shutdown to be quiet")
	}
}

func TestVoiceLoopLocalCloseReturns(t *testing.T) {
	srv := New(DefaultConfig(), Dependencies{})
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); srv.voiceLoop(conn) }()
	defer func() { srv.cancel(); <-done; srv.Shutdown() }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("voice loop spun after local socket close")
	}
}
