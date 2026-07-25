// Package server assembles the chi router, middleware chain, and http.Server.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"go-boilerplate/internal/config"
	"go-boilerplate/internal/handler"
	"go-boilerplate/internal/handler/health"
	"go-boilerplate/internal/handler/user"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	usersvc "go-boilerplate/internal/service/user"
)

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

// redisPinger adapts *goredis.Client to health.Pinger.
type redisPinger struct{ rdb *goredis.Client }

func (p redisPinger) Ping(ctx context.Context) error { return p.rdb.Ping(ctx).Err() }

// Deps carries everything the router needs. Adding a resource adds a field
// here; New's signature never changes.
type Deps struct {
	Users usersvc.Service

	// Infrastructure handles for health checks. Redis may be nil when
	// disabled or unreachable in optional mode.
	Pool  *pgxpool.Pool
	Redis *goredis.Client
	// RedisRequired makes a failing Redis ping fail /readyz. The caller
	// derives it from redis.mode; the server never reads config for it.
	RedisRequired bool
}

// New assembles the router — middleware chain, probes, /metrics, and every
// /api/v1 resource — into a ready-to-start Server.
func New(cfg *config.Config, log *slog.Logger, deps Deps) *Server {
	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	// No RealIP middleware on purpose: it trusts X-Forwarded-For / X-Real-IP
	// unconditionally, letting any client spoof its address (and evade the
	// rate limiter). Deployments behind a trusted proxy should add their own
	// middleware that only honors forwarded headers from that proxy.
	r.Use(spanRoutePattern)
	r.Use(requestLogger(log))
	r.Use(recoverPanics(log))
	// Per-request deadline: downstream ctx-aware calls (pgx, redis) abort
	// when it fires and the client gets a 504.
	r.Use(chimw.Timeout(cfg.Server.RequestTimeout))

	// Unknown routes and wrong methods use the same error envelope as the
	// API. Registered before Mount so sub-routers inherit both handlers.
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		handler.WriteError(w, http.StatusNotFound, handler.CodeNotFound, "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		handler.WriteError(w, http.StatusMethodNotAllowed, handler.CodeMethodNotAllowed, "method not allowed")
	})

	required := map[string]health.Pinger{"postgres": deps.Pool}
	optional := map[string]health.Pinger{}
	if deps.Redis != nil {
		if deps.RedisRequired {
			required["redis"] = redisPinger{rdb: deps.Redis}
		} else {
			optional["redis"] = redisPinger{rdb: deps.Redis}
		}
	}
	healthHandler := health.NewHandler(required, optional)
	r.Get("/healthz", healthHandler.Liveness)
	r.Get("/readyz", healthHandler.Readiness)
	// Registered outside the /api/v1 subtree so it is never rate-limited: a
	// throttled scrape shows up as a monitoring gap during an incident. It's
	// unauthenticated and reveals route patterns and pool sizes, so block
	// /metrics at your ingress if the API is internet-facing.
	r.Handle("/metrics", promhttp.Handler())

	r.Route("/api/v1", func(r chi.Router) {
		// Rate limit only the API subtree so health probes — hit constantly by
		// orchestrators — are never throttled. Requests is the per-IP count
		// allowed in each window; over it returns 429 through the standard
		// envelope.
		if cfg.RateLimit.Enabled {
			// Not httprate's KeyByRealIP: it trusts forwarded headers
			// (GHSA-9g5q-2w5x-hmxf).
			r.Use(httprate.LimitBy(
				cfg.RateLimit.Requests,
				cfg.RateLimit.Window,
				keyByRemoteIP,
				httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) {
					handler.WriteError(w, http.StatusTooManyRequests, handler.CodeRateLimited, "rate limit exceeded")
				}),
			))
		}
		r.Mount("/users", user.NewHandler(deps.Users, log).Routes())
	})

	// otelhttp wraps the whole router: creates the server span, extracts
	// incoming trace context, and records HTTP metrics.
	root := otelhttp.NewHandler(r, "http.server")

	return &Server{
		http: &http.Server{
			Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
			Handler:      root,
			ReadTimeout:  cfg.Server.ReadTimeout,
			WriteTimeout: cfg.Server.WriteTimeout,
			IdleTimeout:  cfg.Server.IdleTimeout,
		},
		log:    log,
		health: healthHandler,
	}
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
