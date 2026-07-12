package server

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"go-boilerplate/internal/handler"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/trace"
)

// requestLogger emits one structured log line per request, correlated with
// the trace ID when a span is recording.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Probe and scrape endpoints get hammered by orchestrators;
			// logging every hit drowns real traffic.
			switch r.URL.Path {
			case "/healthz", "/readyz", "/metrics":
				next.ServeHTTP(w, r)
				return
			}

			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()

			next.ServeHTTP(ww, r)

			// trace_id/span_id come from the logger's trace-aware handler.
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", float64(time.Since(start).Microseconds()) / 1000,
				"request_id", middleware.GetReqID(r.Context()),
				"remote_addr", r.RemoteAddr,
			}

			log.LogAttrs(r.Context(), levelFor(ww.Status()), "http request", toAttrs(attrs)...)
		})
	}
}

func levelFor(status int) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

func toAttrs(kv []any) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		attrs = append(attrs, slog.Any(kv[i].(string), kv[i+1]))
	}
	return attrs
}

// recoverPanics converts handler panics into enveloped 500s, unlike chi's
// Recoverer which writes plain text. The stack is logged, never returned.
func recoverPanics(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					// net/http uses this sentinel to abort a response;
					// swallowing it would break that contract.
					if err, ok := rec.(error); ok && err == http.ErrAbortHandler {
						panic(rec)
					}
					log.ErrorContext(r.Context(), "panic recovered",
						"panic", rec,
						"stack", string(debug.Stack()),
					)
					handler.WriteError(w, http.StatusInternalServerError, handler.CodeInternal, "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// spanRoutePattern renames the server span to "METHOD /route/{pattern}" once
// chi has resolved the route. Runs after the handler because the pattern is
// only known post-routing.
func spanRoutePattern(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil {
			if pattern := routeCtx.RoutePattern(); pattern != "" {
				span := trace.SpanFromContext(r.Context())
				span.SetName(r.Method + " " + pattern)
			}
		}
	})
}
