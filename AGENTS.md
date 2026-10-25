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

On Windows with a 32-bit gcc, `-race` fails (`cc1.exe: 64-bit mode not compiled in`); run tests without `-race` there and let CI cover the race detector. The same toolchain breaks `sqlc` unless CGO is off — the Makefile already sets `CGO_ENABLED=0` for it.

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
- Every HTTP response goes through `internal/handler/respond.go` (`OK`/`OKList`/`NoContent`/`WriteError`/`WriteServiceError`). Never write to the ResponseWriter directly; never use `http.Error` — a 204 is still a response and `NoContent` exists so it stays inside the envelope layer. Envelope shape: `{"data": ...}` / `{"error": {"code", "message", "fields"?, "details"?}}` with machine-readable codes.
- `WriteJSON` marshals into a buffer *before* writing any header. Never encode straight to the ResponseWriter: that commits a 200 first, so a mid-stream marshal failure would ship a truncated body under a success status.
- **Domain errors are self-describing.** `service.Error` carries `Code`, `Status`, `Message` and optional `Details`; `WriteServiceError` reads those via `errors.As` and therefore contains *no* per-resource cases. New resource-specific errors are declared with `service.NewError(...)` in that resource's own package — never by adding a case to the shared handler layer. Structured details are attached with `ErrX.WithDetails(...)`, which copies the sentinel instead of mutating it. Services wrap with `%w` so the mapping survives.
- Context errors are checked **before** the domain error in `WriteServiceError`: an expired request is a 504 and a cancelled one writes nothing, because a timeout is a transport outcome and not a server fault. `chimw.Timeout` only cancels the context — this mapping is what actually produces the 504.
- Request decoding goes through `handler.DecodeValid[T]` — it enforces the 1 MiB body cap, unknown-field rejection, and `validate` struct tags. It also calls a DTO's `Normalize()` *before* validation, so validation sees the value that will be persisted: trimming in the handler after validation is how a whitespace-only name passes `min=1` and is stored as `""`.
- List endpoints read pagination through `handler.DecodePage`, which **rejects** a malformed or out-of-range `limit`/`offset` with 422 instead of silently clamping. Silent clamping hands the client a page it never asked for and contradicts `DecodeValid`'s strictness on the body path.
- Writes that report rows-affected map 0 rows to `service.ErrNotFound` (see `DeleteUser :execrows`) rather than answering a silent 204.
- Config: every knob is an environment variable + a default **and validation**, documented in `.env.example`. Section types and their per-section validators live in `internal/platform/config`; `internal/config` owns the root `Config`, `Load`, the defaults and the few cross-section rules. Never read env vars directly outside that package. `validate()` accumulates every problem with `errors.Join` so one boot surfaces the whole list. Two CI tests keep `.env.example` honest: it must produce a valid config, and every key in it must be one the app actually reads (a typo there is invisible at runtime — the setting is simply ignored forever).
- `config.Load()` is called in `cmd/api/main.go`, not in `app.Run` — `Run(cfg *config.Config)` takes a ready config so it stays callable from a test or a second binary with a config built in code, no file and no environment.
- Anything holding a secret implements `slog.LogValuer` (`platformconfig.PostgresConfig`, `platformconfig.RedisConfig`). `log.Info("...", "postgres", cfg.Postgres)` must never be able to print a password.
- **`internal/platform/` holds generic infrastructure adapters** (`config`, `errors`, `postgres`, `redis`, `cache`, `ratelimit`, `telemetry`). They take plain values plus functional options and know nothing about this application, so tests and tools can construct them without a `config.Config`. **This is enforced, not documented**: a `depguard` rule fails the build if anything under `internal/platform/` imports `github.com/GtFoBAE05/go-boilerplate/internal`. That rule is the whole justification for the directory — without it, `platform/` becomes the dumping ground every such directory turns into. Don't add something there that needs app knowledge; resolve the decision in `app.go` and pass it down as data.
- Only the composition layer imports `internal/config`: `cmd/api` (loads it), `app/` (unpacks it) and `server/` (app-specific by nature). A second `depguard` rule denies `internal/config` to `handler/`, `service/` and `repository/`. Policy decisions (is tracing on, is Redis required) are resolved in `app.go` and passed down as data, not re-read from config downstream.
- Cache is nilable by design: `cache.Cache` interface, nil means run uncached (redis.mode=disabled/optional). Keep nil checks when touching services; never pass a typed-nil `*cache.Cache` (see the `setupRedis` comment in app.go).
- Transactions: read-modify-write goes through `repository.Store.ExecTx` with `FOR UPDATE` (see user Update).
- `internal/repository` is sqlc-generated except `store.go` — never hand-edit generated files; CI's `sqlc diff` job fails on drift.

