package app

import (
	"log/slog"

	"go-boilerplate/internal/platform/cache"
	"go-boilerplate/internal/repository"
	"go-boilerplate/internal/server"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	usersvc "go-boilerplate/internal/service/user"
)

// buildDeps wires repositories → services into server dependencies.
// This is the file that grows with the app: adding a resource means adding
// its construction here and a field in server.Deps — nothing in app.go
// or cmd/ changes.
func buildDeps(pool *pgxpool.Pool, rdb *goredis.Client, redisRequired bool, svcCache cache.Cache, log *slog.Logger) server.Deps {
	repo := repository.NewStore(pool)

	return server.Deps{
		Users:         usersvc.New(repo, svcCache, log),
		Pool:          pool,
		Redis:         rdb,
		RedisRequired: redisRequired,
	}
}
