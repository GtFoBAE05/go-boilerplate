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

	"github.com/GtFoBAE05/go-boilerplate/internal/config"
	"github.com/GtFoBAE05/go-boilerplate/internal/platform/postgres"
	"github.com/GtFoBAE05/go-boilerplate/internal/platform/redis"
	"github.com/GtFoBAE05/go-boilerplate/internal/platform/telemetry"
	"github.com/GtFoBAE05/go-boilerplate/internal/server"

	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
)

// otelFlushTimeout bounds the trace flush at exit so an unreachable collector
// cannot block shutdown forever.
const otelFlushTimeout = 5 * time.Second

// Run boots the app and blocks until shutdown; the caller loads and validates cfg.
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
	otelOpts := []telemetry.Option{telemetry.WithBuild(version, commit)}
	if cfg.OTel.TracesEnabled() {
		sampler, err := telemetry.NewSampler(cfg.OTel.TracesSampler, cfg.OTel.TracesSamplerArg)
		if err != nil {
			// Unreachable via Load, which validates the name.
			return fmt.Errorf("configure trace sampler: %w", err)
		}
		otelOpts = append(otelOpts, telemetry.WithTracing(cfg.OTel.TraceEndpoint(), sampler))
	}
	if cfg.OTel.MetricsEnabled() {
		otelOpts = append(otelOpts, telemetry.WithMetrics(
			cfg.OTel.MetricEndpoint(), cfg.OTel.MetricExportInterval(),
		))
	}
	shutdownOtel, err := telemetry.Setup(ctx, cfg.Service.Name, cfg.Service.Env, otelOpts...)
	if err != nil {
		return fmt.Errorf("setup telemetry: %w", err)
	}
	// A wrong endpoint is invisible at runtime except as missing data: nothing
	// is scrapeable from this process.
	log.Info("telemetry configured",
		"traces", exportTarget(cfg.OTel.TracesEnabled(), cfg.OTel.TraceEndpoint()),
		"metrics", exportTarget(cfg.OTel.MetricsEnabled(), cfg.OTel.MetricEndpoint()),
		"metric_export_interval", cfg.OTel.MetricExportInterval(),
	)
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

	rdb, redisUnavailable, closeRedis, err := setupRedis(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closeRedis()

	redisRequired := cfg.Redis.Mode == config.RedisModeRequired
	srv, err := server.New(cfg, log, buildDeps(pool, rdb, redisUnavailable, redisRequired, log))
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}
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

	// Only a signal means this instance is being taken out of rotation and should
	// drain; a dead listener is already not serving.
	signalled := ctx.Err() != nil
	log.Info("shutdown initiated", "cause", shutdownCause(signalled))

	// Restore default signal handling so a second SIGTERM/Ctrl-C can still kill
	// the process instead of being swallowed during the drain wait.
	stop()

	if signalled {
		// Fail readiness first, then keep serving for drain_delay: Kubernetes
		// removes endpoints asynchronously, so closing immediately causes 502s.
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

func exportTarget(enabled bool, endpoint string) string {
	if !enabled {
		return "disabled"
	}
	return endpoint
}

func shutdownCause(signalled bool) string {
	if signalled {
		return "signal"
	}
	return "listener failure"
}

// setupRedis honors redis.mode: absent when disabled, fatal when required,
// best-effort when optional.
func setupRedis(ctx context.Context, cfg *config.Config, log *slog.Logger) (
	*goredis.Client, error, func(), error,
) {
	noop := func() {}

	if cfg.Redis.Mode == config.RedisModeDisabled {
		log.Info("redis disabled, running without redis-backed features")
		return nil, nil, noop, nil
	}

	client, err := redis.New(ctx, cfg.Redis.Addr,
		redis.Password(cfg.Redis.Password),
		redis.DB(cfg.Redis.DB),
		redis.TLS(cfg.Redis.TLSEnabled, cfg.Redis.TLSInsecureSkipVerify),
		redis.DialTimeout(cfg.Redis.DialTimeout),
		redis.ReadTimeout(cfg.Redis.ReadTimeout),
		redis.WriteTimeout(cfg.Redis.WriteTimeout),
		redis.PoolSize(cfg.Redis.PoolSize),
	)
	if err != nil {
		if cfg.Redis.Mode == config.RedisModeRequired {
			return nil, nil, noop, fmt.Errorf("connect redis (required): %w", err)
		}
		log.Warn("redis unreachable, running without redis-backed features", "error", err)
		return nil, err, noop, nil
	}

	log.Info("connected to redis", "redis", cfg.Redis)
	return client, nil, func() { _ = client.Close() }, nil
}