**Deliberate decisions — do not "fix" these:**
- No RealIP middleware: forwarded headers are spoofable; rate limiting keys off the TCP peer (`RemoteAddr`). Deployments behind a trusted proxy add their own middleware.
- Rate-limit storage follows Redis. With a client, the counter is shared and a Redis error **fails closed** (`503 RATE_LIMIT_UNAVAILABLE`) rather than falling back to a per-process budget that would allow N×`ratelimit.requests`. With `REDIS_MODE=disabled` there is no client and the limiter is per-process, which is a development mode, not a silent production fallback. Production with rate limiting requires `REDIS_MODE=required` unless `RATELIMIT_ALLOW_PROCESS_LOCAL=true` explicitly accepts per-replica budgets; config validation blocks the unsafe optional/disabled combination at boot.
- No auth, swagger, mock generators, CORS — out of scope for this boilerplate by decision.
- The app is intentionally NOT in docker-compose (infra only); it runs natively for fast iteration. `Dockerfile` is the production image.
- goose over golang-migrate; migrations are embedded (`db/migrations/migrations.go`) and auto-applied at boot when `POSTGRES_MIGRATE=true` — which `.env.example` sets for dev only. The default is off, so production migrates from CI unless explicitly told otherwise.
- Offset pagination is the default because it is what a fresh project needs. It degrades past roughly 10k rows — switch that resource to keyset pagination then; `handler.Page.Meta()` is the one place the response contract is built.
- Build provenance lives in `internal/app/version.go` (unexported vars + `buildInfo()`), not its own package: `app` is the only consumer and the vars encode no decision. `debug.ReadBuildInfo` is not the source because `.dockerignore` excludes `.git`, so a container build has no VCS data. Values arrive via `-ldflags -X github.com/GtFoBAE05/go-boilerplate/internal/app.version=…` — the Makefile and Dockerfile must agree on that path.
- `logger` and `observability` are one package (`platform/telemetry`) because they are one seam, not two: the logger's handler reads the active span to stamp `trace_id`/`span_id`, so log correlation only works when both are configured consistently. The colliding `Option` types are disambiguated as `LogOption` (for `NewLogger`) and `Option` (for `Setup`); call sites stay `telemetry.Format(...)` / `telemetry.WithTracing(...)` / `telemetry.WithMetrics(...)`.

**There is no `/metrics` endpoint, and adding one back is a regression.** Metrics are pushed over OTLP to a collector, which re-exposes them for Prometheus. A scrape endpoint would be a second, unauthenticated egress path for the same data, on the public port, revealing route patterns, latency distributions and pool sizes. `TestNoMetricsEndpointOnPublicRouter` asserts a 404.

The cost of push is paid deliberately: **there is no `up`.** Nothing polls the app, so no scrape can fail, and a dead app is indistinguishable from a dead collector or a broken network between them. `deploy/rules.yml` says so out loud — `AppMetricsMissing` fires on `absent(go_goroutine_count)` and `CollectorDown` (the collector *is* scraped) is what tells the two apart. Restarting a wedged process is the liveness probe's job, not an alert's.

