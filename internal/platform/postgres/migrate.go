// Package postgres builds the pgx connection pool and applies goose migrations.
package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	migrations "github.com/GtFoBAE05/go-boilerplate/db/migrations"
)

// Migrate applies pending goose migrations against pool. The advisory lock
// makes concurrent migrators (a rollout, a retried job) serialize instead of
// racing; goose needs database/sql, so this wraps the pgx pool and closes only
// that handle, not the pool.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("create migration locker: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS,
		goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	applied, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	log.Info("database migrations applied", "count", len(applied))
	return nil
}
