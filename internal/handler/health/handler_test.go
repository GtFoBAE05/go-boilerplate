package health

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// ctxPinger captures the context its ping was called with — what otelpgx and
// redisotel would derive their spans from.
type ctxPinger struct{ got context.Context }

func (p *ctxPinger) Ping(ctx context.Context) error {
	p.got = ctx
	return nil
}

type errorPinger struct{ err error }

func (p errorPinger) Ping(context.Context) error { return p.err }

// The clients create a span per ping with no opt-out, and the probe's own HTTP
// span is already filtered out in server.New — so without an unsampled parent
// every probe leaves parentless client spans in the backend.
func TestReadinessPingsAreNotSampled(t *testing.T) {
	pinger := &ctxPinger{}
	h := NewHandler(map[string]Pinger{"postgres": pinger}, nil, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz: want 200, got %d", rec.Code)
	}
	if pinger.got == nil {
		t.Fatal("dependency was never pinged")
	}

	sc := trace.SpanContextFromContext(pinger.got)
	// Valid matters as much as unsampled: with no parent, ParentBased falls back
	// to the root sampler and these get sampled after all.
	if !sc.IsValid() {
		t.Fatal("ping context carries no span context, so nothing suppresses the client spans")
	}
	if sc.IsSampled() {
		t.Fatal("ping context is sampled: probe client spans will reach the backend")
	}
}

func TestReadinessDoesNotLeakDependencyError(t *testing.T) {
	const dependencyErr = "password=hunter2 host=db.internal"
	var logs bytes.Buffer
	h := NewHandler(
		map[string]Pinger{"postgres": errorPinger{err: errors.New(dependencyErr)}},
		nil,
		slog.New(slog.NewJSONHandler(&logs, nil)),
	)

	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz: want 503, got %d", rec.Code)
	}
	if got, want := strings.TrimSpace(rec.Body.String()), `{"checks":{"postgres":"down"}}`; got != want {
		t.Fatalf("/readyz body: want %s, got %s", want, got)
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "db.internal") {
		t.Fatalf("/readyz leaked dependency error: %s", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "hunter2") || !strings.Contains(logs.String(), "db.internal") {
		t.Fatalf("log does not contain dependency details: %s", logs.String())
	}
}

func TestReadinessReportsOptionalFailureAsDegraded(t *testing.T) {
	var logs bytes.Buffer
	h := NewHandler(nil,
		map[string]Pinger{"redis": errorPinger{err: errors.New("redis unavailable")}},
		slog.New(slog.NewJSONHandler(&logs, nil)),
	)

	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz: want 200, got %d", rec.Code)
	}
	if got, want := strings.TrimSpace(rec.Body.String()), `{"checks":{"redis":"degraded"}}`; got != want {
		t.Fatalf("/readyz body: want %s, got %s", want, got)
	}
	if !strings.Contains(logs.String(), "redis unavailable") || !strings.Contains(logs.String(), `"required":false`) {
		t.Fatalf("log does not contain optional dependency failure: %s", logs.String())
	}
}
