# go-boilerplate

[![CI](https://github.com/GtFoBAE05/go-boilerplate/actions/workflows/ci.yml/badge.svg)](https://github.com/GtFoBAE05/go-boilerplate/actions/workflows/ci.yml)
![Go Version](https://img.shields.io/badge/go-1.25-00ADD8?logo=go)

Production-ready Go HTTP API boilerplate: clone it, rename the module, copy the
example resource, and start writing business logic. Community-standard layout
(thin `cmd/`, private code in `internal/`, one grouped `platform/` for infra —
enforced by a linter, not by convention).

## Features

- **Type-safe SQL** — [sqlc](https://sqlc.dev) generates Go from raw SQL; no ORM, no reflection
- **Versioned migrations** — [goose](https://github.com/pressly/goose) files embedded in the binary, applied at boot (or from CI in prod)
- **Cache-aside Redis** — optional by config: disabled / degrade-gracefully / required
- **Full observability** — OTel traces (handler → service → SQL/Redis), Prometheus metrics, trace-correlated `slog` logs
- **Hardened HTTP** — single response envelope, per-IP rate limiting, request timeouts, 1 MiB body cap, strict JSON validation (query params included)
- **Loopback-only pprof** — profiling on its own private port, off by default, never on the public API
- **Validated config** — every knob checked at boot, all errors reported at once, secrets redacted in logs
- **Guardrail CI** — lint, race tests, testcontainers integration suite, pinned `govulncheck`, sqlc drift check, image build, Dependabot
- **Zero-downtime shutdown** — readiness drain before the listener closes, then bounded HTTP drain and OTel flush

## Architecture

```mermaid
flowchart LR
    C[Client] --> M[chi router\nrequest-id · logging · recover\ntimeout · rate limit]
    M --> H[handler\ndecode · validate · envelope]
    H --> S[service\nbusiness logic · domain errors]
    S -->|cache-aside| R[(Redis)]
    S --> Q[repository\nsqlc + pgx]
    Q --> P[(Postgres)]
```

Every layer owns one job: handlers never touch the database, services never
touch HTTP, repositories never contain business rules. Domain errors carry their
own status and code (`service.Error`), so `internal/handler/respond.go` maps
them generically and never grows a case per resource.

## Quickstart

Prerequisites: Go 1.25+, Docker.

```bash
cp .env.example .env      # local settings; without it you get production defaults
docker compose up -d      # postgres + redis + jaeger + prometheus + otel-collector
go run ./cmd/api          # or: make run — applies goose migrations at boot
```

The `.env` step matters: boot-time migrations are **off** by default (production
should not have replicas racing to migrate), and `.env.example` is what turns
them on for local development.

Postgres binds host port **5433** and Redis **6380** (not the defaults) so they
never collide with natively installed instances.

### Endpoints

| Method | Path                 | Description                              |
| ------ | -------------------- | ---------------------------------------- |
| POST   | `/api/v1/users`      | Create user (409 on duplicate email)     |
| GET    | `/api/v1/users/{id}` | Get user (second call served from Redis) |
| GET    | `/api/v1/users`      | List users (`?limit=1..100&offset=`)     |
| PATCH  | `/api/v1/users/{id}` | Partial update (name and/or email)       |
| DELETE | `/api/v1/users/{id}` | Delete user (404 if it never existed)    |
| GET    | `/healthz`           | Liveness probe                           |
| GET    | `/readyz`            | Readiness (pings postgres + redis)       |

`/metrics` and pprof are **not** on this port — see [Observability](#observability).

```bash
curl -X POST localhost:8080/api/v1/users -d '{"name":"alice","email":"alice@example.com"}'
curl localhost:8080/api/v1/users/1
```

## Stack

| Concern       | Choice                                                                 |
| ------------- | ---------------------------------------------------------------------- |
| HTTP router   | [chi](https://github.com/go-chi/chi) (stdlib-compatible)               |
| Database      | PostgreSQL via [pgx/v5](https://github.com/jackc/pgx) connection pool  |
| Query layer   | [sqlc](https://sqlc.dev) — type-safe Go generated from SQL             |
| Migrations    | [goose](https://github.com/pressly/goose) — embedded, applied at boot  |
| Cache         | Redis via [go-redis/v9](https://github.com/redis/go-redis), cache-aside |
| Config        | [Viper](https://github.com/spf13/viper) — env vars + `.env` for dev, validated at boot |
| Logging       | stdlib `log/slog`, JSON in prod, trace-ID correlated                   |
| Tracing       | OpenTelemetry → OTLP/gRPC (Jaeger, Tempo, any collector)               |
| Metrics       | OpenTelemetry → Prometheus exporter on `/metrics`                      |

## Layout

```
cmd/api/              Entry point: loads config, calls app.Run(cfg)
internal/
  app/                Lifecycle (app.go, stable) + DI wiring (di.go, grows per resource)
                      version.go holds -ldflags build provenance
  config/             Config loading + validation (env vars, .env for dev)
  platform/           Generic infra adapters — know nothing about this app
    postgres/         pgxpool construction + goose migration runner
    redis/            Redis client lifecycle (shared by all Redis consumers)
    cache/            JSON cache-aside helper (one consumer of the client)
    telemetry/        slog setup + OTel tracer/meter providers
  repository/         sqlc-generated queries (run `make sqlc`) + hand-written Store (tx wrapper)
  service/            Shared service bits: service.Error type, storage-error helpers
    user/             User business logic: service.go, model.go, input.go
  handler/            Shared HTTP plumbing: respond, validate, page
    health/           Liveness/readiness probes (readiness drains on shutdown)
    user/             /users resource: handler.go, request.go, response.go, routes.go
  server/             Router, middleware, http.Server, private pprof listener
db/
  migrations/         goose SQL migrations (embedded, applied at boot)
  queries/            sqlc query sources
deploy/               Prometheus + OTel collector configs for the local stack
```

`internal/platform/` is the one grouped directory, and it earns the nesting by
having a rule a linter can check: **nothing under it may import
`go-boilerplate/internal`**. A `depguard` rule fails the build otherwise. That is
what keeps infra constructible from a test with no `config.Config` — and what
stops `platform/` from decaying into the `util/`-style dumping ground these
directories usually become. A second rule denies `internal/config` to
`handler/`, `service/` and `repository/`: only the composition root reads config.

## API behavior

### Response envelope

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

### Request guardrails

- **Validation**: DTOs carry [go-playground/validator](https://github.com/go-playground/validator)
  tags; failures return 422 with per-field messages. Unknown JSON fields are
  rejected (typos fail loudly). Bodies over 1 MiB return 413.
- **Pagination**: `?limit`/`?offset` are validated, not clamped — a malformed or
  out-of-range value returns 422 naming the field, rather than silently serving
  a page the client never asked for.
- **Rate limiting**: the `/api/v1` subtree is limited per client IP via
  [httprate](https://github.com/go-chi/httprate); probes are never limited.
  Over the limit → `429` with the `RATE_LIMITED` envelope. Configure under
  `ratelimit`; disable with `RATELIMIT_ENABLED=false`. Counters are
  **per process**: N replicas allow N×`requests`, so use a shared store
  ([httprate-redis](https://github.com/go-chi/httprate-redis)) if you need a
  hard global cap.
- **Timeouts**: every request gets a context deadline (`server.request_timeout`);
  downstream pgx/redis calls abort when it fires and the client gets a `504`
  with the `TIMEOUT` code. A client that disconnects gets no body written.

### Error codes

`error.code` is the stable contract. Transport-level codes (`BAD_REQUEST`,
`VALIDATION_FAILED`, `NOT_FOUND`, `METHOD_NOT_ALLOWED`, `PAYLOAD_TOO_LARGE`,
`RATE_LIMITED`, `TIMEOUT`, `INTERNAL`) live in `internal/handler`. Domain codes
live on the error itself:

```go
// in your resource's service package — no change to the handler layer
var ErrOrderLocked = service.NewError("ORDER_LOCKED", http.StatusConflict, "order is locked")
```

`WriteServiceError` finds the mapping with `errors.As`, so wrapping with `%w`
preserves it and adding a resource never touches shared code.

## Configuration

Everything is configured through environment variables. `.env.example` lists
every one of them, with comments:

```bash
cp .env.example .env      # local development; .env is git-ignored
```

**Precedence: environment variable > `.env` > built-in default.** The built-in
defaults are the production-safe set (`POSTGRES_MIGRATE=false`, JSON logs at
info, `PPROF_ENABLED=false`), so a container that ships no file and sets only
what it needs is already sane. `.env.example` is what opts *local development* into the
convenient values (debug text logs, migrate-on-boot).

There is deliberately **one vocabulary**. An earlier revision had a `config.yaml`
of dotted keys that production never read — every knob had two spellings and the
env names were documented nowhere. `.env` is parsed into a map and layered in as
defaults (never via `os.Setenv`, which would make `Load` non-idempotent), so a
real environment variable still wins for free.

Names are **unprefixed**, which means they share a namespace with everything else
in the environment. Two sections are named defensively as a result, and should
stay that way:

- **`SERVICE_NAME` / `SERVICE_ENV`** rather than bare `NAME` / `ENV`, which are
  generic enough that unrelated tooling sets them.
- **`TRACING_*`** rather than `OTEL_*`, because the OpenTelemetry SDK
  autoconfigures from `OTEL_*` itself (`OTEL_SERVICE_NAME`,
  `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SDK_DISABLED`). Defining our own
  `OTEL_ENABLED` there would squat a namespace another library actively reads —
  an operator setting the real `OTEL_EXPORTER_OTLP_ENDPOINT` would be silently
  ignored.

When adding a key, check the resulting name against what your platform injects.
Kubernetes sets `<SERVICE>_PORT` and `<SERVICE>_SERVICE_HOST` for every Service in
the namespace (`enableServiceLinks`, on by default), so a key spelling out to
`POSTGRES_PORT` or `REDIS_PORT` would be silently overwritten with something like
`tcp://10.96.0.1:5432`. The current names (`POSTGRES_DSN`, `REDIS_ADDR`, …) avoid
that collision.

Adding a knob: register it in `setDefaults` with the value production wants —
registration is mandatory, since Viper's `AutomaticEnv` only resolves keys it
already knows — validate it, then add the dev value to `.env.example`. Two CI
tests keep that file honest: it must produce a valid config, and every key in it
must be one the app actually reads.

Every key is **validated at boot** and all problems are reported together, so a
bad override fails fast with the full list instead of degrading silently:
port ranges, enum values (`app.env`, `log.level`, `log.format`,
`postgres.query_exec_mode`, `redis.mode`), `otel.sample_ratio` ∈ [0,1], and
cross-field rules — `request_timeout < write_timeout` (so the 504 can still be
written) and `shutdown_timeout >= request_timeout` (so shutdown outlasts one
in-flight request). Config structs holding secrets implement `slog.LogValuer`,
so logging a config can't leak a DSN password.

Behind a transaction-mode connection pooler (pgbouncer, RDS Proxy), set
`postgres.query_exec_mode` to `exec` or `simple_protocol` — pgx's default
prepared-statement cache breaks there.

### Dependency policy

- **Postgres is a hard dependency**: unreachable at startup → the process
  exits (fail-fast, let the orchestrator restart it).
- **Redis is a soft dependency**, controlled by `redis.mode`:
  - `disabled` — no Redis at all, service runs uncached.
  - `optional` (default) — log a warning and degrade to uncached reads
    when unreachable; `/readyz` reports Redis as `degraded` but stays 200.
  - `required` — unreachable Redis is fatal at startup.

## Observability

- **Traces**: Jaeger UI at <http://localhost:16686> — spans run
  handler → service → SQL/Redis, correlated with logs via `trace_id`.
- **Metrics**: Prometheus at <http://localhost:9090>, scraping `:8080/metrics`.
- **Profiling**: pprof on a private loopback listener, off by default — set
  `PPROF_ENABLED=true`, then
  `go tool pprof http://localhost:6060/debug/pprof/profile`. While disabled
  nothing is created: no socket, no goroutine.

These are on **two different listeners** because their exposure requirements are
opposite:

| | where | why |
| --- | --- | --- |
| `/metrics` | public port, outside the rate limiter | a scraper must reach it from off-host (in k8s, the pod IP) |
| pprof | `127.0.0.1:PPROF_PORT`, off by default | dumps raw memory, lets any caller trigger expensive profiles |

`/metrics` is unauthenticated and reveals route patterns, latency distributions
and pool sizes — **block it at your ingress if the API is internet-facing.** pprof
has no bind-host option on purpose: reach it with `kubectl port-forward`, never by
widening the bind.

### Shutdown

On SIGTERM the app fails `/readyz` first, keeps serving for
`server.drain_delay`, and only then closes the listener. Kubernetes removes
endpoints asynchronously, so closing immediately is the usual cause of 502s
during a rolling deploy. Liveness stays 200 throughout — failing it would tell
the orchestrator to kill the process mid-drain. A second signal exits at once.

## Development

```bash
make test               # unit tests: go test -race ./... (fast, no Docker)
make test-integration   # integration suite (-tags integration, needs Docker)
make lint               # golangci-lint (CI pins the same version)
make sqlc               # regenerate internal/repository after editing db/queries/
make vuln               # govulncheck at the pinned version
make migrate-new name=x # scaffold a goose migration in db/migrations/
make migrate-up         # apply migrations to the local db
make build              # local binary with version/commit stamped in
make docker-build       # distroless production image, provenance included
```

Tool versions (goose, sqlc, govulncheck, golangci-lint) are pinned as Makefile
variables so local runs and CI can't drift. They stay out of `go.mod` on purpose:
sqlc's dependency tree would otherwise be downloaded by the Dockerfile's
`go mod download` on every image build.

The integration suite spins a throwaway `postgres:16-alpine` via
[testcontainers](https://github.com/testcontainers/testcontainers-go), applies
the goose migrations (guarding them too), and exercises the repository. See
`internal/repository/integration_test.go` — it is the template for new
resources.

### Migrations

Schema changes are goose SQL files under `db/migrations/` (`-- +goose Up` /
`-- +goose Down` in one file), embedded in the binary and applied at boot while
`postgres.migrate` is true — the default for local dev; disable it in prod and
run goose from CI instead — which is the default outside `.env.example`, so the
production image never migrates unless you ask it to. sqlc reads the same
migration files as its schema, so nothing is kept in sync by hand.

## Extending

- **New resource** (mirror the `user` vertical):
  1. `make migrate-new name=create_things` → write the up/down SQL
  2. Add queries in `db/queries/` → `make sqlc`
  3. Copy the layout of `internal/service/user/` and `internal/handler/user/`
     (handlers import their service aliased: `thingsvc ".../service/thing"`)
  4. Wire it: construct the service in `internal/app/di.go`, add a field to
     `server.Deps`, mount the routes in `internal/server/server.go` —
     `cmd/` and `app.go` stay untouched.
- **New binary** (worker, cron): add `cmd/<name>/main.go`, reuse `internal/`.

## Philosophy

A boilerplate should make the decisions everyone agrees on and leave the
contested ones to you. Deliberately **not** included:

- **Auth** — JWT vs sessions vs an external provider is a per-project decision
- **Swagger/OpenAPI** — annotation noise; pick your own spec workflow
- **Mock generators** — interfaces + hand-written fakes are enough (see the tests)
- **CORS / trusted-proxy handling** — depends on your deployment topology;
  add middleware in `internal/server/` to fit it
- **gRPC / message queues / workers** — add them when you need them. The
  lifecycle already runs on an `errgroup`, so a second long-running component is
  one `g.Go` away.
- **Distributed rate limiting** — the in-process limiter is honest about being
  per-replica; swap the store when a global cap actually matters

One complete example vertical (`user`) is the documentation: copy it, rename
it, ship. AI coding agents get their own guidance in [AGENTS.md](AGENTS.md).
