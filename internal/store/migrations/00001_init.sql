-- +goose Up
CREATE TABLE targets (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name             text        NOT NULL UNIQUE,
  url              text        NOT NULL,
  interval_seconds integer     NOT NULL DEFAULT 60 CHECK (interval_seconds >= 10),
  timeout_ms       integer     NOT NULL DEFAULT 5000 CHECK (timeout_ms BETWEEN 100 AND 30000),
  enabled          boolean     NOT NULL DEFAULT true,
  next_check_at    timestamptz NOT NULL DEFAULT now(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT timeout_below_interval CHECK (timeout_ms < interval_seconds * 1000)
);

-- The worker asks "which enabled targets are due?" every few seconds.
-- A partial index holds only enabled targets, ordered by when they are due.
CREATE INDEX targets_due ON targets (next_check_at) WHERE enabled;

CREATE TABLE checks (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  target_id   bigint      NOT NULL REFERENCES targets(id) ON DELETE CASCADE,
  checked_at  timestamptz NOT NULL DEFAULT now(),
  ok          boolean     NOT NULL,
  status_code integer,
  latency_ms  integer     NOT NULL,
  error       text
);

CREATE INDEX checks_target_time ON checks (target_id, checked_at DESC);

-- +goose Down
DROP TABLE checks;
DROP TABLE targets;
