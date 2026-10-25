// Package ratelimit provides the shared HTTP rate-limit storage adapter.
package ratelimit

import (
	"github.com/go-chi/httprate"
	httprateredis "github.com/go-chi/httprate-redis"
	"github.com/redis/go-redis/v9"
)

// RedisCounter shares a limit across replicas and fails closed when Redis is
// unavailable. A local fallback would multiply the budget by the replica count;
// the caller turns the error into a 503.
func RedisCounter(client redis.UniversalClient, prefix string) httprate.LimitCounter {
	return httprateredis.NewCounter(&httprateredis.Config{
		Client:           client,
		FallbackDisabled: true,
		PrefixKey:        prefix,
	})
}
