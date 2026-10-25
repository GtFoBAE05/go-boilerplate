// Package migrations embeds the goose SQL files so they travel with the binary.
// Go's embed cannot reach parent directories, so the directive lives here beside
// the .sql files.
package migrations

import "embed"

// FS holds every goose migration in this directory, applied by
// internal/platform/postgres.Migrate at startup.
//
//go:embed *.sql
var FS embed.FS
