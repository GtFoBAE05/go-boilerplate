package app

import "log/slog"

// Build provenance, overwritten with -ldflags -X (see Dockerfile and Makefile).
// It lives here because app is the only consumer; .dockerignore excludes .git.
var (
	// version is a release tag or short commit describing the build.
	version = "dev"
	// commit is the full git SHA the binary was built from.
	commit = "none"
	// buildDate is the RFC3339 build timestamp.
	buildDate = "unknown"
)

// buildInfo renders provenance as one structured log group.
func buildInfo() slog.Value {
	return slog.GroupValue(
		slog.String("version", version),
		slog.String("commit", commit),
		slog.String("date", buildDate),
	)
}
