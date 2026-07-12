// Package redis owns the Redis client lifecycle, analogous to
// internal/postgres for the database pool. Consumers of the client
// (cache, rate limiting, sessions, ...) are wired in cmd/.
package redis

import (
	"context"
	"fmt"
	"time"

	"go-boilerplate/internal/config"

	"github.com/redis/go-redis/extra/redisotel/v9"
	goredis "github.com/redis/go-redis/v9"
)

// New creates a Redis client and verifies connectivity.
func New(ctx context.Context, cfg config.RedisConfig) (*goredis.Client, error) {
	client := goredis.NewClient(&goredis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	// Every command becomes a child span of the caller's context, so cache
	// hits/misses show up inside the request trace.
	if err := redisotel.InstrumentTracing(client); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("instrument redis tracing: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}
