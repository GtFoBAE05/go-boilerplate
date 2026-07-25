package health

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// The clients create a span per ping with no opt-out, and the probe's own HTTP
// span is already filtered out in server.New — so without an unsampled parent
// every probe leaves parentless client spans in the backend.
func TestReadinessPingsAreNotSampled(t *testing.T) {
	pinger := &ctxPinger{}
	h := NewHandler(map[string]Pinger{"postgres": pinger}, nil)

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
