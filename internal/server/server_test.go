package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"go-boilerplate/internal/config"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"

	usersvc "go-boilerplate/internal/service/user"
)

// stubUserService lets the router build without a real service; List returns
// empty so allowed requests succeed and we can observe the 429 on overflow.
type stubUserService struct{ usersvc.UserService }

func (stubUserService) List(context.Context, int32, int32) ([]usersvc.ListOutput, error) {
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

	for _, path := range []string{"/healthz", "/readyz"} {
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

// Metrics leave over OTLP. Re-adding a scrape endpoint would put route
// patterns, latency distributions and pool sizes back on a public port.
func TestNoMetricsEndpointOnPublicRouter(t *testing.T) {
	h := newTestServer(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("/metrics must not be served; metrics are pushed over OTLP, got %d", rec.Code)
	}
}

// Without routePattern feeding otelhttp's Labeler, every endpoint lands in one
// latency histogram and per-route alerting is impossible.
func TestRequestMetricsCarryRoutePattern(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })
	// Built after the provider is installed: otelhttp resolves its meter once,
	// when the handler is constructed.
	h := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	req.RemoteAddr = "203.0.113.5:1234"
	h.ServeHTTP(httptest.NewRecorder(), req)

	// An unmatched path must contribute no route, or a scanner walking random
	// URLs mints a series per path.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope/12345", nil))

	routes := recordedRoutes(t, reader)
	if !slices.Contains(routes, "/api/v1/users") {
		t.Fatalf("want an http.route=/api/v1/users data point, got routes %q", routes)
	}
	if slices.Contains(routes, "/nope/12345") {
		t.Fatalf("unmatched path leaked into http.route: %q", routes)
	}
}

// recordedRoutes returns the http.route attribute of every request-duration
// data point, with the empty string standing for a point that carries none.
func recordedRoutes(t *testing.T, reader sdkmetric.Reader) []string {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	var routes []string
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "http.server.request.duration" {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("%s: want a float64 histogram, got %T", m.Name, m.Data)
			}
			for _, dp := range hist.DataPoints {
				route, _ := dp.Attributes.Value(semconv.HTTPRouteKey)
				routes = append(routes, route.AsString())
			}
		}
	}
	if len(routes) == 0 {
		t.Fatal("no http.server.request.duration data points recorded at all")
	}
	return routes
}

// Tracing probes buries real traces; counting them skews the latency
// percentiles and the error-ratio denominator.
func TestProbesAreNotTraced(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	h := newTestServer(t)

	for _, path := range []string{"/healthz", "/readyz"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	if got := recorder.Ended(); len(got) != 0 {
		t.Fatalf("probes must produce no spans, got %d", len(got))
	}

	// The filter must be scoped to probes, not disable tracing outright.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	req.RemoteAddr = "203.0.113.5:1234"
	h.ServeHTTP(httptest.NewRecorder(), req)
	if len(recorder.Ended()) == 0 {
		t.Fatal("a real request must still be traced")
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
