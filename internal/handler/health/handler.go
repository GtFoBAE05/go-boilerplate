// Package health exposes liveness and readiness probes.
package health

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
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

// readinessTimeout bounds the whole probe, not each dependency, so one slow
// dependency cannot consume the orchestrator's probe budget.
const readinessTimeout = 2 * time.Second

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
	// state remembers the last result per dependency so a prolonged outage logs
	// once on transition, not on every probe.
	state sync.Map
}

// NewHandler builds the probe handler; log is required to keep dependency
// errors out of readiness responses.
func NewHandler(required, optional map[string]Pinger, log *slog.Logger) *Handler {
	return &Handler{required: required, optional: optional, log: log}
}

// BeginDrain makes /readyz fail so an orchestrator removes this instance before
// in-flight work is drained; liveness stays green.
func (h *Handler) BeginDrain() { h.draining.Store(true) }

// Liveness always returns 200 while the process is up, including during drain:
// a liveness failure would tell the orchestrator to kill it mid-shutdown.
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

	ctx, cancel := context.WithTimeout(unsampled(r.Context()), readinessTimeout)
	defer cancel()

	type result struct {
		name     string
		required bool
		err      error
	}
	total := len(h.required) + len(h.optional)
	results := make(chan result, total)
	for name, dep := range h.required {
		go func(name string, dep Pinger) {
			results <- result{name: name, required: true, err: dep.Ping(ctx)}
		}(name, dep)
	}
	for name, dep := range h.optional {
		go func(name string, dep Pinger) {
			results <- result{name: name, required: false, err: dep.Ping(ctx)}
		}(name, dep)
	}

	status := http.StatusOK
	checks := make(map[string]string, total)
	record := func(res result) {
		h.recordDependency(r.Context(), res.name, res.required, res.err)
		switch {
		case res.err == nil:
			checks[res.name] = "up"
		case res.required:
			checks[res.name] = "down"
			status = http.StatusServiceUnavailable
		default:
			checks[res.name] = "degraded"
		}
	}

	// Do not wait for a Pinger that ignores its context: the probe budget is a
	// hard ceiling, not a hint. The channel is buffered so late goroutines can
	// still send and exit after this response is written.
	pending := make(map[string]bool, total)
	for name := range h.required {
		pending[name] = true
	}
	for name := range h.optional {
		pending[name] = false
	}
	for received := 0; received < total; {
		select {
		case res := <-results:
			received++
			delete(pending, res.name)
			record(res)
		case <-ctx.Done():
			for name, required := range pending {
				record(result{name: name, required: required, err: ctx.Err()})
			}
			received = total
		}
	}

	handler.WriteJSON(w, status, map[string]any{"checks": checks})
}

// recordDependency logs transitions only: an outage that lasts hours must not
// produce one warning per probe.
func (h *Handler) recordDependency(ctx context.Context, name string, required bool, err error) {
	down := err != nil
	prev, loaded := h.state.Swap(name, down)
	prevDown, _ := prev.(bool)
	switch {
	case down && (!loaded || !prevDown):
		h.log.WarnContext(ctx, "readiness dependency failed",
			"dependency", name, "required", required, "error", err)
	case !down && loaded && prevDown:
		h.log.InfoContext(ctx, "readiness dependency recovered",
			"dependency", name, "required", required)
	}
}
