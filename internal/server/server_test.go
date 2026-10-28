package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GtFoBAE05/go-boilerplate/internal/config"
	"github.com/GtFoBAE05/go-boilerplate/internal/handler"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"

	usersvc "github.com/GtFoBAE05/go-boilerplate/internal/service/user"
)

// stubUserService lets the router build without a real service; List returns
// empty so API requests can exercise the router without a database.
type stubUserService struct{ usersvc.UserService }

func (stubUserService) List(context.Context, usersvc.ListInput) ([]usersvc.ListOutput, error) {
	return nil, nil
}

type stubPinger struct{}

func (stubPinger) Ping(context.Context) error { return nil }

// newTestServer builds the real router with no external dependencies.
func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	return newTestServerWith(t).http.Handler
}

func newTestServerWith(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Port: 8080, ReadTimeout: 10 * time.Second, ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
			RequestTimeout: 20 * time.Second,
		},
		Redis: config.RedisConfig{Mode: config.RedisModeDisabled},
	}
	srv, err := New(cfg, slog.New(slog.DiscardHandler), Deps{Users: stubUserService{}, Postgres: stubPinger{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

// The header deadline is the slowloris guard; MaxHeaderBytes keeps a single
// request from spending the connection's read budget on headers. Both are
// asserted because neither shows up in behavior tests until under attack.
func TestHTTPServerHeaderLimits(t *testing.T) {
	s := newTestServerWith(t)

	if s.http.ErrorLog == nil {
		t.Fatal("want HTTP server errors routed through slog")
	}
	if got := s.http.ReadHeaderTimeout; got != 5*time.Second {
		t.Fatalf("want ReadHeaderTimeout 5s, got %s", got)
	}
	if got := s.http.MaxHeaderBytes; got != maxHeaderBytes {
		t.Fatalf("want MaxHeaderBytes %d, got %d", maxHeaderBytes, got)
	}
}

func TestNewRejectsMissingOrTypedNilDependencies(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, ReadTimeout: 10 * time.Second, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second},
		Redis:  config.RedisConfig{Mode: config.RedisModeDisabled},
	}
	log := slog.New(slog.DiscardHandler)

	if _, err := New(cfg, log, Deps{Postgres: stubPinger{}}); err == nil {
		t.Fatal("want an error when Users is nil")
	}
	var nilPool *pgxpool.Pool
	if _, err := New(cfg, log, Deps{Users: stubUserService{}, Postgres: nilPool}); err == nil {
		t.Fatal("want an error for a typed-nil Postgres pinger")
	}
}

func TestMethodNotAllowedSetsAllow(t *testing.T) {
	h := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/1", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	allow := rec.Header().Get("Allow")
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		if !strings.Contains(allow, method) {
			t.Fatalf("Allow %q missing %s", allow, method)
		}
	}
}

// The custom timeout middleware must write exactly one response, whether the
// handler notices the deadline (WriteServiceError) or not (fallback).
func TestRequestTimeoutWritesOneEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "handler writes",
			handler: func(w http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
				handler.WriteServiceError(w, r, slog.New(slog.DiscardHandler), r.Context().Err())
			},
		},
		{
			name: "handler returns silently",
			handler: func(_ http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := chi.NewRouter()
			router.Use(requestTimeout(10 * time.Millisecond))
			router.Get("/slow", tt.handler)

			cw := &countingWriter{ResponseWriter: httptest.NewRecorder()}
			router.ServeHTTP(cw, httptest.NewRequest(http.MethodGet, "/slow", nil))

			if cw.headerWrites != 1 {
				t.Fatalf("want exactly one WriteHeader call, got %d", cw.headerWrites)
			}
			rec := cw.ResponseWriter.(*httptest.ResponseRecorder)
			if rec.Code != http.StatusGatewayTimeout {
				t.Fatalf("want 504, got %d (body: %s)", rec.Code, rec.Body.String())
			}
			var resp struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("timeout body is not JSON: %v", err)
			}
			if resp.Error.Code != handler.CodeTimeout {
				t.Fatalf("want code %s, got %q", handler.CodeTimeout, resp.Error.Code)
			}
		})
	}
}

func TestRecoverPanicsWritesEnvelopeWhenNothingWasWritten(t *testing.T) {
	h := recoverPanics(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("panic response is not JSON: %v", err)
	}
	if resp.Error.Code != handler.CodeInternal {
		t.Fatalf("want code %s, got %q", handler.CodeInternal, resp.Error.Code)
	}
}

func TestRecoverPanicsAbortsAfterResponseStarted(t *testing.T) {
	h := recoverPanics(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("boom")
	}))

	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Fatalf("want http.ErrAbortHandler after a started response, got %v", got)
		}
	}()

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	t.Fatal("handler should have aborted the connection")
}

func unreachableRedis(t *testing.T) *goredis.Client {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  10 * time.Millisecond,
		ReadTimeout:  10 * time.Millisecond,
		WriteTimeout: 10 * time.Millisecond,
		MaxRetries:   -1,
	})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// A required Redis must fail readiness when it is down.
