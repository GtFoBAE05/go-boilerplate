// Package health exposes liveness and readiness probes.
package health

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"go-boilerplate/internal/handler"
)

// Pinger is satisfied by *pgxpool.Pool; other dependencies adapt to it in
// the server package.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Handler serves the /healthz and /readyz probes.
type Handler struct {
	// required dependencies fail readiness (503) when down.
	required map[string]Pinger
	// optional dependencies are reported but never fail readiness —
	// the app can serve traffic without them (e.g. cache).
	optional map[string]Pinger
	// draining makes readiness fail while the process is shutting down.
	// Read on every probe and written once from the shutdown path, hence atomic.
	draining atomic.Bool
}

// NewHandler builds the probe handler from required and optional
// dependencies, keyed by the name reported in the readiness body.
func NewHandler(required, optional map[string]Pinger) *Handler {
	return &Handler{required: required, optional: optional}
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

// Readiness returns 503 while draining, or when a required dependency fails
// its ping.
func (h *Handler) Readiness(w http.ResponseWriter, r *http.Request) {
	if h.draining.Load() {
		handler.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "draining",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	status := http.StatusOK
	checks := make(map[string]string, len(h.required)+len(h.optional))

	for name, dep := range h.required {
		if err := dep.Ping(ctx); err != nil {
			checks[name] = "down: " + err.Error()
			status = http.StatusServiceUnavailable
		} else {
			checks[name] = "up"
		}
	}
	for name, dep := range h.optional {
		if err := dep.Ping(ctx); err != nil {
			checks[name] = "degraded: " + err.Error()
		} else {
			checks[name] = "up"
		}
	}

	handler.WriteJSON(w, status, map[string]any{"checks": checks})
}
