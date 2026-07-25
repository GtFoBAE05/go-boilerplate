package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Option tunes the pool before it is opened. Unset options keep pgx's own
// defaults, so callers that only have a DSN (tests, one-off tools) can call
// NewPool with no options and still get the production tracer.
type Option func(*pgxpool.Config)

// MaxConns caps the pool size.
func MaxConns(n int32) Option {
	return func(c *pgxpool.Config) { c.MaxConns = n }
}

// MinConns keeps n connections warm.
func MinConns(n int32) Option {
	return func(c *pgxpool.Config) { c.MinConns = n }
}

// MaxConnLifetime retires a connection after d, regardless of health.
func MaxConnLifetime(d time.Duration) Option {
	return func(c *pgxpool.Config) { c.MaxConnLifetime = d }
}

// QueryExecMode selects pgx's statement protocol by name, as accepted in
// config: cache_statement (pgx's default), cache_describe, describe_exec, exec,
// simple_protocol. An unknown name is ignored, leaving pgx's default — config
// validation is what rejects typos.
//
// This exists because server-side prepared statements, which cache_statement
// relies on, break behind a connection pooler running in transaction mode
// (pgbouncer, RDS Proxy). Such deployments need exec or simple_protocol.
func QueryExecMode(mode string) Option {
	return func(c *pgxpool.Config) {
		m, ok := queryExecModes[mode]
		if !ok {
			return
		}
		c.ConnConfig.DefaultQueryExecMode = m
	}
}

var queryExecModes = map[string]pgx.QueryExecMode{
	"cache_statement": pgx.QueryExecModeCacheStatement,
	"cache_describe":  pgx.QueryExecModeCacheDescribe,
	"describe_exec":   pgx.QueryExecModeDescribeExec,
	"exec":            pgx.QueryExecModeExec,
	"simple_protocol": pgx.QueryExecModeSimpleProtocol,
}

// NewPool creates a pgx connection pool and verifies connectivity.
func NewPool(ctx context.Context, dsn string, opts ...Option) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	for _, opt := range opts {
		opt(poolCfg)
	}

	// Every query becomes a child span of the caller's context, so traces
	// run handler → service → SQL. Span names carry the trimmed statement.
	poolCfg.ConnConfig.Tracer = otelpgx.NewTracer(otelpgx.WithTrimSQLInSpanName())

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	// Pool saturation never surfaces as an error, only as latency inside the
	// acquire. Reads the global meter provider, so telemetry must be set up first.
	if err := otelpgx.RecordStats(pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("record postgres pool stats: %w", err)
	}
	return pool, nil
}
