// Package migrations embeds the goose SQL migration files so they travel with
// the binary. Go's embed cannot reach parent directories, so the embed
// directive must live here, beside the .sql files it references.
package migrations

import "embed"

// FS holds every goose migration in this directory, applied by
// internal/platform/postgres.Migrate at startup.
//
//go:embed *.sql
var FS embed.FS
