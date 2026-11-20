# 0002. One binary, and database steps as Kubernetes Jobs

- Status: Accepted
- Date: 2026-11-13 (sync-targets added 2026-11-19)

## Context

uptime-watch has several roles: the API and status page, the worker, schema
migrations, pruning old checks, and syncing targets from git. Migrations and
the target sync must run exactly once per release, before or after the new
pods, never once per pod.

## Decision

- **One binary and one image**, with commands: `serve`, `worker`, `migrate`,
  `prune`, `sync-targets`, `check`. Each Deployment or Job runs the same image
  with different arguments.
- **`migrate` runs as a Job with the Helm hook `pre-install,pre-upgrade`**
  (Argo CD runs it as PreSync): the tables are ready before new pods start, and
  a failed migration stops the release while the old pods keep running.
- **`sync-targets` runs as a Job with `post-install,post-upgrade`** (PostSync):
  it needs the ConfigMap with the target list, which exists only after the
  sync phase.
- **`prune` is a daily CronJob** with `concurrencyPolicy: Forbid`.

## Consequences

- One build, one vulnerability scan, one version to pin.
- Two API pods never race to migrate; Argo CD shows a failed Job in red.
- The migrate hook cannot read the chart's ConfigMap (pre-install runs before
  it exists), so it reads only the Secret.
- The sync Job keeps its last run (`hook-delete-policy: before-hook-creation`)
  so `kubectl logs job/uptime-watch-sync-targets` shows what changed; the
  migrate Job is deleted when it succeeds.
- A migration must work on a database that already has rows: CI starts from
  an empty database and cannot catch, for example, `ADD COLUMN ... NOT NULL`
  without a default.

## Alternatives considered

- **Migrate on start-up of every pod**: two replicas would migrate at once.
- **Separate images per role**: more builds and versions to keep in step.
- **An init container that migrates**: runs once per pod, the same race.
