// Package postgres builds the pgx connection pool and applies goose migrations.
package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	migrations "github.com/GtFoBAE05/go-boilerplate/db/migrations"
)

// Migrate applies pending goose migrations against pool. Boot-time migration is
// a local convenience; production runs goose from CI. goose needs database/sql,
// so this wraps the pgx pool and closes only that handle, not the pool.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}

	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	log.Info("database migrations applied")
	return nil
}
