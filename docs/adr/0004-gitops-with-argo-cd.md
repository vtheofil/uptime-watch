# 0004. GitOps with Argo CD: app of apps, sync waves and pinned versions

- Status: Accepted
- Date: 2026-11-19

## Context

Deploying meant running scripts (`helm install`, `kubectl apply`) by hand. A
lost or rebuilt cluster had to be set up again from memory, and nothing
recorded which version ran or who changed what. Some components depend on
others: the app's `ServiceMonitor` needs the Prometheus Operator CRDs; HTTP
routes need the Gateway API CRDs and Traefik.

## Decision

- **Argo CD keeps the cluster equal to git.** Only `deploy/argocd/root.yaml`
  is applied by hand (by `bootstrap.sh`); it points at `deploy/argocd/apps/`,
  one `Application` per component (app of apps). `prune` and `selfHeal` are on:
  removed from git means removed from the cluster, a manual change is undone.
- **Sync waves order the components**: CRD providers first (Gateway API),
  then Traefik, monitoring, Postgres, the app, and the routes last.
- Waves only wait if an `Application` has a health status, which Argo CD
  stopped computing in 1.8. `bootstrap.sh` adds a Lua health check for
  `Application` so each wave really waits for the previous one.
- **The image is pinned to a commit SHA** in `apps/uptime-watch.yaml`, never
  `latest`: git says exactly which build runs, and a rollback is `git revert`.
- Secrets are never in git: `bootstrap.sh` creates them (see 0008 for secrets
  that must survive a rebuild).

## Consequences

- A new cluster is `make kind-up && make kind-argocd`, and everything comes
  back from git (data excepted: the database is not in git; see the backups
  idea in the README).
- Deploying is a commit; the history is the deploy log.
- `main` is what runs: a broken commit on `main` is deployed within minutes.
  Changes go through a pull request so CI runs first.
- A wave that waits on an app reported Degraded fails the sync. On a fresh
  cluster Prometheus and Alertmanager report Degraded for a few minutes while
  images download, so the root app needs a longer retry policy than Argo CD's
  default (`retry` in `root.yaml`). Once the retries run out, Argo CD does not
  retry the same commit by itself (see the runbook).
- Changes made with `kubectl` to Argo-managed objects are reverted: debugging
  by hand needs `selfHeal` turned off, or a change in git.

## Alternatives considered

- **Flux**: the same model; Argo CD was chosen for its UI, which helps learning.
- **CI runs `helm upgrade`** (push-based): CI would need cluster credentials,
  and drift between git and the cluster would go unnoticed.
- **One Application for everything**: no ordering between components.
