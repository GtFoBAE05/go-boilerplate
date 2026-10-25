// Command api is the HTTP API entry point: load configuration, hand it to the
// app, and turn a failure into an exit code. Wiring lives in internal/app.
package main

import (
	"log/slog"
	"os"

	"github.com/GtFoBAE05/go-boilerplate/internal/app"
	"github.com/GtFoBAE05/go-boilerplate/internal/config"
)

func main() {
	// Loaded here, not inside Run, so Run stays callable with a config built in code.
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
