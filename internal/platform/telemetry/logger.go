package telemetry

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// LogOption tunes the logger. Unset options mean JSON output and no env
// attribute, so NewLogger(level) alone yields a usable production logger.
type LogOption func(*logOptions)

type logOptions struct {
	format string
	env    string
}

// Format selects the handler: "text" for local development, anything else
// (conventionally "json") for structured output.
func Format(f string) LogOption {
	return func(o *logOptions) { o.format = f }
}

// Env stamps every record with an "env" attribute identifying the deployment.
func Env(e string) LogOption {
	return func(o *logOptions) { o.env = e }
}

// NewLogger builds a slog.Logger at the given level ("debug", "info", "warn",
// "error"; anything else means info). Every record logged with a *Context
// method gets trace_id/span_id attached automatically when the context
// carries an active span.
func NewLogger(level string, opts ...LogOption) *slog.Logger {
	var o logOptions
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
