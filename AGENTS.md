# AGENTS.md

Guidance for AI coding agents (Claude Code, Cursor, Copilot, Codex, ...)
working in this repository.

## Commands

```bash
cp .env.example .env        # required first: defaults are production-safe, so no .env means no boot-time migrations
docker compose up -d        # postgres(5433) redis(6380) jaeger prometheus otel-collector
go run ./cmd/api            # run the API (applies goose migrations on boot when .env sets POSTGRES_MIGRATE=true)
make test                   # unit tests with -race
make test-integration       # integration tests (needs Docker; -tags integration)
make lint                   # golangci-lint (CI pins v2.12.2 — keep local in sync)
make sqlc                   # regenerate internal/repository after editing db/queries/
make sqlc-diff              # same staleness check CI runs
make migrate-new name=foo   # create a new goose migration in db/migrations/
make vuln                   # govulncheck at the pinned version
```

Run a single test: `go test -run TestName ./internal/service/user/`.
Integration tests live behind the `integration` build tag so plain `go test ./...` stays Docker-free.

Host ports are 5433 (Postgres) and 6380 (Redis) because native installs occupy 5432/6379 on this machine — never change these mappings.

On this Windows machine `-race` fails (`cc1.exe: 64-bit mode not compiled in` — 32-bit gcc); run tests without `-race` locally and let CI cover the race detector. The same 32-bit gcc breaks `sqlc` unless CGO is off — the Makefile already sets `CGO_ENABLED=0` for it.

Tool versions (goose, sqlc, govulncheck) are pinned as Makefile variables, **not** `go.mod` tool directives: sqlc's dependency tree (wazero, the tidb parser, cel-go) would otherwise be pulled by the Dockerfile's `go mod download` on every image build. Bump them in the Makefile and `.github/workflows/ci.yml` together.

## Architecture

Request flow: `internal/handler/<resource>` → `internal/service/<resource>` → `internal/repository` (sqlc/pgx), with the service consulting Redis (cache-aside) before Postgres on reads.

The cache-aside in `service/user` **demonstrates the pattern; it is not a recommendation to cache every read.** A primary-key lookup is sub-millisecond in Postgres — caching it buys no latency and adds an invalidation surface. When copying the vertical, delete the four `s.cache != nil` blocks unless the new resource has measured read pressure.

**Adding a resource** is a copy-paste of the `user` vertical — it exists as the template:
1. `make migrate-new` → write SQL in `db/migrations/` (goose format, up+down in one file)
2. Add queries in `db/queries/` → `make sqlc` (sqlc reads the migrations dir as schema)
3. Copy `internal/service/user/` and `internal/handler/user/`, adapt
4. Wire it in `internal/app/di.go` (add construction) and `internal/server/server.go` (add a `Deps` field + `r.Mount`). `app.go`, `cmd/` and `internal/platform/` never change.

Adding a resource must not require editing `internal/handler/respond.go` or `internal/service/errors.go`. If it does, the error model has regressed — see below.

