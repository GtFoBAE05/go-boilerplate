// Command migrate applies embedded goose migrations from a dedicated job.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/GtFoBAE05/go-boilerplate/internal/config"
	"github.com/GtFoBAE05/go-boilerplate/internal/platform/postgres"
	"github.com/GtFoBAE05/go-boilerplate/internal/platform/telemetry"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadMigrate()
	if err != nil {
		return err
	}

	log := telemetry.NewLogger(cfg.Log.Level,
		telemetry.Format(cfg.Log.Format),
		telemetry.Env(cfg.Service.Env),
	)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn := cfg.Postgres.MigrationDSN
	if dsn == "" {
		dsn = cfg.Postgres.DSN
	}
	// Migrations are a batch job, not a request: they are bounded by the Job's
	// activeDeadlineSeconds, not the API's per-statement timeout. DDL such as a
	// large CREATE INDEX legitimately runs longer than 10s.
	pool, err := postgres.NewPool(ctx, dsn,
		postgres.MaxConns(2),
		postgres.MinConns(0),
		postgres.MaxConnLifetime(cfg.Postgres.MaxConnLifetime),
		postgres.MaxConnLifetimeJitter(cfg.Postgres.MaxConnLifetimeJitter),
		postgres.ApplicationName(cfg.Service.Name+"-migrate"),
		postgres.QueryExecMode(cfg.Postgres.QueryExecMode),
	)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	log.Info("applying database migrations", "service", cfg.Service.Name)
	if err := postgres.Migrate(ctx, pool, log); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
