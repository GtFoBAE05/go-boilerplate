# AGENTS.md

Guidance for AI coding agents (Claude Code, Cursor, Copilot, Codex, ...)
working in this repository.

## Commands

```bash
docker compose up -d        # postgres(5433) redis(6380) jaeger prometheus otel-collector
go run ./cmd/api            # run the API (auto-applies goose migrations on boot)
make test                   # unit tests with -race
make test-integration       # integration tests (needs Docker; -tags integration)
make lint                   # golangci-lint (CI pins v2.12.2 — keep local in sync)
make sqlc                   # regenerate internal/repository after editing db/queries/
make migrate-new name=foo   # create a new goose migration in db/migrations/
```

Run a single test: `go test -run TestName ./internal/service/user/`.
Integration tests live behind the `integration` build tag so plain `go test ./...` stays Docker-free.

Host ports are 5433 (Postgres) and 6380 (Redis) because native installs occupy 5432/6379 on this machine — never change these mappings.

On this Windows machine `-race` fails (`cc1.exe: 64-bit mode not compiled in` — 32-bit gcc); run tests without `-race` locally and let CI cover the race detector.

## Architecture

Request flow: `internal/handler/<resource>` → `internal/service/<resource>` → `internal/repository` (sqlc/pgx), with the service consulting Redis (cache-aside) before Postgres on reads.

The cache-aside in `service/user` **demonstrates the pattern; it is not a recommendation to cache every read.** A primary-key lookup is sub-millisecond in Postgres — caching it buys no latency and adds an invalidation surface. When copying the vertical, delete the four `s.cache != nil` blocks unless the new resource has measured read pressure.

**Adding a resource** is a copy-paste of the `user` vertical — it exists as the template:
1. `make migrate-new` → write SQL in `db/migrations/` (goose format, up+down in one file)
2. Add queries in `db/queries/` → `make sqlc` (sqlc reads the migrations dir as schema)
3. Copy `internal/service/user/` and `internal/handler/user/`, adapt
4. Wire it in `internal/app/di.go` (add construction) and `internal/server/server.go` (add a `Deps` field + `r.Mount`). `app.go` and `cmd/` never change.

**Non-negotiable invariants:**
- Every HTTP response goes through `internal/handler/respond.go` (`OK`/`OKList`/`WriteError`/`WriteServiceError`). Never write to the ResponseWriter directly; never use `http.Error`. Envelope shape: `{"data": ...}` / `{"error": {"code", "message", "fields"?}}` with machine-readable codes.
- Domain errors live in `internal/service/errors.go`; services map storage errors (e.g. `service.IsUniqueViolation`) to them, and `WriteServiceError` maps them to HTTP once for all resources. Unmapped errors become logged 500s.
- Request decoding goes through `handler.DecodeValid[T]` — it enforces the 1 MiB body cap, unknown-field rejection, and `validate` struct tags.
- Config: every knob is yaml in `config.yaml` + `APP_*` env override + a default and validation in `internal/config`. Never read env vars directly.
- Only the composition layer imports `internal/config`: `app/` (unpacks it) and `server/` (app-specific by nature). Infra packages — `postgres`, `redis`, `logger`, `observability`, `cache` — take plain values plus functional options, so tests and tools can construct them without a `config.Config`. Policy decisions (is tracing on, is Redis required) are resolved in `app.go` and passed down as data, not re-read from config downstream.
- Cache is nilable by design: `cache.Cache` interface, nil means run uncached (redis.mode=disabled/optional). Keep nil checks when touching services; never pass a typed-nil `*cache.Cache` (see app.go comment).
- Transactions: read-modify-write goes through `repository.Store.ExecTx` with `FOR UPDATE` (see user Update).
- `internal/repository` is sqlc-generated except `store.go` — never hand-edit generated files; CI's `sqlc diff` job fails on drift.

**Deliberate decisions — do not "fix" these:**
- No RealIP middleware: forwarded headers are spoofable; rate limiting keys off the TCP peer (`RemoteAddr`). Deployments behind a trusted proxy add their own middleware.
- No auth, swagger, mock generators, CORS — out of scope for this boilerplate by decision.
- The app is intentionally NOT in docker-compose (infra only); it runs natively for fast iteration. `Dockerfile` is the production image.
- goose over golang-migrate; migrations are embedded (`db/migrations/migrations.go`) and auto-applied at boot when `postgres.migrate: true` (production flips it off and migrates from CI).
- pprof listens on a separate private `127.0.0.1:6060` listener (`server.pprof_port`), never on the public router.

**Observability is wired end-to-end:** otelhttp wraps the router, otelpgx traces queries, redisotel traces cache calls; handlers/services start their own spans; slog lines auto-carry `trace_id`/`span_id` via the logger's trace-aware handler. Follow this pattern in new code (tracer per package, `WarnContext`/`ErrorContext` with ctx).

## Conventions

- Comments explain *why* or a contract, never *what*. Every exported symbol has a doc comment (revive enforces this).
- Import order: stdlib / third-party / `go-boilerplate/...` last. `.golangci.yml` sets `gofumpt.module-path` because the dot-less module name otherwise confuses gofumpt — don't remove it.
- Line endings are LF everywhere (`.gitattributes` enforces); CRLF breaks gofumpt.
- Tests use hand-written fakes (`fakeStore`, `mockUserService`, `stubUserService` patterns) — no mock generation tooling.
- `internal/repository/integration_test.go` is the template integration test: testcontainers Postgres + real goose migrations + full CRUD.
