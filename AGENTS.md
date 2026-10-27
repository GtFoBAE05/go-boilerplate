# AGENTS.md

Normative rules for AI coding agents. `README.md` is the human-facing overview;
keep human documentation there and do not add a separate `docs/` tree.

## Commands

```bash
cp .env.example .env
make docker-up-all
make observability-rules
make run-observability
make test test-integration lint gosec sqlc sqlc-diff vuln
make migrate-new name=foo
```

`make run-observability` tees JSON stdout to `.tmp/logs/api.log`; plain
`go run ./cmd/api` does not ship logs to Loki.

Single test: `go test -run TestName ./internal/service/user/`.

Promtool (Mimir rule format): `docker run --rm --entrypoint promtool -v "$PWD/deploy:/deploy:ro" prom/prometheus:v3.14.0 check rules /deploy/rules.yml`.

On Windows with 32-bit gcc, `-race` fails; let CI cover it. sqlc needs
`CGO_ENABLED=0`, which the Makefile sets. Versions are pinned in the Makefile
and CI, not `go.mod`.

## Architecture

`handler/<resource> → service/<resource> → repository`

- A service never calls another service; the use case issues its own queries.
- Handlers never import `internal/repository`.
- `internal/platform/` is generic and may not import application code. Only
  `cmd/api`, `internal/app`, and `internal/server` may import `internal/config`.
- `internal/repository` is sqlc-generated except `store.go`; never hand-edit it.
- Transactions use `repository.Store.ExecTx`; read-modify-write takes
  `FOR UPDATE`.

Adding a resource: migration → queries/sqlc → copy service/handler → wire
`di.go` and `server.go`. `cmd/` and `app.go` stay untouched.

## Invariants

### HTTP

- All responses go through `respond.go`; never write the `ResponseWriter`
  directly or use `http.Error`.
- `WriteJSON` marshals into a buffer before writing headers.
- Domain errors carry `Code`, `Kind`, `Message`, and optional `Details`.
  `WriteServiceError` maps `Kind` to status. New errors use
  `service.NewError(...)`; never add a per-resource case to the shared handler.
- Check context errors first: timeout → 504, cancellation → no body.
- `DecodeValid[T]`: 1 MiB cap, unknown/duplicate fields rejected, `Normalize()`
  before validation, `validate` tags.
- `DecodePage`: invalid `limit`/`offset` is 422, not clamped.
- Rows-affected writes map 0 rows to `service.ErrNotFound`; never a silent 204.
- No `RealIP`; rate limiting keys off `RemoteAddr`.
- A panic after a response has started aborts the connection; it cannot be
  turned into an error envelope.

### Config

- Each setting needs an env var, a production-safe default, and validation.
  Register it in `internal/config`, document it in `.env.example`, and add it to
  `setDefaults`.
- `config.Load()` runs in `cmd/api`; `app.Run(cfg)` receives a ready config.
- `validate()` uses `errors.Join`; never read env vars outside `internal/config`;
  secrets implement `slog.LogValuer`.
- Precedence is environment > `.env` > defaults. `.env` is layered as Viper
  defaults, never exported with `os.Setenv`.
- Production defaults: `SERVICE_ENV=production`, `REDIS_MODE=required`,
  `POSTGRES_MIGRATE=false`, `LOG_FORMAT=json`, `LOG_LEVEL=info`,
  `PPROF_ENABLED=false`.
- Production rejects boot migrations, process-local rate limiting without
  `RATELIMIT_ALLOW_PROCESS_LOCAL=true`, and insecure Redis TLS.

### Data

- Business invariants live in DTO validation, service validation, and database
  constraints (`CHECK`, `lower(email)` unique index).
- Postgres error inspection belongs in `internal/repository`
  (`IsUniqueViolation`, `ConstraintName`), not services.
- Goose migrations are embedded and applied at boot only when
  `POSTGRES_MIGRATE=true`; production runs the advisory-locked
  `POSTGRES_MIGRATION_DSN=... make migrate-run` (`cmd/migrate`) from a dedicated
  job.

### Observability

- Start a tracer per package; use `WarnContext`/`ErrorContext`.
- Metrics come from instrumentation libraries. Keep labels bounded; never use a
  user id, email, or `trace_id`. Do not add `/metrics`.
- Probes are excluded from tracing/metrics; `/readyz` pings under an unsampled
  parent span.
- Redis is required for `/readyz` whenever the rate limiter uses it: the limiter
  fails closed, so a 200/degraded probe would keep a replica answering 503 in
  rotation. Optional Redis is only optional when the limiter is off or fell back
  to local counters at startup.
- `telemetry.Setup` runs before `postgres.NewPool` and `redis.New`.
- The local stack is LGTM: app OTLP → collector → Tempo/Mimir; app stdout JSON
  → `.tmp/logs/api.log` → collector `file_log` → Loki. Grafana is the UI.
- Keep `honor_labels: true` in the collector's `prometheus/self` receiver. It
  preserves the app's `job`/`instance` labels when the collector scrapes its own
  Prometheus exporter before remote-writing to Mimir.
- Do not parse JSON log fields into OTLP attributes in the collector: Loki
  promotes attributes to labels, and `trace_id`/`request_id`/`path` would create
  unbounded streams. Keep them in the JSON body.
- `deploy/rules.yml` is loaded into Mimir's ruler by the one-shot `mimir-rules`
  service; `make observability-rules` reloads it. There is no local
  Alertmanager, so rules evaluate but do not notify.

## Deliberate decisions — do not “fix” these

- No auth, Swagger/OpenAPI, mock generators, or CORS.
- The app is not in either compose file; they are local infrastructure.
- Offset pagination is the default; switch a resource to keyset past ~10k rows.
- Provenance comes from `-ldflags` into `internal/app/version.go`;
  `.dockerignore` excludes `.git`.
- `logger` and observability are one package (`platform/telemetry`).
- pprof is loopback-only, off by default, with no bind-host knob.
- The app has no `up` metric. The collector's self-scrape provides
  `up{job="otel-collector"}`; use `AppMetricsMissing` and `CollectorDown`
  together.
- Shutdown: restore signals → `BeginDrain()` → `drain_delay` → `Shutdown`;
  liveness stays 200 during the drain.

## Conventions

- Comments explain *why* or a contract. Every exported symbol has a doc comment.
- Imports: stdlib, third-party, then
  `github.com/GtFoBAE05/go-boilerplate/...`. LF only.
- Tests use hand-written fakes; no mock generation.
- `internal/architecture/architecture_test.go` checks imports; add new
  `internal/` packages there.
- `internal/repository/integration_test.go` is the integration-test template.
- Service packages export `<Resource>Service`, keep `<resource>Service`
  unexported, and provide `New<Resource>Service`. No interface assertions.
- A service is exactly `model.go` + `service.go`; split only past ~200 lines.
- Method types use `Input`/`Output` suffixes; nested output types keep them.
- Service types carry no `json`, `validate`, or `pgtype` tags; mapping stays in
  the handler.

## Definition of done

```bash
gofmt -l . && go build ./... && go vet ./... && go test -race ./...
make lint && make gosec && make sqlc-diff && make vuln
```

Run `make test-integration` for repository, migration, or startup changes. Update
`README.md` for user-visible behavior; update this file for agent rules.