func TestReadinessRequiresConfiguredRedis(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, RequestTimeout: 20 * time.Second},
		Redis:  config.RedisConfig{Mode: config.RedisModeRequired},
	}
	srv, err := New(cfg, slog.New(slog.DiscardHandler), Deps{
		Users:         stubUserService{},
		Postgres:      stubPinger{},
		Redis:         unreachableRedis(t),
		RedisRequired: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when required Redis is down, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readiness: %v", err)
	}
	if body.Checks["redis"] != "down" {
		t.Fatalf("want redis down, got %#v", body.Checks)
	}
}

// Optional Redis is genuinely optional: a down client is reported as degraded,
// but the process can still serve traffic.
func TestReadinessAllowsOptionalRedis(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, RequestTimeout: 20 * time.Second},
		Redis:  config.RedisConfig{Mode: config.RedisModeOptional},
	}
	srv, err := New(cfg, slog.New(slog.DiscardHandler), Deps{
		Users:    stubUserService{},
		Postgres: stubPinger{},
		Redis:    unreachableRedis(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for optional Redis, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readiness: %v", err)
	}
	if body.Checks["redis"] != "degraded" {
		t.Fatalf("want redis degraded, got %#v", body.Checks)
	}
}

type countingWriter struct {
	http.ResponseWriter
	headerWrites int
}

func (c *countingWriter) WriteHeader(status int) {
	c.headerWrites++
	c.ResponseWriter.WriteHeader(status)
}

func TestSanitizeRequestID(t *testing.T) {
	router := chi.NewRouter()
	router.Use(sanitizeRequestID)
	router.Use(chimw.RequestID)
	router.Get("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(chimw.GetReqID(r.Context())))
	})

	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"valid", "abc-123", "abc-123"},
		{"too long", strings.Repeat("a", 65), ""},
		{"bad characters", "abc 123", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("X-Request-Id", tt.header)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			got := rec.Body.String()
			if tt.want != "" && got != tt.want {
				t.Fatalf("want request id %q, got %q", tt.want, got)
			}
			if tt.want == "" && got == tt.header {
				t.Fatalf("invalid request id %q was adopted", tt.header)
			}
		})
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

// Optional Redis that was unreachable at startup must report degraded without
// failing readiness; the app can still serve traffic without it.
func TestReadinessReportsOptionalRedisUnavailable(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, RequestTimeout: 20 * time.Second},
		Redis:  config.RedisConfig{Mode: config.RedisModeOptional},
	}
	srv, err := New(cfg, slog.New(slog.DiscardHandler), Deps{
		Users:            stubUserService{},
		Postgres:         stubPinger{},
		RedisUnavailable: errors.New("redis down"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("optional Redis must not fail readiness, got %d", rec.Code)
	}
	var body struct {
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readiness: %v", err)
	}
	if body.Checks["redis"] != "degraded" {
		t.Fatalf("want redis degraded, got %#v", body.Checks)
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

func TestTrustedProxyIgnoresHeaderFromUntrustedPeer(t *testing.T) {
	nets, err := parseTrustedProxyCIDRs("10.0.0.0/8")
	if err != nil {
		t.Fatalf("parse CIDRs: %v", err)
	}
	var got string
	h := trustedProxy(nets)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.RemoteAddr
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got != "203.0.113.9:1234" {
		t.Fatalf("untrusted peer must keep its address, got %q", got)
	}
}

func TestTrustedProxyUsesRightmostUntrustedHop(t *testing.T) {
	nets, err := parseTrustedProxyCIDRs("10.0.0.0/8")
	if err != nil {
		t.Fatalf("parse CIDRs: %v", err)
	}
	for _, tt := range []struct {
		name string
		xff  string
		want string
	}{
		{"single client", "198.51.100.7", "198.51.100.7:0"},
		{"skips trusted hops", "198.51.100.7, 10.0.0.6", "198.51.100.7:0"},
		{"ignores spoofed left hop", "1.1.1.1, 198.51.100.7", "198.51.100.7:0"},
		{"malformed falls back", "garbage", "10.0.0.5:1234"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			h := trustedProxy(nets)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = r.RemoteAddr
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "10.0.0.5:1234"
			req.Header.Set("X-Forwarded-For", tt.xff)
			h.ServeHTTP(httptest.NewRecorder(), req)
			if got != tt.want {
				t.Fatalf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestNewRejectsInvalidTrustedProxyCIDR(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Port: 8080, ReadTimeout: 10 * time.Second, ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
			RequestTimeout: 20 * time.Second, TrustedProxyCIDRs: "not-a-cidr",
		},
		Redis: config.RedisConfig{Mode: config.RedisModeDisabled},
	}
	if _, err := New(cfg, slog.New(slog.DiscardHandler), Deps{Users: stubUserService{}, Postgres: stubPinger{}}); err == nil {
		t.Fatal("want an invalid CIDR error")
	}
}

func TestRequestIDIsEchoed(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-Id", "trace-me-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-Id"); got != "trace-me-123" {
		t.Fatalf("want echoed request id, got %q", got)
	}
}

func TestGeneratedRequestIDIsEchoed(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-Id"); got == "" {
		t.Fatal("want generated request id in response")
	}
}
