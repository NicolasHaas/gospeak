// Package logging provides configurable structured logging for GoSpeak.
//
// Both server and client use Go's standard log/slog with configurable levels.
// Log levels from most to least verbose: DEBUG, INFO, WARN, ERROR.
//
// Usage:
//
//	logging.Setup(logging.Options{Level: "debug", Format: "text"})
//	slog.Debug("detailed trace", "key", "value")
//	slog.Info("normal operation", "key", "value")
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Options controls how logging is configured.
type Options struct {
	Level  string    // "debug", "info", "warn", "error" (default: "info")
	Format string    // "text" or "json" (default: "text")
	Output io.Writer // where to write logs (default: os.Stdout)
}

// ParseLevel converts a string level name to slog.Level.
// Returns slog.LevelInfo for unrecognized values.
func ParseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "info", "":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Setup initialises the global slog logger with the given options.
// Safe to call early in main() before any logging occurs.
func Setup(opts Options) error {
	if err := Validate(opts.Level); err != nil {
		return err
	}

	out := opts.Output
	if out == nil {
		out = os.Stdout
	}

	level := ParseLevel(opts.Level)

	handlerOpts := &slog.HandlerOptions{
		Level:     level,
		AddSource: level == slog.LevelDebug, // include file:line in debug mode
	}

	var handler slog.Handler
	switch strings.ToLower(opts.Format) {
	case "json":
		handler = slog.NewJSONHandler(out, handlerOpts)
	default:
		handler = slog.NewTextHandler(out, handlerOpts)
	}

	slog.SetDefault(slog.New(handler))
	return nil
}

// LevelNames returns all valid level names, useful for --help text.
func LevelNames() string {
	return "debug, info, warn, error"
}

// Validate returns an error if the level string is not recognized.
func Validate(level string) error {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug", "info", "warn", "warning", "error", "":
		return nil
	default:
		return fmt.Errorf("unknown log level %q (valid: %s)", level, LevelNames())
	}
}
