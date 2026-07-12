# go-boilerplate

Production-ready Go HTTP API boilerplate. Follows the community-standard layout
(thin `cmd/`, private code in `internal/`, flat packages — no over-nesting).

## Stack

| Concern       | Choice                                                                 |
| ------------- | ---------------------------------------------------------------------- |
| HTTP router   | [chi](https://github.com/go-chi/chi) (stdlib-compatible)               |
| Database      | PostgreSQL via [pgx/v5](https://github.com/jackc/pgx) connection pool  |
| Query layer   | [sqlc](https://sqlc.dev) — type-safe Go generated from SQL             |
| Cache         | Redis via [go-redis/v9](https://github.com/redis/go-redis), cache-aside |
| Config        | [Viper](https://github.com/spf13/viper) — `config.yaml` + `APP_*` env overrides |
| Logging       | stdlib `log/slog`, JSON in prod, trace-ID correlated                   |
| Tracing       | OpenTelemetry → OTLP/gRPC (Jaeger, Tempo, any collector)               |
| Metrics       | OpenTelemetry → Prometheus exporter on `/metrics`                      |

## Layout

```
cmd/api/              Entry point: calls app.Run(), never changes
internal/
  app/                Lifecycle (app.go, stable) + DI wiring (di.go, grows per resource)
  config/             Viper config loading (yaml + env)
  logger/             slog setup
  observability/      OTel tracer/meter providers
  postgres/           pgxpool construction
  redis/              Redis client lifecycle (shared by all Redis consumers)
  cache/              JSON cache-aside helper (one consumer of the client)
  repository/         sqlc-generated queries (run `make sqlc`) + hand-written Store (tx wrapper)
  service/            Shared service bits: domain errors, storage-error helpers
    user/             User business logic: service.go, model.go, input.go
  handler/            Shared HTTP plumbing: respond, validate
    health/           Liveness/readiness probes
    user/             /users resource: handler.go, request.go, response.go, routes.go
  server/             Router, middleware, http.Server
db/
  migrations/         goose SQL migrations (embedded, applied at boot)
  queries/            sqlc query sources
deploy/prometheus.yml Prometheus scrape config for local stack
```

Request flow: `handler → service → repository (sqlc/pgx)`, with `service`
consulting Redis before Postgres on reads.

## Quickstart

```bash
docker compose up -d      # postgres + redis + jaeger + prometheus
go run ./cmd/api          # or: make run
```

Try it:

```bash
curl -X POST localhost:8080/api/v1/users -d '{"name":"alice","email":"alice@example.com"}'
curl localhost:8080/api/v1/users/1        # second call hits Redis
curl localhost:8080/api/v1/users?limit=10
curl -X PATCH localhost:8080/api/v1/users/1 -d '{"name":"alicia"}'   # partial update
curl -X DELETE localhost:8080/api/v1/users/1
curl localhost:8080/healthz               # liveness
curl localhost:8080/readyz                # readiness (pings postgres + redis)
curl localhost:8080/metrics               # prometheus metrics
```

UIs: Jaeger traces at <http://localhost:16686>, Prometheus at <http://localhost:9090>.

Profiling: pprof runs on a private `localhost`-only port (`server.pprof_port`,
`0` disables) — e.g. `go tool pprof http://localhost:6060/debug/pprof/profile`.
It is never mounted on the public API port.

## Response envelope

Every API response has exactly one shape, enforced centrally in
`internal/handler/respond.go` — resource handlers never touch the encoder:

```jsonc
// success                              // failure
{"data": {...}}                         {"error": {"code": "NOT_FOUND", "message": "..."}}
{"data": [...], "meta": {"limit": 20,   {"error": {"code": "VALIDATION_FAILED", "message": "...",
           "offset": 0}}                           "fields": {"email": "is required"}}}
```

Clients branch on `error.code` (machine-readable), never on `message`.
Health probes (`/healthz`, `/readyz`) intentionally skip the envelope.

## Rate limiting

The `/api/v1` subtree is rate-limited per client IP via
[httprate](https://github.com/go-chi/httprate); health probes and `/metrics`
are never limited. Configure under `ratelimit` (`enabled`, `rps`, `burst`) —
`burst` is the max requests allowed in a 1s window. Over the limit returns
`429` with the standard `RATE_LIMITED` envelope. Set `ratelimit.enabled: false`
(or `APP_RATELIMIT_ENABLED=false`) to disable.

## Configuration

`config.yaml` holds development defaults. Any key is overridable via env var:
`server.port` → `APP_SERVER_PORT`, `postgres.dsn` → `APP_POSTGRES_DSN`,
`otel.enabled` → `APP_OTEL_ENABLED`. In containers, ship no file and configure
entirely through env.

### Dependency policy

- **Postgres is a hard dependency**: unreachable at startup → the process
  exits (fail-fast, let the orchestrator restart it).
- **Redis is a soft dependency**, controlled by `redis.mode`:
  - `disabled` — no Redis at all, service runs uncached.
  - `optional` (default) — log a warning and degrade to uncached reads
    when unreachable; `/readyz` reports Redis as `degraded` but stays 200,
    since the app can still serve traffic without its cache.
  - `required` — unreachable Redis is fatal at startup.

  Any other value fails config validation at startup.

## Development

```bash
make test          # go test -race ./...
make lint          # golangci-lint run
make sqlc          # regenerate internal/repository after editing db/queries/
make docker-build  # distroless production image
```

### Testing

- `make test` — unit tests, fast and dependency-free (`go test -race ./...`).
- `make test-integration` — the integration suite (`-tags integration`).
  Requires Docker: it spins a throwaway `postgres:16-alpine` via
  [testcontainers](https://github.com/testcontainers/testcontainers-go),
  applies the goose migrations, and exercises the repository against it. See
  `internal/repository/integration_test.go` as the template for new resources.

Schema changes use [goose](https://github.com/pressly/goose) migrations under
`db/migrations/`, embedded in the binary and applied at boot when
`postgres.migrate` is true (default; disable it in prod and run goose from CI).
New resource: `make migrate-new name=add_foo` → write the `-- +goose Up`/`Down`
SQL → restart the app (or `make migrate-up`) to apply → add queries in
`db/queries/` → `make sqlc`. sqlc reads the same migration files, so no separate
schema file is kept in sync.

## Extending

- **New resource**: add SQL in `db/queries/`, `make sqlc`, then mirror the
  `user/` layout in both layers: `internal/service/<resource>/` (`service.go`,
  `model.go`, `input.go`) and `internal/handler/<resource>/` (`handler.go`,
  `request.go`, `response.go`, `routes.go`). Wire it up: construct the service
  in `internal/app/di.go`, add a field to `server.Deps`, and mount its routes
  in `internal/server/server.go` — `cmd/` and `app.go` stay untouched.
  (Handler packages import their service aliased: `usersvc ".../service/user"`.) Request DTOs use
  [go-playground/validator](https://github.com/go-playground/validator) tags;
  validation failures return 422 with per-field messages.
- **New binary** (worker, cron): add `cmd/<name>/main.go`, reuse `internal/`.
- **Auth, rate limiting**: add middleware in `internal/server/`.