**pprof** is the one remaining private listener: hardcoded to `127.0.0.1:PPROF_PORT`, off by default (`PPROF_ENABLED=false`). Disabled means `NewPprofServer` returns nil and `app.go` starts no goroutine and opens no socket — being off is an **attack-surface** decision, not a performance one, since pprof samples nothing until an endpoint is requested. There is deliberately **no bind-host knob** — reach it with `kubectl port-forward`, never by widening the bind. Tests assert it is absent from the public router, that the private listener is loopback, and that disabled returns nil.

**Defaults are the production-safe set, not the convenient set.** A deployment ships no file, so `internal/config`'s defaults are what it actually runs on: `POSTGRES_MIGRATE=false`, `LOG_FORMAT=json`, `LOG_LEVEL=info`, `PPROF_ENABLED=false`. `.env.example` is the dev-only opt-in to the convenient values (debug text logs, migrate-on-boot). When adding a knob: register it in `setDefaults` with the value production wants — registration is mandatory, since Viper's `AutomaticEnv` only resolves keys it already knows — validate it, then add the dev value to `.env.example`.

**Variable names are unprefixed, so check every new key against the shared namespace.** Kubernetes injects `<SERVICE>_PORT` and `<SERVICE>_SERVICE_HOST` for every Service in the namespace (`enableServiceLinks`, on by default), so a key that spells out to `POSTGRES_PORT` or `REDIS_PORT` would be silently overwritten with something like `tcp://10.96.0.1:5432`. Current keys (`POSTGRES_DSN`, `REDIS_ADDR`, ...) avoid that; keep it that way.

**`service.*`** is named defensively and must not be "simplified" back: bare `NAME` and `ENV` are generic enough that unrelated tooling sets them.

