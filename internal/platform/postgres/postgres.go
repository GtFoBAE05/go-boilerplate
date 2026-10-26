package postgres

import (
	"context"
	"fmt"
	"strconv"
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

// MaxConnLifetimeJitter spreads retirements around MaxConnLifetime so replicas
// do not reconnect in lockstep.
func MaxConnLifetimeJitter(d time.Duration) Option {
	return func(c *pgxpool.Config) { c.MaxConnLifetimeJitter = d }
}

// StatementTimeout sets Postgres's server-side per-statement deadline.
func StatementTimeout(d time.Duration) Option {
	return func(c *pgxpool.Config) { setRuntimeMillis(c, "statement_timeout", d) }
}

// LockTimeout bounds how long a statement waits for a lock.
func LockTimeout(d time.Duration) Option {
	return func(c *pgxpool.Config) { setRuntimeMillis(c, "lock_timeout", d) }
}

// IdleInTransactionTimeout reaps a transaction abandoned by a dead client.
func IdleInTransactionTimeout(d time.Duration) Option {
	return func(c *pgxpool.Config) {
		setRuntimeMillis(c, "idle_in_transaction_session_timeout", d)
	}
}

// ApplicationName labels this process in pg_stat_activity and slow-query logs.
func ApplicationName(name string) Option {
	return func(c *pgxpool.Config) {
		if c.ConnConfig.RuntimeParams == nil {
			c.ConnConfig.RuntimeParams = map[string]string{}
		}
		c.ConnConfig.RuntimeParams["application_name"] = name
	}
}

func setRuntimeMillis(c *pgxpool.Config, key string, d time.Duration) {
	if c.ConnConfig.RuntimeParams == nil {
		c.ConnConfig.RuntimeParams = map[string]string{}
	}
	c.ConnConfig.RuntimeParams[key] = strconv.FormatInt(d.Milliseconds(), 10)
}

// QueryExecMode selects pgx's statement protocol by name. An unknown name is
// ignored, leaving pgx's default; config validation rejects typos. Transaction-
// mode poolers (pgbouncer, RDS Proxy) break server-side prepared statements, so
// those deployments need exec or simple_protocol.
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
	// run handler → service → SQL. otelpgx v0.13 names spans after the SQL
	// operation by default; WithTrimSQLInSpanName is deprecated there.
	poolCfg.ConnConfig.Tracer = otelpgx.NewTracer()

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
