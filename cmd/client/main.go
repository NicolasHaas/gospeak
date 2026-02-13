package main

import (
	"os"

	"github.com/NicolasHaas/gospeak/pkg/logging"
	"github.com/NicolasHaas/gospeak/ui"
)

func main() {
	// Default to "info"; override with GOSPEAK_LOG_LEVEL env var (debug, info, warn, error).
	level := "info"
	if v := os.Getenv("GOSPEAK_LOG_LEVEL"); v != "" {
		level = v
	}
	format := "text"
	if v := os.Getenv("GOSPEAK_LOG_FORMAT"); v != "" {
		format = v
	}
	_ = logging.Setup(logging.Options{
		Level:  level,
		Format: format,
		Output: os.Stdout,
	})

	app := ui.NewApp()
	app.Run()
}
