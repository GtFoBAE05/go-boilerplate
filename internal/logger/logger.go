// Package logger builds the application's slog.Logger from a level and
// options, with trace_id/span_id attached automatically inside traced requests.
package logger

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// Option tunes the logger. Unset options mean JSON output and no env
// attribute, so New(level) alone yields a usable production logger.
type Option func(*options)

type options struct {
	format string
	env    string
}

// Format selects the handler: "text" for local development, anything else
// (conventionally "json") for structured output.
func Format(f string) Option {
	return func(o *options) { o.format = f }
}

// Env stamps every record with an "env" attribute identifying the deployment.
func Env(e string) Option {
	return func(o *options) { o.env = e }
}

// New builds a slog.Logger at the given level ("debug", "info", "warn",
// "error"; anything else means info). Every record logged with a *Context
// method gets trace_id/span_id attached automatically when the context
// carries an active span.
func New(level string, opts ...Option) *slog.Logger {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	handlerOpts := &slog.HandlerOptions{Level: parseLevel(level)}

	var handler slog.Handler
	if strings.EqualFold(o.format, "text") {
		handler = slog.NewTextHandler(os.Stdout, handlerOpts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, handlerOpts)
	}

	log := slog.New(traceHandler{handler})
	if o.env != "" {
		log = log.With("env", o.env)
	}
	return log
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
