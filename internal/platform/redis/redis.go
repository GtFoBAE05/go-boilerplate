// Package redis owns the Redis client lifecycle, analogous to
// internal/platform/postgres for the database pool. Consumers of the client
// (cache, rate limiting, sessions, ...) are wired in cmd/.
package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	goredis "github.com/redis/go-redis/v9"
)

// Option tunes the client before it connects. Unset options keep go-redis'
// own defaults (no password, DB 0).
type Option func(*goredis.Options)

// Password authenticates against a password-protected server.
func Password(p string) Option {
	return func(o *goredis.Options) { o.Password = p }
}

// DB selects the numbered logical database.
func DB(db int) Option {
	return func(o *goredis.Options) { o.DB = db }
}

// New creates a Redis client and verifies connectivity.
func New(ctx context.Context, addr string, opts ...Option) (*goredis.Client, error) {
	clientOpts := &goredis.Options{Addr: addr}
	for _, opt := range opts {
		opt(clientOpts)
	}
	client := goredis.NewClient(clientOpts)

	// Every command becomes a child span of the caller's context, so cache
	// hits/misses show up inside the request trace.
	if err := redisotel.InstrumentTracing(client); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("instrument redis tracing: %w", err)
	}

	// Traces answer "why was this call slow"; these answer "is the pool starved".
	// Reads the global meter provider, so telemetry must be set up first.
	if err := redisotel.InstrumentMetrics(client); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("instrument redis metrics: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}
