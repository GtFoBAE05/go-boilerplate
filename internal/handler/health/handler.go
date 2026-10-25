// Package health exposes liveness and readiness probes.
package health

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/GtFoBAE05/go-boilerplate/internal/handler"

	"go.opentelemetry.io/otel/trace"
)

// Pinger is satisfied by *pgxpool.Pool; other dependencies adapt to it in
// the server package.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Handler serves the /healthz and /readyz probes.
type Handler struct {
	log *slog.Logger
	// required dependencies fail readiness (503) when down.
	required map[string]Pinger
	// optional dependencies are reported but never fail readiness —
	// the app can serve traffic without them (e.g. Redis).
	optional map[string]Pinger
	// draining makes readiness fail while the process is shutting down.
	// Read on every probe and written once from the shutdown path, hence atomic.
	draining atomic.Bool
}

// NewHandler builds the probe handler; log is required to keep dependency
// errors out of readiness responses.
func NewHandler(required, optional map[string]Pinger, log *slog.Logger) *Handler {
	return &Handler{required: required, optional: optional, log: log}
}

// BeginDrain makes /readyz fail from now on, without touching liveness.
//
// It is called before shutdown starts so an orchestrator can take this
// instance out of rotation while it is still able to finish in-flight work.
// Endpoint removal is asynchronous in Kubernetes: shutting down the moment
// SIGTERM lands means new connections keep arriving at a closing listener,
// which is the usual source of 502s during a rolling deploy.
func (h *Handler) BeginDrain() { h.draining.Store(true) }

// Liveness always returns 200 while the process is up, including while
// draining: a liveness failure tells the orchestrator to kill the process,
// which is the opposite of what a graceful shutdown wants.
func (h *Handler) Liveness(w http.ResponseWriter, _ *http.Request) {
	handler.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// probeParent stops the ping spans that otelpgx and redisotel create with no
// opt-out: under the parentbased_* samplers, a child of an unsampled parent is
// dropped. Must be valid, or ParentBased falls back to the root sampler. IDs
// are arbitrary; nothing is exported under them.
var probeParent = trace.NewSpanContext(trace.SpanContextConfig{
	TraceID: trace.TraceID{0x70, 0x72, 0x6f, 0x62, 0x65, 1},
	SpanID:  trace.SpanID{0x70, 0x72, 0x6f, 0x62, 0x65, 1},
})

func unsampled(ctx context.Context) context.Context {
	return trace.ContextWithSpanContext(ctx, probeParent)
}

// Readiness returns 503 while draining, or when a required dependency fails
// its ping.
func (h *Handler) Readiness(w http.ResponseWriter, r *http.Request) {
	if h.draining.Load() {
		handler.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "draining",
		})
		return
	}

	ctx, cancel := context.WithTimeout(unsampled(r.Context()), 3*time.Second)
	defer cancel()

	status := http.StatusOK
	checks := make(map[string]string, len(h.required)+len(h.optional))

	for name, dep := range h.required {
		if err := dep.Ping(ctx); err != nil {
			h.log.WarnContext(r.Context(), "readiness dependency failed",
				"dependency", name, "required", true, "error", err)
			checks[name] = "down"
			status = http.StatusServiceUnavailable
		} else {
			checks[name] = "up"
		}
	}
	for name, dep := range h.optional {
		if err := dep.Ping(ctx); err != nil {
			h.log.WarnContext(r.Context(), "readiness dependency failed",
				"dependency", name, "required", false, "error", err)
			checks[name] = "degraded"
		} else {
			checks[name] = "up"
		}
	}

	handler.WriteJSON(w, status, map[string]any{"checks": checks})
}
