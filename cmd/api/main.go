// Command api is the HTTP API entry point: load configuration, hand it to the
// app, and turn a failure into an exit code. Wiring lives in internal/app.
package main

import (
	"log/slog"
	"os"

	"go-boilerplate/internal/app"
	"go-boilerplate/internal/config"
)

func main() {
	// Config is loaded here, before the app exists, so app.Run takes a ready
	// *config.Config instead of reaching for the environment itself. That keeps
	// Run callable from a test or an alternate binary with a config built in
	// code — no file, no environment.
	cfg, err := config.Load()
	if err != nil {
		// The real logger is built from cfg, so this failure has to report
		// through the default one.
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	if err := app.Run(cfg); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}
