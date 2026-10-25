// Package ratelimit provides the shared HTTP rate-limit storage adapter.
package ratelimit

import (
	"github.com/go-chi/httprate"
	httprateredis "github.com/go-chi/httprate-redis"
	"github.com/redis/go-redis/v9"
)

// RedisCounter shares a limit across replicas and fails closed when Redis is
// unavailable.
//
// Fail-closed is the deliberate choice: an in-process fallback would keep
// serving traffic with a budget multiplied by the replica count, which is
// exactly the failure the shared counter exists to prevent. The caller supplies
// the error handler that turns this into a 503.
func RedisCounter(client redis.UniversalClient, prefix string) httprate.LimitCounter {
	return httprateredis.NewCounter(&httprateredis.Config{
		Client:           client,
		FallbackDisabled: true,
		PrefixKey:        prefix,
	})
}
