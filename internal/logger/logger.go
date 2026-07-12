// Package logger builds the application's slog.Logger: level and format from
// config, with trace_id/span_id attached automatically inside traced requests.
package logger

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go-boilerplate/internal/config"

	"go.opentelemetry.io/otel/trace"
)

// New builds a slog.Logger from config: JSON (production) or text (local),
// with the level parsed from config ("debug", "info", "warn", "error").
// Every record logged with a *Context method gets trace_id/span_id attached
// automatically when the context carries an active span.
func New(cfg config.LogConfig, appEnv string) *slog.Logger {
	level := parseLevel(cfg.Level)

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if strings.EqualFold(cfg.Format, "text") {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(traceHandler{handler}).With("env", appEnv)
}

// traceHandler decorates a slog.Handler with trace correlation, so any log
// line written inside a traced request can be looked up in Jaeger and
// vice versa — no per-call-site plumbing.
type traceHandler struct {
	slog.Handler
}

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
