// Package server assembles the chi router, middleware chain, and http.Server.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"strings"

	"github.com/GtFoBAE05/go-boilerplate/internal/config"
	"github.com/GtFoBAE05/go-boilerplate/internal/handler"
	"github.com/GtFoBAE05/go-boilerplate/internal/handler/health"
	"github.com/GtFoBAE05/go-boilerplate/internal/handler/user"
	"github.com/GtFoBAE05/go-boilerplate/internal/platform/ratelimit"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	usersvc "github.com/GtFoBAE05/go-boilerplate/internal/service/user"
)

// maxHeaderBytes caps the request header size. net/http's default is the same
// 1 MiB, but stating it here keeps the limit visible next to the timeouts: a
// template that never mentions it reads as "unbounded".
const maxHeaderBytes = 1 << 20

// Server wraps the http.Server with the assembled router and logger.
type Server struct {
	http   *http.Server
	log    *slog.Logger
	health *health.Handler
}

// keyByRemoteIP buckets rate-limit counters by r.RemoteAddr, the unspoofable
// TCP peer (see the RealIP note in New). On the rare parse failure we fall
// back to the raw value so limiting still applies.
func keyByRemoteIP(r *http.Request) (string, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr, nil
	}
	return host, nil
}

// rateLimitOptions adds the shared Redis counter when one is configured. With
// no Redis (disabled, or optional and unreachable) the limiter stays
// per-process: fine for development, but the cap is then one budget per replica
// rather than a global one. A Redis error during a request fails closed with a
// 503 instead of silently lifting the limit.
func rateLimitOptions(client *goredis.Client, prefix string, log *slog.Logger) []httprate.Option {
	if client == nil {
		return nil
	}
	return []httprate.Option{
		httprate.WithLimitCounter(ratelimit.RedisCounter(client, prefix)),
		httprate.WithErrorHandler(func(w http.ResponseWriter, r *http.Request, err error) {
			log.ErrorContext(r.Context(), "distributed rate limit failed closed", "prefix", prefix, "error", err)
			w.Header().Set("Retry-After", "1")
			handler.WriteError(w, http.StatusServiceUnavailable, handler.CodeRateLimitUnavailable,
				"rate limiting is temporarily unavailable")
		}),
	}
}

// redisPinger adapts *goredis.Client to health.Pinger.
type redisPinger struct{ rdb *goredis.Client }

type pingerFunc func(context.Context) error

func (f pingerFunc) Ping(ctx context.Context) error { return f(ctx) }

func (p redisPinger) Ping(ctx context.Context) error { return p.rdb.Ping(ctx).Err() }

// Deps carries everything the router needs. Adding a resource adds a field
// here; New's signature never changes.
type Deps struct {
	Users usersvc.UserService

	// Infrastructure handles for health checks. Redis may be nil when
	// disabled or unreachable in optional mode. Postgres is an interface, not
	// *pgxpool.Pool, so a test can supply a fake and so a typed-nil pool is
	// detectable instead of becoming a non-nil Pinger.
	Postgres health.Pinger
	Redis    *goredis.Client
	// RedisRequired makes a failing Redis ping fail /readyz. The caller
	// derives it from redis.mode; New also treats Redis as required when the
	// rate limiter is using it, because the limiter fails closed.
	RedisRequired bool
	// RedisUnavailable is set when redis.mode=optional and the startup ping
	// failed. Redis is nil in that case, so this carries the reason /readyz
	// reports "degraded" instead of omitting the dependency entirely.
	RedisUnavailable error
}

