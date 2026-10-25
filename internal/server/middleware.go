package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/GtFoBAE05/go-boilerplate/internal/handler"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

// isProbe reports whether path is an orchestrator health probe. Hit every few
// seconds forever, so the request log and otelhttp both skip it. One function
// so the two skip lists cannot drift apart.
func isProbe(path string) bool {
	return path == "/healthz" || path == "/readyz"
}

// requestLogger emits one structured log line per request, correlated with
// the trace ID when a span is recording.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isProbe(r.URL.Path) {
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
			}
			// RemoteAddr is PII and has no place in the default info log. It
			// is still available at debug when someone is actually debugging.
			if log.Enabled(r.Context(), slog.LevelDebug) {
				attrs = append(attrs, "remote_addr", r.RemoteAddr)
			}

			log.LogAttrs(r.Context(), levelFor(ww.Status()), "http request", toAttrs(attrs)...)
		})
	}
}

// requestTimeout cancels the request context after d. It deliberately does not
// write a response itself on every path: chi's Timeout middleware writes a bare
// 504, and WriteServiceError writes the JSON envelope when the handler notices
// the deadline - two writers on one response. Here the fallback is written only
// when the handler wrote nothing at all.
func requestTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()

			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r.WithContext(ctx))

			if errors.Is(ctx.Err(), context.DeadlineExceeded) && ww.Status() == 0 {
				handler.WriteError(ww, http.StatusGatewayTimeout, handler.CodeTimeout, "request timed out")
			}
		})
	}
}

// sanitizeRequestID drops a client-supplied X-Request-Id unless it is short and
// uses only characters safe to copy into logs and trace attributes. chi then
// generates its own ID, so every request still has one.
func sanitizeRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.Header.Get("X-Request-Id"); id != "" && !validRequestID(id) {
			r.Header.Del("X-Request-Id")
		}
		next.ServeHTTP(w, r)
	})
}

func validRequestID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
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

// routePattern stamps chi's resolved route onto the server span and onto
// otelhttp's request metrics. Runs after the handler because the pattern is
// only known post-routing: otelhttp wraps the router from outside and never
// sees it, so without this every endpoint shares one latency histogram. Its
// Labeler is the supported way to reach those metrics mid-request.
func routePattern(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		routeCtx := chi.RouteContext(r.Context())
		if routeCtx == nil {
			return
		}
		// Empty on a 404, where the only "route" is the raw URL: labelling with
		// that mints a series per path a scanner invents.
		pattern := routeCtx.RoutePattern()
		if pattern == "" {
			return
		}

		span := trace.SpanFromContext(r.Context())
		span.SetName(r.Method + " " + pattern)
		span.SetAttributes(semconv.HTTPRoute(pattern))

		if labeler, ok := otelhttp.LabelerFromContext(r.Context()); ok {
			labeler.Add(semconv.HTTPRoute(pattern))
		}
	})
}
