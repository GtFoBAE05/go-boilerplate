// Package redis owns the Redis client lifecycle, analogous to
// internal/platform/postgres for the database pool. Consumers of the client
// (rate limiting, sessions, ...) are wired in app/.
package redis

import (
	"context"
	"crypto/tls"
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

// TLS enables TLS. Managed Redis services normally require it; skipping
// verification is available for a self-signed development endpoint, and the
// config layer rejects that combination in production.
func TLS(enabled, insecureSkipVerify bool) Option {
	return func(o *goredis.Options) {
		if !enabled {
			return
		}
		o.TLSConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: insecureSkipVerify, // #nosec G402 -- explicit opt-in, rejected in production
		}
	}
}

// DialTimeout bounds connection establishment.
func DialTimeout(d time.Duration) Option {
	return func(o *goredis.Options) { o.DialTimeout = d }
}

// ReadTimeout bounds a single read command.
func ReadTimeout(d time.Duration) Option {
	return func(o *goredis.Options) { o.ReadTimeout = d }
}

// WriteTimeout bounds a single write command.
func WriteTimeout(d time.Duration) Option {
	return func(o *goredis.Options) { o.WriteTimeout = d }
}

// PoolSize caps the number of socket connections.
func PoolSize(n int) Option {
	return func(o *goredis.Options) { o.PoolSize = n }
}

// New creates a Redis client and verifies connectivity.
func New(ctx context.Context, addr string, opts ...Option) (*goredis.Client, error) {
	clientOpts := &goredis.Options{Addr: addr}
	for _, opt := range opts {
		opt(clientOpts)
	}
	client := goredis.NewClient(clientOpts)

	// Every command becomes a child span of the caller's context, so Redis
	// latency shows up inside the request trace.
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
