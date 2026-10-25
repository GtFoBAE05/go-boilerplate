package app

import (
	"log/slog"

	"github.com/GtFoBAE05/go-boilerplate/internal/repository"
	"github.com/GtFoBAE05/go-boilerplate/internal/server"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	usersvc "github.com/GtFoBAE05/go-boilerplate/internal/service/user"
)

// buildDeps wires repositories → services into server dependencies. Adding a
// resource means adding its construction here and a field in server.Deps.
func buildDeps(pool *pgxpool.Pool, rdb *goredis.Client, redisUnavailable error, redisRequired bool, log *slog.Logger) server.Deps {
	repo := repository.NewStore(pool)

	return server.Deps{
		Users:            usersvc.NewUserService(repo, log),
		Postgres:         pool,
		Redis:            rdb,
		RedisRequired:    redisRequired,
		RedisUnavailable: redisUnavailable,
	}
}
