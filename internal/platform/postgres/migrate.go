// Package postgres builds the pgx connection pool and applies goose migrations.
package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	migrations "go-boilerplate/db/migrations"
)

// Migrate applies all pending goose migrations against pool. It runs at boot so
// a fresh Postgres (empty data dir) reaches the current schema without a
// separate migration step — right for a boilerplate; production users disable
// postgres.migrate and run goose from CI instead.
//
// goose speaks database/sql, so we open an *sql.DB over the existing pgx pool
// rather than a second connection, and close that handle (not the pool) after.
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
