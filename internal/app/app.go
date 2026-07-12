// Package app owns the application lifecycle: bootstrap infrastructure,
// wire dependencies (see di.go), serve, and shut down gracefully.
// This file is stable; per-resource wiring churn belongs in di.go.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go-boilerplate/internal/cache"
	"go-boilerplate/internal/config"
	"go-boilerplate/internal/logger"
	"go-boilerplate/internal/observability"
	"go-boilerplate/internal/postgres"
	"go-boilerplate/internal/redis"
	"go-boilerplate/internal/server"

	goredis "github.com/redis/go-redis/v9"
)

// Run boots the app and blocks until the process is told to stop.
func Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := logger.New(cfg.Log, cfg.App.Env)
	slog.SetDefault(log)

	shutdownOtel, err := observability.Setup(ctx, cfg)
	if err != nil {
		return fmt.Errorf("setup observability: %w", err)
	}
	defer func() {
		// Bound the flush: if the OTLP collector is unreachable, the batch
		// exporter can block indefinitely and the process never exits.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownOtel(ctx); err != nil {
			log.Error("otel shutdown failed", "error", err)
		}
	}()

	pool, err := postgres.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()
	log.Info("connected to postgres")

	if cfg.Postgres.Migrate {
		if err := postgres.Migrate(ctx, pool, log); err != nil {
			return fmt.Errorf("run migrations: %w", err)
		}
	}

	// Redis is a soft dependency; redis.mode decides how strictly to treat it.
	// svcCache stays a nil interface (not a typed-nil *cache.Cache) when
	// Redis is absent, so the service's nil check keeps working.
	var (
		rdb      *goredis.Client
		svcCache cache.Cache
	)
	switch cfg.Redis.Mode {
	case config.RedisModeDisabled:
		log.Info("redis disabled, running without cache")
	default:
		client, err := redis.New(ctx, cfg.Redis)
		if err != nil {
			if cfg.Redis.Mode == config.RedisModeRequired {
				return fmt.Errorf("connect redis (required): %w", err)
			}
			log.Warn("redis unreachable, running without cache", "error", err)
		} else {
			defer func() { _ = client.Close() }()
			rdb = client
			svcCache = cache.New(client, cfg.Cache.DefaultTTL)
			log.Info("connected to redis")
		}
	}

	srv := server.New(cfg, log, buildDeps(pool, rdb, svcCache, log))

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()

	// pprof runs on its own private listener; a failure there is logged but
	// must not take down the API.
	pprofSrv := server.NewPprofServer(cfg.Server.PprofPort, log)
	if pprofSrv != nil {
		go func() {
			if err := pprofSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error("pprof server failed", "error", err)
			}
		}()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received")
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
	return nil
}
