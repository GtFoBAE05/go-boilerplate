// Package app owns the application lifecycle: bootstrap infrastructure,
// wire dependencies (see di.go), serve, and shut down gracefully.
// This file is stable; per-resource wiring churn belongs in di.go.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go-boilerplate/internal/config"
	"go-boilerplate/internal/platform/cache"
	"go-boilerplate/internal/platform/postgres"
	"go-boilerplate/internal/platform/redis"
	"go-boilerplate/internal/platform/telemetry"
	"go-boilerplate/internal/server"

	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
)

// otelFlushTimeout bounds the trace flush at exit: if the OTLP collector is
// unreachable, the batch exporter blocks indefinitely and the process never
// exits.
const otelFlushTimeout = 5 * time.Second

// Run boots the app with the given configuration and blocks until the process
// is told to stop. The caller owns loading and validating cfg (see cmd/api).
func Run(cfg *config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := telemetry.NewLogger(cfg.Log.Level,
		telemetry.Format(cfg.Log.Format),
		telemetry.Env(cfg.Service.Env),
	)
	slog.SetDefault(log)
	log.Info("starting", "service", cfg.Service.Name, "build", buildInfo())

	// Unpacked here so the platform packages below take plain values, not *config.Config.
	var otelOpts []telemetry.Option
	if cfg.Tracing.Enabled {
		otelOpts = append(otelOpts, telemetry.WithTracing(
			cfg.Tracing.Endpoint, cfg.Tracing.Insecure, cfg.Tracing.SampleRatio,
		))
	}
	shutdownOtel, err := telemetry.Setup(ctx, cfg.Service.Name, cfg.Service.Env, otelOpts...)
	if err != nil {
		return fmt.Errorf("setup telemetry: %w", err)
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), otelFlushTimeout)
		defer cancel()
		if err := shutdownOtel(flushCtx); err != nil {
			log.Error("otel shutdown failed", "error", err)
		}
	}()

	pool, err := postgres.NewPool(ctx, cfg.Postgres.DSN,
		postgres.MaxConns(cfg.Postgres.MaxConns),
		postgres.MinConns(cfg.Postgres.MinConns),
		postgres.MaxConnLifetime(cfg.Postgres.MaxConnLifetime),
		postgres.QueryExecMode(cfg.Postgres.QueryExecMode),
	)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()
	log.Info("connected to postgres", "postgres", cfg.Postgres)

	if cfg.Postgres.Migrate {
		if err := postgres.Migrate(ctx, pool, log); err != nil {
			return fmt.Errorf("run migrations: %w", err)
		}
	}

	rdb, svcCache, closeRedis, err := setupRedis(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closeRedis()

	redisRequired := cfg.Redis.Mode == config.RedisModeRequired
	srv := server.New(cfg, log, buildDeps(pool, rdb, redisRequired, svcCache, log))
	pprofSrv := server.NewPprofServer(cfg.Pprof.Enabled, cfg.Pprof.Port, log)

	// Each listener runs in the group; the first hard failure cancels runCtx
	// and unblocks the shutdown path below, exactly like a signal would.
	g, runCtx := errgroup.WithContext(ctx)

	g.Go(func() error { return srv.Start() })

	if pprofSrv != nil {
		g.Go(func() error {
			// A dead pprof listener costs profiling, not traffic,
			// so it is logged rather than propagated as a group error.
			if err := pprofSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("pprof listener failed", "error", err)
			}
			return nil
		})
	}

	<-runCtx.Done()

	// Distinguish the two: only a signal means this instance is being taken out
	// of rotation deliberately and should drain. A dead listener is already not
	// serving, so waiting would just delay the error report.
	signalled := ctx.Err() != nil
	log.Info("shutdown initiated", "cause", shutdownCause(signalled))

	// Restore default signal handling now that shutdown has begun, so a second
	// SIGTERM/Ctrl-C kills the process immediately instead of being swallowed
	// by a handler that no longer acts on it. Without this there is no way to
	// escape the drain wait.
	stop()

	if signalled {
		// Fail readiness first, then keep serving for drain_delay. Kubernetes
		// removes endpoints asynchronously, so a listener that closes the
		// instant SIGTERM lands still receives connections — the usual source
		// of 502s during a rolling deploy.
		srv.BeginDrain()
		if d := cfg.Server.DrainDelay; d > 0 {
			log.Info("draining: failing readiness before shutdown", "drain_delay", d)
			time.Sleep(d)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if pprofSrv != nil {
		if err := pprofSrv.Shutdown(shutdownCtx); err != nil {
			log.Error("pprof shutdown failed", "error", err)
		}
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	// Surfaces a listener error (e.g. port already in use) that the group
	// recorded; Shutdown itself makes Start return nil.
	if err := g.Wait(); err != nil {
		return err
	}
	log.Info("shutdown complete")
	return nil
}

func shutdownCause(signalled bool) string {
	if signalled {
		return "signal"
	}
	return "listener failure"
}

// setupRedis honors redis.mode: absent in disabled mode, fatal in required
// mode, best-effort in optional mode. The returned cache stays a nil interface
// (not a typed-nil *cache.Cache) when Redis is unavailable, so the services'
// nil checks keep working.
func setupRedis(ctx context.Context, cfg *config.Config, log *slog.Logger) (
	*goredis.Client, cache.Cache, func(), error,
) {
	noop := func() {}

	if cfg.Redis.Mode == config.RedisModeDisabled {
		log.Info("redis disabled, running without cache")
		return nil, nil, noop, nil
	}

	client, err := redis.New(ctx, cfg.Redis.Addr,
		redis.Password(cfg.Redis.Password),
		redis.DB(cfg.Redis.DB),
	)
	if err != nil {
		if cfg.Redis.Mode == config.RedisModeRequired {
			return nil, nil, noop, fmt.Errorf("connect redis (required): %w", err)
		}
		log.Warn("redis unreachable, running without cache", "error", err)
		return nil, nil, noop, nil
	}

	log.Info("connected to redis", "redis", cfg.Redis)
	return client, cache.New(client, cfg.Cache.DefaultTTL), func() { _ = client.Close() }, nil
}