**Non-negotiable invariants:**
- Every HTTP response goes through `internal/handler/respond.go` (`OK`/`OKList`/`WriteError`/`WriteServiceError`). Never write to the ResponseWriter directly; never use `http.Error`. Envelope shape: `{"data": ...}` / `{"error": {"code", "message", "fields"?}}` with machine-readable codes.
- `WriteJSON` marshals into a buffer *before* writing any header. Never encode straight to the ResponseWriter: that commits a 200 first, so a mid-stream marshal failure would ship a truncated body under a success status.
- **Domain errors are self-describing.** `service.Error` carries `Code`, `Status` and `Message`; `WriteServiceError` reads those via `errors.As` and therefore contains *no* per-resource cases. New resource-specific errors are declared with `service.NewError(...)` in that resource's own package — never by adding a case to the shared handler switch. Services wrap with `%w` so the mapping survives.
- Context errors are checked **before** the domain error in `WriteServiceError`: an expired request is a 504 and a cancelled one writes nothing, because a timeout is a transport outcome and not a server fault. `chimw.Timeout` only cancels the context — this mapping is what actually produces the 504.
- Request decoding goes through `handler.DecodeValid[T]` — it enforces the 1 MiB body cap, unknown-field rejection, and `validate` struct tags.
- List endpoints read pagination through `handler.DecodePage`, which **rejects** a malformed or out-of-range `limit`/`offset` with 422 instead of silently clamping. Silent clamping hands the client a page it never asked for and contradicts `DecodeValid`'s strictness on the body path.
- Writes that report rows-affected map 0 rows to `service.ErrNotFound` (see `DeleteUser :execrows`) rather than answering a silent 204.
- Config: every knob is an environment variable + a default **and validation** in `internal/config`, documented in `.env.example`. Never read env vars directly outside that package. `validate()` accumulates every problem with `errors.Join` so one boot surfaces the whole list. Two CI tests keep `.env.example` honest: it must produce a valid config, and every key in it must be one the app actually reads (a typo there is invisible at runtime — the setting is simply ignored forever).
- `config.Load()` is called in `cmd/api/main.go`, not in `app.Run` — `Run(cfg *config.Config)` takes a ready config so it stays callable from a test or a second binary with a config built in code, no file and no environment.
- Anything holding a secret implements `slog.LogValuer` (`PostgresConfig`, `RedisConfig`). `log.Info("...", "postgres", cfg.Postgres)` must never be able to print a password.
- **`internal/platform/` holds generic infrastructure adapters** (`postgres`, `redis`, `cache`, `telemetry`). They take plain values plus functional options and know nothing about this application, so tests and tools can construct them without a `config.Config`. **This is enforced, not documented**: a `depguard` rule fails the build if anything under `internal/platform/` imports `go-boilerplate/internal`. That rule is the whole justification for the directory — without it, `platform/` becomes the dumping ground every such directory turns into. Don't add something there that needs app knowledge; resolve the decision in `app.go` and pass it down as data.
- Only the composition layer imports `internal/config`: `cmd/api` (loads it), `app/` (unpacks it) and `server/` (app-specific by nature). A second `depguard` rule denies `internal/config` to `handler/`, `service/` and `repository/`. Policy decisions (is tracing on, is Redis required) are resolved in `app.go` and passed down as data, not re-read from config downstream.
- Cache is nilable by design: `cache.Cache` interface, nil means run uncached (redis.mode=disabled/optional). Keep nil checks when touching services; never pass a typed-nil `*cache.Cache` (see the `setupRedis` comment in app.go).
- Transactions: read-modify-write goes through `repository.Store.ExecTx` with `FOR UPDATE` (see user Update).
- `internal/repository` is sqlc-generated except `store.go` — never hand-edit generated files; CI's `sqlc diff` job fails on drift.

**Deliberate decisions — do not "fix" these:**
- No RealIP middleware: forwarded headers are spoofable; rate limiting keys off the TCP peer (`RemoteAddr`). Deployments behind a trusted proxy add their own middleware.
- The rate limiter counts **in-process**: N replicas allow N×`ratelimit.requests`. That is documented, not overlooked — swap in `httprate-redis` before treating it as a hard global cap.
- No auth, swagger, mock generators, CORS — out of scope for this boilerplate by decision.
- The app is intentionally NOT in docker-compose (infra only); it runs natively for fast iteration. `Dockerfile` is the production image.
- goose over golang-migrate; migrations are embedded (`db/migrations/migrations.go`) and auto-applied at boot when `POSTGRES_MIGRATE=true` — which `.env.example` sets for dev only. The default is off, so production migrates from CI unless explicitly told otherwise.
- Offset pagination is the default because it is what a fresh project needs. It degrades past roughly 10k rows — switch that resource to keyset pagination then; `handler.Page.Meta()` is the one place the response contract is built.
- Build provenance lives in `internal/app/version.go` (unexported vars + `buildInfo()`), not its own package: `app` is the only consumer and the vars encode no decision. `debug.ReadBuildInfo` is not the source because `.dockerignore` excludes `.git`, so a container build has no VCS data. Values arrive via `-ldflags -X go-boilerplate/internal/app.version=…` — the Makefile and Dockerfile must agree on that path.
- `logger` and `observability` are one package (`platform/telemetry`) because they are one seam, not two: the logger's handler reads the active span to stamp `trace_id`/`span_id`, so log correlation only works when both are configured consistently. The colliding `Option` types are disambiguated as `LogOption` (for `NewLogger`) and `Option` (for `Setup`); call sites stay `telemetry.Format(...)` / `telemetry.WithTracing(...)`.

