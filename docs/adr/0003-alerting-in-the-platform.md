# 0003. Alerting in the platform; target state exported from the database

- Status: Accepted
- Date: 2026-11-13

## Context

People must be told when a target goes down. The app could send messages
itself, or report state and let the monitoring platform decide. With several
worker replicas, each worker only sees the targets it happened to check.

## Decision

- The app **reports, it does not notify**. Prometheus scrapes metrics;
  `PrometheusRule`s in the Helm chart decide when something is wrong;
  Alertmanager decides whom to tell (Discord) and how often.
- **Target state (`uptime_target_up`, last check time, interval) is exported
  by the API**, read from the database at scrape time by a custom collector,
  not by the workers. Workers export only their own counters and latencies.
- Metric labels use route patterns and target names, never raw URLs or ids
  from requests, to keep cardinality bounded.
- Alerts: `TargetDown` (down for 2 minutes), `TargetChecksStale` (no result
  for 3 intervals: catches a stopped worker that `TargetDown` cannot see),
  `UptimeWorkerDown`, `UptimeAPIDown`.

## Consequences

- Grouping, repeat intervals, silences and routing are Alertmanager config,
  changed without touching the app.
- The state is correct whatever the number of workers.
- Every API replica exports the same series, so rules aggregate with
  `max by (namespace, target)`.
- If the API is down, `TargetDown` cannot fire: `UptimeAPIDown` covers that.
- If the whole alerting path dies, nothing fires: the always-firing
  `Watchdog` alert is the hook for an external dead man's switch (not wired
  yet).

## Alternatives considered

- **The worker sends Discord messages**: duplicates Alertmanager (grouping,
  repeats, silences), and every new channel means app code.
- **Workers export `up` per target**: with two workers, half the series
  disappear between checks.
