// Command api is the HTTP API entry point. All bootstrap and wiring lives
// in internal/app; this file should never need to change.
package main

import (
	"log/slog"
	"os"

	"go-boilerplate/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}