**`/metrics` and pprof are split across two listeners, on purpose.** They have opposite exposure requirements, and one listener cannot satisfy both:
- `/metrics` is on the **public** router (`server.port`), outside the `/api/v1` rate-limited subtree. A scraper must reach it from off-host — in Kubernetes Prometheus hits the pod IP — so it cannot live on loopback. It is unauthenticated and reveals route patterns, latency distributions and pool sizes: block it at the ingress if the API is internet-facing. A test asserts it answers on the public router.
- **pprof** is on its own listener, hardcoded to `127.0.0.1:PPROF_PORT`, and off by default (`PPROF_ENABLED=false`). Disabled means `NewPprofServer` returns nil and `app.go` starts no goroutine and opens no socket — being off is an **attack-surface** decision, not a performance one, since pprof samples nothing until an endpoint is requested. There is deliberately **no bind-host knob** — reach it with `kubectl port-forward`, never by widening the bind. Tests assert it is absent from the public router, that the private listener is loopback, and that disabled returns nil.

Do not "simplify" these onto one listener. An earlier revision did exactly that and had to invent an `admin_host` knob that was necessarily wrong for one of the two — routable enough to scrape means routable enough to profile.

**Defaults are the production-safe set, not the convenient set.** A deployment ships no file, so `internal/config`'s defaults are what it actually runs on: `POSTGRES_MIGRATE=false`, `LOG_FORMAT=json`, `LOG_LEVEL=info`, `PPROF_ENABLED=false`. `.env.example` is the dev-only opt-in to the convenient values (debug text logs, migrate-on-boot). When adding a knob: register it in `setDefaults` with the value production wants — registration is mandatory, since Viper's `AutomaticEnv` only resolves keys it already knows — validate it, then add the dev value to `.env.example`.

**Variable names are unprefixed, so check every new key against the shared namespace.** Kubernetes injects `<SERVICE>_PORT` and `<SERVICE>_SERVICE_HOST` for every Service in the namespace (`enableServiceLinks`, on by default), so a key that spells out to `POSTGRES_PORT` or `REDIS_PORT` would be silently overwritten with something like `tcp://10.96.0.1:5432`. Current keys (`POSTGRES_DSN`, `REDIS_ADDR`, ...) avoid that; keep it that way.

Two sections are named defensively and must not be "simplified" back: **`service.*`** because bare `NAME` and `ENV` are generic enough that unrelated tooling sets them, and **`tracing.*`** because the OpenTelemetry SDK autoconfigures from `OTEL_*` itself (`OTEL_SERVICE_NAME`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SDK_DISABLED`) — defining our own `OTEL_ENABLED`/`OTEL_ENDPOINT` would squat a namespace another library actively reads, so an operator setting the real `OTEL_EXPORTER_OTLP_ENDPOINT` would be silently ignored.

**There is one config vocabulary: environment variables.** An earlier revision had a `config.yaml` of dotted keys that production never read, so every knob had two spellings and the env names were documented nowhere. `.env` is loaded by `parseDotEnv` into a map and layered in as Viper *defaults* — never via `os.Setenv`, which would make `Load` non-idempotent and leak between tests. Because `AutomaticEnv` resolves before defaults, a real environment variable still wins for free: **environment > .env > defaults**.

**Shutdown ordering matters** (`app.go`): a signal fires → `stop()` restores default signal handling so a second Ctrl-C can still kill the process → `srv.BeginDrain()` flips `/readyz` to 503 while traffic is still served → wait `server.drain_delay` → `Shutdown`. The drain gap exists because Kubernetes removes endpoints asynchronously; closing the listener the instant SIGTERM lands is the usual source of 502s during a rolling deploy. Liveness keeps returning 200 throughout — a liveness failure tells the orchestrator to *kill* the process mid-drain.

**Observability is wired end-to-end:** otelhttp wraps the router, otelpgx traces queries, redisotel traces cache calls; handlers/services start their own spans; slog lines auto-carry `trace_id`/`span_id` via the logger's trace-aware handler. Follow this pattern in new code (tracer per package, `WarnContext`/`ErrorContext` with ctx).

## Conventions

- Comments explain *why* or a contract, never *what*. Every exported symbol has a doc comment (revive enforces this).
- Import order: stdlib / third-party / `go-boilerplate/...` last. `.golangci.yml` sets `gofumpt.module-path` because the dot-less module name otherwise confuses gofumpt — don't remove it.
- Line endings are LF everywhere (`.gitattributes` enforces); CRLF breaks gofumpt.
- Tests use hand-written fakes (`fakeStore`, `mockUserService`, `stubUserService` patterns) — no mock generation tooling.
- `internal/repository/integration_test.go` is the template integration test: testcontainers Postgres + real goose migrations + full CRUD.