**`otel.*` does the opposite on purpose: it adopts the OpenTelemetry SDK's own variable names with the SDK's own meaning** (`OTEL_SDK_DISABLED`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_TRACES_SAMPLER`, ...). An earlier revision used a private `TRACING_*` namespace to avoid squatting; adopting the real names avoids the same failure more directly, because an operator who sets `OTEL_EXPORTER_OTLP_ENDPOINT` from memory now gets what they expect instead of being ignored. Taking the spec's names means owing the spec's semantics — three consequences that are easy to break:
- `OTEL_METRIC_EXPORT_INTERVAL` is an integer of **milliseconds**, not a Go duration like every other interval in `internal/config`. `TestMetricExportIntervalIsMilliseconds` pins it; reading "60000" as a duration would mean 60µs.
- `OTEL_EXPORTER_OTLP_ENDPOINT` **requires a scheme** — it is what selects TLS. A bare `host:4317` is rejected at boot with the fix in the message, because it is what everyone types first.
- `OTEL_TRACES_SAMPLER` accepts only the strategies `telemetry.NewSampler` implements; `jaeger_remote` and `xray` are **rejected, not ignored**. A sampler that silently is not the one you asked for is how a trace bill or an empty incident timeline happens.

`OTEL_SERVICE_NAME` is deliberately **not** read: `SERVICE_NAME` owns the service identity because the logger uses it too, and it is documented in `.env.example` rather than left to surprise someone. Anything unregistered (`OTEL_EXPORTER_OTLP_HEADERS`, `_TIMEOUT`, `_COMPRESSION`) still reaches the exporter from the **real** environment, since the SDK reads those itself — but not from `.env`, which is parsed into Viper and never exported to the process.

**There is one config vocabulary: environment variables.** An earlier revision had a `config.yaml` of dotted keys that production never read, so every knob had two spellings and the env names were documented nowhere. `.env` is loaded by `platformconfig.ParseDotEnv` into a map and layered in as Viper *defaults* — never via `os.Setenv`, which would make `Load` non-idempotent and leak between tests. Because `AutomaticEnv` resolves before defaults, a real environment variable still wins for free: **environment > .env > defaults**.

**Shutdown ordering matters** (`app.go`): a signal fires → `stop()` restores default signal handling so a second Ctrl-C can still kill the process → `srv.BeginDrain()` flips `/readyz` to 503 while traffic is still served → wait `server.drain_delay` → `Shutdown`. The drain gap exists because Kubernetes removes endpoints asynchronously; closing the listener the instant SIGTERM lands is the usual source of 502s during a rolling deploy. Liveness keeps returning 200 throughout — a liveness failure tells the orchestrator to *kill* the process mid-drain.

**Probes are excluded from both signals.** `otelhttp.WithFilter(!isProbe)` in `server.New` drops `/healthz` and `/readyz` — it short-circuits before any instrumentation, so metrics go too, which is wanted: constant always-fast probe traffic drags the p99 down and dilutes the error-ratio denominator. That alone is not enough, because `/readyz` pings Postgres and Redis through instrumented clients that create a span each with no opt-out; filtering only the HTTP span leaves those as **parentless roots**, noisier than before. `health.Readiness` therefore pings under an unsampled parent span context, which the parentbased_* samplers propagate as a drop. `TestProbesAreNotTraced` and `TestReadinessPingsAreNotSampled` cover the two halves.

**Observability is wired end-to-end:** otelhttp wraps the router, otelpgx traces queries, redisotel traces cache calls; traces and metrics both leave over OTLP/gRPC to the collector; handlers/services start their own spans; slog lines auto-carry `trace_id`/`span_id` via the logger's trace-aware handler. Follow this pattern in new code (tracer per package, `WarnContext`/`ErrorContext` with ctx).

**Metrics come from libraries, never hand-rolled**, so a new resource inherits them without writing any: otelhttp emits the RED signals, `otelpgx.RecordStats` the pgx pool gauges, `redisotel.InstrumentMetrics` the Redis client pool, and `contrib/instrumentation/runtime` the `go.*` runtime metrics. That last one is started explicitly in `telemetry.Setup` because pushing has no client_golang registry to contribute a Go collector by default — drop it and goroutine count and heap size stop existing. Four consequences:
- The `routePattern` middleware is load-bearing for metrics, not just span names: otelhttp wraps the router from outside and never sees chi's pattern, so it pushes `http.route` into otelhttp's Labeler post-routing. Delete it and every endpoint collapses into one latency histogram. It skips unmatched paths on purpose — labelling a 404 with the raw URL is unbounded cardinality from unauthenticated input. `TestRequestMetricsCarryRoutePattern` covers both halves.
- `telemetry.Setup` must run **before** `postgres.NewPool` and `redis.New` — both capture `otel.GetMeterProvider()` at call time, so a pool built first is instrumented against the no-op provider and silently exports nothing. `app.Run` already orders it that way; keep it.
- Before adding an instrument, check it is not derivable from `http_server_*`. A per-endpoint success counter is not — it is `http_server_request_duration_seconds_count` filtered by status. Build any genuinely new instrument once in the constructor, never per request, and keep its attributes to a bounded set: one label carrying an id, email or `trace_id` is one time series per value and takes down the collector, not just the dashboard.
- `instance` comes from `service.instance.id`, stamped from the hostname in `telemetry.Setup`. Scraping derived it from the target address for free; pushing, only the process can say who it is, and without it every replica collapses onto one series. `deploy/prometheus.yml` must keep `honor_labels: true` or the scrape overwrites both `job` and `instance` with the collector's identity.

Build provenance reaches Prometheus as `target_info{service_version,vcs_ref_head_revision}` via `telemetry.WithBuild(version, commit)` — the OTel resource, not a separate `build_info` gauge, because the resource already flows to traces too. `deploy/rules.yml` holds the recording + alerting rules. **Its series names were verified against a real export, not guessed** — instrument names rarely match intuition (`pgxpool.acquire_duration` scrapes as `pgxpool_acquire_duration_nanoseconds_total`; redisotel emits `db_client_connections_*`, not `redis_*`). Re-verify after any instrumentation change and run `promtool check rules deploy/rules.yml`. An alert querying a series that does not exist never fires and never tells you.

## Conventions

- Comments explain *why* or a contract, never *what*. Every exported symbol has a doc comment (revive enforces this).
- Import order: stdlib / third-party / `github.com/GtFoBAE05/go-boilerplate/...` last. `.golangci.yml` states `gofumpt.module-path` explicitly instead of relying on inference — keep it in sync with `go.mod` if the module is ever renamed.
- Line endings are LF everywhere (`.gitattributes` enforces); CRLF breaks gofumpt.
- Tests use hand-written fakes (`fakeStore`, `mockUserService`, `stubUserService` patterns) — no mock generation tooling.
- `internal/repository/integration_test.go` is the template integration test: testcontainers Postgres + real goose migrations + full CRUD.

### Service package conventions

These four rules keep every resource in this repository uniform. Follow them when copying the `user` vertical.

**1. Service names are resource-qualified, not package-relative.** Each `internal/service/<resource>` exports `<Resource>Service` (interface), `<resource>Service` (unexported struct), and `New<Resource>Service`; the receiver matches the struct name.

```go
type UserService interface { ... }
type userService struct { ... }
func NewUserService(...) UserService
func (s *userService) Create(...) ...
```

Not `Service` + `svc` + `New`. The composition layer (`app/di.go`, `server/server.go`) imports several service packages aliased, and a bare `Service` says nothing about whose it is; `grep userService` lands on one thing.

This costs a `revive` exception: under package-per-resource the name reads as `user.UserService`, which the `exported` rule calls a stutter. `.golangci.yml` sets `disableStutteringCheck` for exactly this reason — the rest of the `exported` rule (doc comments on every exported symbol) still applies, so don't drop the rule wholesale.

**2. No compile-time interface assertions in service packages.** Delete `var _ Service = (*svc)(nil)`. The constructor returns the interface, so an incomplete implementation already fails to compile at `return &userService{...}`.

Keep the assertion where no constructor returns the interface — `repository.store`, `platform/cache.redisCache`, and the hand-written test fakes (`fakeStore`, `mockUserService`) rely on it as the only check. Never touch generated files (`repository/querier.go` is sqlc's).

**3. One service package is two files.** `model.go` holds every type; `service.go` holds the interface plus the implementation. No separate `input.go` / `output.go` / `dto.go` — merge them. Split further only once `model.go` passes roughly 200 lines.

**4. Method-signature types carry an `Input` / `Output` suffix, named per method** — `Create` → `CreateInput`/`CreateOutput`, `Login` → `LoginInput`/`LoginOutput`. Types nested inside an `Output` take the suffix too. A bare name (`User`, `Claims`) means the type is *not* part of a method signature.

Accept the duplication this creates: `service/user` ships `CreateOutput`, `GetOutput`, `ListOutput` and `UpdateOutput` field-for-field identical. Leave duplicates until a **third** identical copy appears — merging early couples endpoints that should stay free to diverge. The handler mirrors this with one mapper per Output (`createToDetailResponse`, `getToDetailResponse`, `updateToDetailResponse`); all three build the same `DetailResponse` today.

**A cached resource needs one more type.** `service/user` keeps `UserCache` — no `Input`/`Output` suffix, because it appears in no method signature. It exists because the shape stored in Redis must stay decoupled from any method's `Output`: cache a `GetOutput` and changing that signature silently invalidates every live entry. So `Get` deserializes into `UserCache` and converts at the return boundary, while `Create`, `Update` and `List` map `repository.User` straight to their `Output` and never touch it. Don't route the non-cached methods through it for symmetry — that hop buys nothing.

`toGetOutput` is a `GetOutput(u)` conversion, which compiles only while the two remain field-identical. When `GetOutput` diverges, write the mapping out field by field; never reshape `UserCache` to preserve the cast, since its whole point is not tracking an Output.

**What these rules must not change:** service types carry **zero struct tags** — `json`/`validate` belong on handler Request/Response types, DB tags only on generated repository types. Mapping domain → response stays in the handler (`toDetailResponse`, `toListItemResponses`); a service must never construct a response type.
