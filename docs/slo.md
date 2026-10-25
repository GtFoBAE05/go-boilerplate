# Service-level objectives

These are starting targets for the template, not a contract copied from a real
deployment. Replace the numbers with what the service's users actually need.

## Availability

- **SLO:** 99.9% of requests over a rolling 30 days are not 5xx.
- **SLI:** `1 - job:http_requests:error_ratio5m` (5xx only; 4xx is client input,
  not service failure).
- **Error budget:** 0.1% of requests, roughly 43 minutes of full outage in 30
  days.
- **Burn-rate pages:** the `HighErrorRate` alert at 5% over five minutes is a
  fast-burn signal. A slow burn should be reviewed weekly against the budget,
  not paged.

## Latency

- **SLO:** p99 request duration under 1 second over a rolling 30 days.
- **SLI:** `job:http_request_duration_seconds:p99_5m`.
- **Note:** the p99 is measured at the HTTP boundary, including database and
  Redis time. A pool-exhaustion incident appears here before it appears as a
  5xx.

## Freshness / correctness

This template has no asynchronous pipeline, so there is no freshness SLO. When
one is added (outbox, queue consumer), define the SLI as the age of the oldest
unprocessed item and alert on the age, not on queue depth alone: a deep queue
that is draining is healthy; a shallow queue that is stuck is not.

## What is deliberately not an SLO

- **`up`:** metrics are pushed, so there is no scrape target to be up. Use
  `AppMetricsMissing` and the liveness probe instead.
- **CPU or memory percentage:** these are causes, not user-visible outcomes.
  Alert on the symptom (latency, errors, pool saturation) and use resource
  graphs for diagnosis.

## Review

Review the SLOs after every incident and at least once per quarter. A target
that has never been missed may be too loose; a target that pages weekly without
a user-visible impact is measuring the wrong thing.
