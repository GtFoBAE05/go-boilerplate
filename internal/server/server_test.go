package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-boilerplate/internal/config"

	usersvc "go-boilerplate/internal/service/user"
)

// stubUserService lets the router build without a real service; List returns
// empty so allowed requests succeed and we can observe the 429 on overflow.
type stubUserService struct{ usersvc.Service }

func (stubUserService) List(context.Context, int32, int32) ([]usersvc.User, error) {
	return nil, nil
}

// newTestServer builds the real router allowing 1 request per window, so the
// second request is throttled.
func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	return newTestServerWith(t).http.Handler
}

func newTestServerWith(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		Redis:     config.RedisConfig{Mode: config.RedisModeDisabled},
		RateLimit: config.RateLimitConfig{Enabled: true, Requests: 1, Window: time.Second},
	}
	return New(cfg, slog.New(slog.DiscardHandler), Deps{Users: stubUserService{}})
}

func TestRateLimitEnvelope(t *testing.T) {
	h := newTestServer(t)

	do := func() *httptest.ResponseRecorder {
		// Same RemoteAddr each call so httprate keys them to one client.
		req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
		req.RemoteAddr = "203.0.113.5:1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := do(); rec.Code != http.StatusOK {
		t.Fatalf("first request: want 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	rec := do()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: want 429, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("429 response is not JSON: %v", err)
	}
	if resp.Error.Code != "RATE_LIMITED" {
		t.Fatalf("want code RATE_LIMITED, got %q", resp.Error.Code)
	}
	if resp.Error.Message == "" {
		t.Fatalf("want non-empty message")
	}
}

// Probes live outside /api/v1 and must never be rate-limited, even under a
// limit that would throttle the API.
func TestProbesNotRateLimited(t *testing.T) {
	h := newTestServer(t)

	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "203.0.113.5:1234"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code == http.StatusTooManyRequests {
				t.Fatalf("%s was rate-limited on call %d", path, i+1)
			}
		}
	}
}

// /metrics is served on the public router (a scraper reaches it from off-host),
// so it must at least answer there.
func TestMetricsOnPublicRouter(t *testing.T) {
	h := newTestServer(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics: want 200, got %d", rec.Code)
	}
}

// pprof must never be reachable on the public router, whatever the private
// listener is doing.
func TestPprofNotOnPublicRouter(t *testing.T) {
	h := newTestServer(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("/debug/pprof/ must not be on the public port, got %d", rec.Code)
	}
}

// The pprof listener binds loopback only — a routable bind would expose memory
// contents and let any caller trigger expensive profiles.
func TestPprofServerBindsLoopbackOnly(t *testing.T) {
	srv := NewPprofServer(true, 6060, slog.New(slog.DiscardHandler))
	if srv == nil {
		// Explicit return: t.Fatal already stops the test, but the linter
		// cannot see that and flags the derefs below.
		t.Fatal("want a pprof server for a non-zero port")
		return
	}
	if srv.Addr != "127.0.0.1:6060" {
		t.Fatalf("pprof must bind loopback, got %q", srv.Addr)
	}

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/debug/pprof/ on the private listener: want 200, got %d", rec.Code)
	}
}

// Disabled must mean no server at all — app.go keys the goroutine and the
// listening socket off this nil, so a non-nil return would run both. A
// configured port is ignored while disabled.
func TestPprofServerDisabledReturnsNil(t *testing.T) {
	if srv := NewPprofServer(false, 6060, slog.New(slog.DiscardHandler)); srv != nil {
		t.Fatalf("want nil server when disabled, got one bound to %q", srv.Addr)
	}
}

// Draining must fail readiness while liveness keeps reporting 200: a liveness
// failure tells the orchestrator to kill the process mid-drain.
func TestBeginDrainFailsReadinessButNotLiveness(t *testing.T) {
	srv := newTestServerWith(t)
	h := srv.http.Handler

	srv.BeginDrain()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz while draining: want 503, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz while draining: want 200, got %d", rec.Code)
	}
}
