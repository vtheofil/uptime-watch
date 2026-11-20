# 0001. Schedule checks in Postgres with FOR UPDATE SKIP LOCKED

- Status: Accepted
- Date: 2026-11-13

## Context

Every target must be checked once per interval. The worker should scale to
several replicas without two of them checking the same target, survive
restarts without losing the schedule, and need no extra infrastructure.

## Decision

Each target has a `next_check_at` column. Every few seconds a worker claims up
to N due targets and moves their `next_check_at` forward in one statement:

```sql
UPDATE targets SET next_check_at = now() + interval
WHERE id IN (SELECT id FROM targets
             WHERE enabled AND next_check_at <= now()
             ORDER BY next_check_at LIMIT $1
             FOR UPDATE SKIP LOCKED)
RETURNING ...
```

`FOR UPDATE` locks the claimed rows; `SKIP LOCKED` makes a second worker skip
them instead of waiting, so concurrent claims never overlap. A partial index
on `next_check_at WHERE enabled` keeps the query cheap.

## Consequences

- Workers scale out by raising `replicas`; no leader election, no queue.
- The schedule lives in the database, so restarts and rollouts keep it.
- `next_check_at` moves when a target is claimed, not when its check ends: a
  worker that crashes mid-check loses that one check, and the target is
  checked again one interval later. Acceptable for an uptime monitor.
- One small query per worker per poll interval (5 s), even when idle.
- A test runs five claimers in parallel against a real Postgres and asserts
  that no target is claimed twice.

## Alternatives considered

- **Timers in memory** (one goroutine per target): every replica would check
  every target, and the schedule is lost on restart.
- **A message queue or a cron service**: another component to run and
  monitor, for a load Postgres handles easily.
- **Advisory locks**: work, but row locks with `SKIP LOCKED` are simpler and
  release themselves at commit.
