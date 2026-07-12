// Package cache provides the object cache consumed by services. The
// contract is codec-agnostic — the Redis implementation happens to encode
// JSON, but callers must not rely on that. Non-object Redis usecases
// (counters, locks, rate limits) should use the shared *redis.Client from
// internal/redis directly instead of going through this package.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Cache is the object-cache contract. Services hold it as a nilable field:
// nil means "run uncached"; tests substitute a fake.
type Cache interface {
	// Get loads the value stored at key into dest. Returns false on miss.
	Get(ctx context.Context, key string, dest any) (bool, error)
	// Set stores val at key with the configured TTL.
	Set(ctx context.Context, key string, val any) error
	// Delete removes keys; missing keys are not an error.
	Delete(ctx context.Context, keys ...string) error
}

// redisCache implements Cache on Redis, encoding values as JSON.
type redisCache struct {
	rdb goredis.Cmdable
	ttl time.Duration
}

var _ Cache = (*redisCache)(nil)

// New builds the Redis-backed Cache; every Set uses ttl as its expiry.
func New(rdb goredis.Cmdable, ttl time.Duration) Cache {
	return &redisCache{rdb: rdb, ttl: ttl}
}

func (c *redisCache) Get(ctx context.Context, key string, dest any) (bool, error) {
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("redis get %q: %w", key, err)
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return false, fmt.Errorf("unmarshal cached %q: %w", key, err)
	}
	return true, nil
}

func (c *redisCache) Set(ctx context.Context, key string, val any) error {
	raw, err := json.Marshal(val)
	if err != nil {
		return fmt.Errorf("marshal for cache %q: %w", key, err)
	}
	if err := c.rdb.Set(ctx, key, raw, c.ttl).Err(); err != nil {
		return fmt.Errorf("redis set %q: %w", key, err)
	}
	return nil
}

func (c *redisCache) Delete(ctx context.Context, keys ...string) error {
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("redis del %v: %w", keys, err)
	}
	return nil
}
