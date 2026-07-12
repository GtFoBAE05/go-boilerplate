// Package health exposes liveness and readiness probes.
package health

import (
	"context"
	"net/http"
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
}

// NewHandler builds the probe handler from required and optional
// dependencies, keyed by the name reported in the readiness body.
func NewHandler(required, optional map[string]Pinger) *Handler {
	return &Handler{required: required, optional: optional}
}

// Liveness always returns 200: the process is up.
func (h *Handler) Liveness(w http.ResponseWriter, _ *http.Request) {
	handler.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readiness returns 503 only when a required dependency fails its ping.
func (h *Handler) Readiness(w http.ResponseWriter, r *http.Request) {
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
