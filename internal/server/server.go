// Package server assembles the chi router, middleware chain, and http.Server.
package server

import (
	"context"
	"errors"
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

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	usersvc "github.com/GtFoBAE05/go-boilerplate/internal/service/user"
)

// maxHeaderBytes caps request headers; it matches net/http's default but keeps
// the limit visible next to the timeouts.
const maxHeaderBytes = 1 << 20

// Server wraps the http.Server with the assembled router and logger.
type Server struct {
	http   *http.Server
	log    *slog.Logger
	health *health.Handler
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

	// Infrastructure handles for health checks. Redis may be nil when disabled
	// or unreachable in optional mode; Postgres is an interface so tests can fake
	// it and typed-nil pools are detectable.
	Postgres health.Pinger
	Redis    *goredis.Client
	// RedisRequired makes a failing Redis ping fail /readyz.
	RedisRequired bool
	// RedisUnavailable carries the startup error when optional Redis could not be
	// reached; Redis is nil then, so /readyz can still report "degraded".
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

	trustedNets, err := parseTrustedProxyCIDRs(cfg.Server.TrustedProxyCIDRs)
	if err != nil {
		return nil, fmt.Errorf("server: %w", err)
	}

	r := chi.NewRouter()

	// Validate the request ID before chi adopts it. An unbounded, arbitrary
	// header would otherwise land in every log line and trace.
	r.Use(sanitizeRequestID)
	r.Use(chimw.RequestID)
	r.Use(trustedProxy(trustedNets))
	// No chi RealIP middleware: it trusts forwarded headers unconditionally.
	// trustedProxy above is the opt-in, CIDR-gated replacement.
	r.Use(routePattern)
	r.Use(requestLogger(log))
	r.Use(recoverPanics(log))
	// Per-request deadline: ctx-aware calls abort when it fires. The middleware
	// writes a fallback 504 only if the handler wrote nothing; WriteServiceError
	// writes the envelope when the handler notices first.
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
	if deps.Redis != nil {
		if deps.RedisRequired {
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
	// No /metrics: metrics are pushed over OTLP, so no unauthenticated scrape
	// endpoint exposes route patterns, latency, or pool sizes.

	r.Route("/api/v1", func(r chi.Router) {
		r.Mount("/users", user.NewHandler(deps.Users, log).Routes())
	})

	// otelhttp creates the server span, extracts trace context, and records HTTP
	// metrics. The filter drops probes so always-fast traffic does not drag down
	// the p99 or dilute the error-ratio denominator.
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

// allowedMethods asks chi which methods match the path. It runs only on a 405, so
// the tree walk is irrelevant; hard-coding Allow would silently rot.
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

// Start blocks until the listener fails or Shutdown is called. Binding happens
// before the "listening" log, so a failed bind never claims to be serving.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return fmt.Errorf("listen http: %w", err)
	}
	s.log.Info("http server listening", "addr", ln.Addr().String())
	if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// Shutdown drains in-flight requests until ctx expires, then closes.
func (s *Server) Shutdown(ctx context.Context) error {
	s.log.Info("shutting down http server")
	return s.http.Shutdown(ctx)
}
