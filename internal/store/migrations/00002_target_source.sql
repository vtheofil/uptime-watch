-- +goose Up
-- Where a target came from: 'api' (POST /api/targets) or 'git' (the targets
-- file synced by `uptime-watch sync-targets`). The sync deletes only 'git'
-- targets, so targets added by hand are never removed by a deploy.
-- Existing rows were all added through the API, so the default fits them.
ALTER TABLE targets
  ADD COLUMN source text NOT NULL DEFAULT 'api'
  CONSTRAINT targets_source_check CHECK (source IN ('api', 'git'));

-- +goose Down
-- Dropping the column drops its CHECK constraint with it.
ALTER TABLE targets DROP COLUMN source;
