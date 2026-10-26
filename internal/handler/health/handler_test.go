package health

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

type barrierPinger struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (p barrierPinger) Ping(context.Context) error {
	p.started <- struct{}{}
	<-p.release
	return nil
}

func TestReadinessRunsDependenciesInParallel(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	h := NewHandler(map[string]Pinger{
		"postgres": barrierPinger{started: started, release: release},
		"redis":    barrierPinger{started: started, release: release},
	}, nil, slog.New(slog.DiscardHandler))

	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		done <- rec.Code
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("dependencies did not start concurrently")
		}
	}
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
}

type hangingPinger struct {
	release <-chan struct{}
}

func (p hangingPinger) Ping(context.Context) error {
	<-p.release
	return nil
}

func TestReadinessTimesOutHungDependency(t *testing.T) {
	release := make(chan struct{})
	h := NewHandler(map[string]Pinger{"postgres": hangingPinger{release: release}},
		nil, slog.New(slog.DiscardHandler))

	start := time.Now()
	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	close(release)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	if elapsed := time.Since(start); elapsed > readinessTimeout+time.Second {
		t.Fatalf("probe exceeded its budget: %s", elapsed)
	}
}

type toggledPinger struct {
	mu  sync.Mutex
	err error
}

func (p *toggledPinger) Ping(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *toggledPinger) set(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

func TestReadinessLogsDependencyTransitionsOnly(t *testing.T) {
	var logs bytes.Buffer
	dep := &toggledPinger{err: errors.New("down")}
	h := NewHandler(map[string]Pinger{"postgres": dep}, nil, slog.New(slog.NewJSONHandler(&logs, nil)))

	for range 2 {
		h.Readiness(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
	}
	dep.set(nil)
	for range 2 {
		h.Readiness(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
	}

	out := logs.String()
	if got := strings.Count(out, "readiness dependency failed"); got != 1 {
		t.Fatalf("want one failure log, got %d: %s", got, out)
	}
	if got := strings.Count(out, "readiness dependency recovered"); got != 1 {
		t.Fatalf("want one recovery log, got %d: %s", got, out)
	}
}