// isNil catches both a nil interface and the typed-nil pointer assigned to it.
// The latter is the classic Go footgun: a nil *pgxpool.Pool stored in a
// health.Pinger is not == nil, so /readyz would panic on the first ping.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// New assembles the router — middleware chain, probes, and every /api/v1
// resource — into a ready-to-start Server.
func New(cfg *config.Config, log *slog.Logger, deps Deps) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("server: config is required")
	}
	if log == nil {
		return nil, fmt.Errorf("server: logger is required")
	}
	if isNil(deps.Postgres) {
		return nil, fmt.Errorf("server: postgres dependency is required")
	}
	if isNil(deps.Users) {
		return nil, fmt.Errorf("server: users service is required")
	}

	r := chi.NewRouter()

	// Validate the request ID before chi adopts it. An unbounded, arbitrary
	// header would otherwise land in every log line and trace.
	r.Use(sanitizeRequestID)
	r.Use(chimw.RequestID)
	// No RealIP middleware on purpose: it trusts X-Forwarded-For / X-Real-IP
	// unconditionally, letting any client spoof its address (and evade the
	// rate limiter). Deployments behind a trusted proxy should add their own
	// middleware that only honors forwarded headers from that proxy.
	r.Use(routePattern)
	r.Use(requestLogger(log))
	r.Use(recoverPanics(log))
	// Per-request deadline: downstream ctx-aware calls (pgx, redis) abort
	// when it fires. The middleware only cancels the context and writes a
	// fallback 504 if the handler wrote nothing; WriteServiceError writes the
	// envelope when the handler notices first.
	r.Use(requestTimeout(cfg.Server.RequestTimeout))

	// Unknown routes and wrong methods use the same error envelope as the
	// API. Registered before Mount so sub-routers inherit both handlers.
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		handler.WriteError(w, http.StatusNotFound, handler.CodeNotFound, "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		if allow := allowedMethods(req, r); allow != "" {
			w.Header().Set("Allow", allow)
		}
		handler.WriteError(w, http.StatusMethodNotAllowed, handler.CodeMethodNotAllowed, "method not allowed")
	})

	required := map[string]health.Pinger{"postgres": deps.Postgres}
	optional := map[string]health.Pinger{}
	// The limiter fails closed when its Redis counter errors. If it is using
	// Redis, readiness must fail too: reporting 200/degraded while every API
	// request is a 503 would keep a broken replica in rotation.
	redisRequiredForReadiness := deps.RedisRequired || (cfg.RateLimit.Enabled && deps.Redis != nil)
	if deps.Redis != nil {
		if redisRequiredForReadiness {
			required["redis"] = redisPinger{rdb: deps.Redis}
		} else {
			optional["redis"] = redisPinger{rdb: deps.Redis}
		}
	} else if deps.RedisUnavailable != nil {
		optional["redis"] = pingerFunc(func(context.Context) error {
			return deps.RedisUnavailable
		})
	}
	healthHandler := health.NewHandler(required, optional, log)
	r.Get("/healthz", healthHandler.Liveness)
	r.Get("/readyz", healthHandler.Readiness)
	// No /metrics on purpose: metrics are pushed over OTLP (see
	// platform/telemetry). A scrape endpoint would put route patterns, latency
	// distributions and pool sizes on an unauthenticated public port.

	r.Route("/api/v1", func(r chi.Router) {
		// Rate limit only the API subtree so health probes — hit constantly by
		// orchestrators — are never throttled. Requests is the per-IP count
		// allowed in each window; over it returns 429 through the standard
		// envelope.
		if cfg.RateLimit.Enabled {
			// Not httprate's KeyByRealIP: it trusts forwarded headers
			// (GHSA-9g5q-2w5x-hmxf).
			options := []httprate.Option{
				httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) {
					retry := int(cfg.RateLimit.Window.Seconds())
					if retry < 1 {
						retry = 1
					}
					w.Header().Set("Retry-After", fmt.Sprintf("%d", retry))
					handler.WriteError(w, http.StatusTooManyRequests, handler.CodeRateLimited, "rate limit exceeded")
				}),
			}
			options = append(options, rateLimitOptions(deps.Redis, cfg.Service.Name+":api", log)...)
			r.Use(httprate.LimitBy(
				cfg.RateLimit.Requests,
				cfg.RateLimit.Window,
				keyByRemoteIP,
				options...,
			))
		}
		r.Mount("/users", user.NewHandler(deps.Users, log).Routes())
	})

	// otelhttp wraps the whole router: creates the server span, extracts
	// incoming trace context, and records HTTP metrics. The filter drops probes
	// from both signals — always-fast probe traffic in the same histogram drags
	// the p99 down and dilutes the error-ratio denominator.
	root := otelhttp.NewHandler(r, "http.server",
		otelhttp.WithFilter(func(r *http.Request) bool { return !isProbe(r.URL.Path) }),
	)

	return &Server{
		http: &http.Server{
			Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
			Handler:           root,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
			ReadTimeout:       cfg.Server.ReadTimeout,
			ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
			WriteTimeout:      cfg.Server.WriteTimeout,
			IdleTimeout:       cfg.Server.IdleTimeout,
			MaxHeaderBytes:    maxHeaderBytes,
		},
		log:    log,
		health: healthHandler,
	}, nil
}

// allowedMethods asks chi which methods match the path. It runs only on a 405,
// so the extra tree walk is irrelevant; the alternative is an Allow header that
// hard-codes this template's routes and silently rots.
func allowedMethods(r *http.Request, router chi.Routes) string {
	var allowed []string
	for _, method := range []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions,
	} {
		if router.Match(chi.NewRouteContext(), method, r.URL.Path) {
			allowed = append(allowed, method)
		}
	}
	return strings.Join(allowed, ", ")
}

// BeginDrain flips /readyz to 503 while continuing to serve traffic, so an
// orchestrator can remove this instance from rotation before Shutdown starts.
func (s *Server) BeginDrain() { s.health.BeginDrain() }

// Start blocks until the listener fails or Shutdown is called.
func (s *Server) Start() error {
	s.log.Info("http server listening", "addr", s.http.Addr)
	if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// Shutdown drains in-flight requests until ctx expires, then closes.
func (s *Server) Shutdown(ctx context.Context) error {
	s.log.Info("shutting down http server")
	return s.http.Shutdown(ctx)
}
