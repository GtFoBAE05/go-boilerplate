# Runbook

This is the on-call companion to `deploy/rules.yml`. Each alert links here with
an anchor; the first question is always whether the signal is real, because
metrics are **pushed** and the absence of data is itself ambiguous.

## First response

1. Check `CollectorDown` and `AppMetricsMissing` together. If both fire, the
   pipeline is broken and every other metric alert is blind rather than healthy.
2. Open the trace for a failing request. Every log line carries `trace_id` and
   `span_id`; paste the `trace_id` into Jaeger.
3. Check the most recent deploy: `target_info{service_version}` tells you which
   build produced a series. A regression that starts exactly at a version
   boundary is a rollback, not a debugging session.
4. Confirm the alert's window is long enough. `HighErrorRate` needs five
   minutes; a single bad request is not an incident.

## AppMetricsMissing

**Meaning:** no `go_goroutine_count` series for three minutes. The app, the
collector, or the network between them is down.

**Do not** assume the app is healthy because requests still succeed; the
collector may be the broken hop. Check `CollectorDown` first.

- If `CollectorDown` also fires: the collector is down. Restart it and verify
  that Prometheus can scrape `:8889`. Other metric alerts are unknown until it
  recovers.
- If only `AppMetricsMissing` fires: check the app's liveness probe and recent
  logs. In Kubernetes, a wedged process should be restarted by the liveness
  probe, not by a human.
- If the app is healthy and the collector is healthy, inspect egress from the
  app to `OTEL_EXPORTER_OTLP_ENDPOINT` (DNS, TLS, network policy).

## CollectorDown

**Meaning:** Prometheus cannot scrape the collector (`up{job="otel-collector"}
== 0`). This is the one alert that still rests on `up`.

- Restart the collector and confirm `/metrics` on its Prometheus exporter port.
- If the collector is up but not scrapeable, check `deploy/prometheus.yml` and
  `honor_labels: true`.
- Treat all other metric alerts as unknown, not resolved, until data returns.

## HighErrorRate

**Meaning:** more than 5% of requests are 5xx over five minutes.

- Break down by `http_route` and `http_response_status_code`.
- For a 500/504, follow a `trace_id` from the error log into Jaeger.
- If the errors started at a deploy, roll back first and diagnose after.
- If only one route is affected, check its downstream dependency (Postgres for
  most routes, Redis for the limiter).
- A 503 `RATE_LIMIT_UNAVAILABLE` means Redis is failing closed. Restore Redis or
  deliberately switch to `REDIS_MODE=optional` for development only; production
  should not accept a per-process limiter silently.

## HighLatencyP99

**Meaning:** p99 request latency is above one second for ten minutes.

- Check `instance:pgxpool_connections:utilization` and
  `PostgresPoolStarved` first. Pool exhaustion shows up as latency inside the
  acquire, not as an error.
- Check Postgres slow queries and lock waits. A read-modify-write holds
  `FOR UPDATE`; a long transaction blocks the next update of the same row.
- Check Redis command latency if the limiter is in the request path.
- If the p99 is caused by a single route, fix that route; do not raise the
  timeout to hide it.

## PostgresPoolSaturated

**Meaning:** more than 90% of `POSTGRES_MAX_CONNS` is in use for five minutes.

- Raise `POSTGRES_MAX_CONNS` only after checking the database's own connection
  limit; every replica adds its pool.
- Find the query holding connections: `pg_stat_activity`, then correlate with
  `trace_id` in the query span.
- A leaked transaction is a code bug. Check `ExecTx` call sites for a missing
  rollback or a long-running function inside the transaction.

## PostgresPoolStarved

**Meaning:** requests are waiting for an empty pool more than once per second.

- This is the leading indicator for `PostgresPoolSaturated`; treat it as an
  early warning.
- Reduce query time or pool pressure. Adding replicas multiplies pool demand,
  so scale the database before scaling the app.
- If the wait is caused by one slow query, fix that query first.

## Rollback

Any alert that starts at a version boundary is a rollback candidate. Roll back
to the previous image, confirm `target_info` changes, and keep the incident open
until the error ratio and p99 have returned to baseline for one full window.

## Escalation

- **Page:** `AppMetricsMissing`, `CollectorDown`, `HighErrorRate`.
- **Warn:** `HighLatencyP99`, `PostgresPoolSaturated`, `PostgresPoolStarved`.
- Escalate to the database owner for pool or lock incidents; escalate to the
  platform owner for collector or network incidents.
