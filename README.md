# uptime-watch

[![ci](https://github.com/vtheofil/uptime-watch/actions/workflows/ci.yaml/badge.svg)](https://github.com/vtheofil/uptime-watch/actions/workflows/ci.yaml)
![Go 1.27](https://img.shields.io/badge/go-1.27-00ADD8)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

An uptime monitor written in Go and run the way production services are run:
container image built and scanned in CI, deployed to Kubernetes with Helm and
Argo CD, observed with Prometheus, Grafana and Alertmanager.

It checks a list of URLs on a schedule, stores every result in Postgres, shows
a status page, exposes a JSON API and exports metrics that drive alerts, which
reach you in Discord when a site goes down and again when it recovers.

| Status page | Grafana dashboard |
|---|---|
| ![Status page with one target down](docs/images/status-page.jpg) | ![Grafana dashboard: up/down timeline per target, with a short outage of demo-site](docs/images/grafana.jpg) |

```
            ┌──────────────┐  claim due targets   ┌──────────┐
            │    worker    │ ───────────────────▶ │          │
 sites ◀────│ (N replicas) │  store results       │ Postgres │
  HTTP GET  └──────────────┘ ───────────────────▶ │          │
                   │ /metrics                     └──────────┘
                   ▼                                    ▲
            ┌──────────────┐   scrape /metrics   ┌──────────────┐
            │  Prometheus  │ ◀─────────────────  │  api (serve) │ ◀── browser / curl
            └──────────────┘                     └──────────────┘
              │         │
          Grafana   Alertmanager → notifications
```

## Features

- **One binary, five commands**: `serve` (status page, API, `/healthz`, `/readyz`,
  `/metrics`), `worker` (the check loop), `migrate`, `prune`, `check <url>`.
- **Horizontally scalable worker**: due targets are claimed with
  `UPDATE … WHERE id IN (SELECT … FOR UPDATE SKIP LOCKED)`, so any number of
  worker replicas share the work and never check the same target twice. A test
  proves it with concurrent workers on a real Postgres.
- **Bounded concurrency** with a channel used as a semaphore; the worker only
  claims as many targets as it has free slots.
- **Graceful shutdown** that matches Kubernetes: on `SIGTERM` the API fails
  `/readyz`, waits for the Service to drop it, then drains; the worker stops
  claiming and gives running checks a grace period to finish.
- **Up/down rule**: final status below 400 after following redirects is up;
  4xx/5xx, timeouts, DNS/TCP/TLS errors are down, with the error stored.
- **Metrics and alerts** for the failures that matter, including the one that
  is easy to miss: if the worker stops, the last result stays "up" forever, so
  `TargetChecksStale` alerts when results get older than three intervals.
- **Tests against real dependencies**: real HTTP servers (`httptest`) and a real
  Postgres in Docker (`testcontainers-go`); no mocks.

## Quick start

Requirements: Go 1.27, Docker and `make`. `make help` lists every command.

```bash
git clone https://github.com/vtheofil/uptime-watch.git && cd uptime-watch
make db        # Postgres in Docker (port 5433) + migrations
make run       # serve on :8080 and worker on :8081; Ctrl+C stops both
```

`make run` sets the admin token to `dev-token`. Add a target and open
<http://localhost:8080>:

```bash
curl -X POST localhost:8080/api/targets \
  -H "Authorization: Bearer dev-token" \
  -d '{"name":"go-dev","url":"https://go.dev","interval_seconds":30}'
```

Run the checks CI runs:

```bash
make lint      # gofmt, go vet, staticcheck, govulncheck
make test      # unit and integration tests (needs Docker)
```

## API

| Method and path | Purpose | Auth |
|---|---|---|
| `GET /` | Status page (HTML, refreshes every 30 s) | – |
| `GET /api/targets` | Targets with their latest check and 24 h uptime | – |
| `POST /api/targets` | Create a target: `name`, `url`, optional `interval_seconds` (≥ 10, default 60) and `timeout_ms` (100–30000, below the interval, default 5000) | Bearer token |
| `DELETE /api/targets/{id}` | Delete a target and its history | Bearer token |
| `GET /api/targets/{id}/checks?limit=100` | Latest checks of a target (limit 1–1000) | – |
| `GET /healthz` | Liveness: the process runs | – |
| `GET /readyz` | Readiness: the database answers and we are not shutting down | – |
| `GET /metrics` | Prometheus metrics | – |

The write endpoints need `Authorization: Bearer $ADMIN_TOKEN`. With
`ADMIN_TOKEN` unset they are switched off (403), and the server is read-only.

## Configuration

All settings come from environment variables.

| Variable | Default | Used by |
|---|---|---|
| `DATABASE_URL` | – (required) | all but `check` |
| `ADMIN_TOKEN` | empty: write API off | serve |
| `PORT` | `8080` | serve, worker |
| `WORKER_CONCURRENCY` | `10` | worker |
| `WORKER_POLL_INTERVAL` | `5s` | worker |
| `WORKER_GRACE` | `10s` | worker |
| `SHUTDOWN_DELAY` | `0` (Helm chart: `5s`) | serve |
| `RETENTION_DAYS` | `30` | prune |
| `LOG_FORMAT` | `text` (Helm chart: `json`) | all |
| `LOG_LEVEL` | `info` | all |

## Metrics

| Metric | Type | From |
|---|---|---|
| `uptime_target_up{target}` | gauge (1/0) | serve, read from the database at scrape time |
| `uptime_target_last_check_timestamp_seconds{target}` | gauge | serve |
| `uptime_target_interval_seconds{target}` | gauge | serve |
| `uptime_checks_total{target,result}` | counter | worker |
| `uptime_check_duration_seconds{target}` | histogram | worker |
| `uptime_worker_inflight` | gauge | worker |
| `http_requests_total{route,code}` | counter | serve |
| `http_request_duration_seconds{route}` | histogram | serve |

Target state is exported by the API from the database, not by the worker: with
several workers, each one only sees the targets it checked.

## Running on Kubernetes

### kind + Helm

Extra requirements: [kind](https://kind.sigs.k8s.io/), `kubectl` and `helm`.

```bash
make kind-up          # a kind cluster with its own kubeconfig in .kube/ (your contexts stay untouched)
make kind-monitoring  # Prometheus, Grafana, Alertmanager (kube-prometheus-stack), ~5 minutes
make kind-deploy      # build the image, load it, install Postgres + the chart, run the e2e smoke test
make kind-ports       # open every UI on localhost (Ctrl+C closes them)
```

| UI | Address | Login |
|---|---|---|
| Status page | <http://localhost:18080> | – |
| Prometheus | <http://localhost:9090> | – |
| Grafana | <http://localhost:3000> | `admin` / `admin` on first login |
| Alertmanager | <http://localhost:9093> | – |
| Argo CD | <http://localhost:8443> | `admin` / `make kind-argocd-password` |

Every `make kind-*` target uses the kind kubeconfig, whatever your shell has
set, so it cannot reach another cluster by mistake. `make kind-deploy` turns on
the ServiceMonitor, alert rules and dashboard by itself when the monitoring
stack is installed. `make kind-down` deletes the cluster.

#### Watch an alert fire

Start a small web server in the cluster, monitor it, then take it down:

```bash
alias k="kubectl --kubeconfig $PWD/.kube/kind-uptime-watch.yaml"
k -n uptime-watch create deployment demo-site --image=nginx:alpine
k -n uptime-watch expose deployment demo-site --port 80
curl -X POST localhost:18080/api/targets -H "Authorization: Bearer $(make -s kind-token)" \
  -d '{"name":"demo-site","url":"http://demo-site.uptime-watch.svc","interval_seconds":10}'

k -n uptime-watch scale deployment demo-site --replicas=0   # the site goes down
```

About 10 seconds later the status page shows it down; `TargetDown` turns
pending in Prometheus, firing after 2 minutes, and reaches Alertmanager (and
Discord, see [Notifications](#notifications)) 30 seconds after that. Scale it
back to 1 and the alert resolves.

The chart (`deploy/helm/uptime-watch`) installs:

- `api` Deployment (2 replicas, liveness `/healthz`, readiness `/readyz`,
  PodDisruptionBudget) and its Service
- `worker` Deployment (liveness fails if the check loop is stuck) and a Service
  for scraping
- `migrate` Job as a pre-install/pre-upgrade hook (Argo CD runs it as PreSync)
- `prune` CronJob (daily, `concurrencyPolicy: Forbid`)
- optional `sync-targets` Job and its ConfigMap (see [Targets as code](#targets-as-code))
- optional ServiceMonitor, PrometheusRule and Grafana dashboard
- optional Gateway API `HTTPRoute` (`httpRoute` values)
- pods run as non-root with a read-only root filesystem, no capabilities and
  the default seccomp profile

Secrets are never in the chart or in git: it expects an existing Secret with
`DATABASE_URL` and `ADMIN_TOKEN` (`existingSecret`, default `uptime-watch`).

### GitOps with Argo CD

`deploy/argocd/root.yaml` is the only manifest applied by hand. It points Argo
CD at `deploy/argocd/apps/`, which holds one Application per component, ordered
with sync waves:

| Wave | Application | Source |
|---|---|---|
| -3 | `gateway-api` | Gateway API CRDs v1.6.3 from `kubernetes-sigs/gateway-api` |
| -2 | `traefik` | Traefik chart + `deploy/traefik/values.yaml` |
| -1 | `monitoring` | kube-prometheus-stack chart + `deploy/monitoring/values.yaml` |
| 0 | `postgres` | `deploy/kind/postgres.yaml` |
| 1 | `uptime-watch` | `deploy/helm/uptime-watch` |
| 2 | `routes` | `deploy/routes/`: HTTPRoutes for Grafana, Prometheus, Alertmanager, Argo CD |

```bash
make kind-argocd   # installs Argo CD, creates the Secrets, applies the root app
```

The sync waves only order the Applications if Argo CD waits for each one to
be healthy, which it stopped doing by default in 1.8; the bootstrap adds the
Application health check that brings it back. It also refuses to run against
a kubectl context that is not a kind cluster unless `FORCE=1` is set.

Before you run it:

- Use a fresh cluster (`make kind-up` only). Argo CD installs Postgres, the
  monitoring stack and the app itself; mixing it with `make kind-deploy` gives
  two owners for the same resources.
- The image must be pullable: CI pushes it to `ghcr.io` from `main`. GHCR
  packages start private, so make the package public (package settings) or
  add an image pull secret.
- Argo CD reads this repository, so a fork needs its own URL in
  `deploy/argocd/root.yaml` and `deploy/argocd/apps/*.yaml`.

#### Every UI through Traefik

With Argo CD, Traefik is the cluster's entry point. It implements the
[Gateway API](https://gateway-api.sigs.k8s.io/): the chart creates a `Gateway`,
and each component adds an `HTTPRoute` for its host name. The kind node
publishes Traefik's NodePort (30080) on `localhost:8000` (`extraPortMappings`
in `deploy/kind/cluster.yaml`, so the cluster must be created with it), and
`*.localhost` names resolve to this machine:

```bash
make kind-urls
```

| UI | Address |
|---|---|
| Status page | <http://status.localhost:8000> |
| Prometheus | <http://prometheus.localhost:8000> |
| Grafana | <http://grafana.localhost:8000> |
| Alertmanager | <http://alertmanager.localhost:8000> |
| Argo CD | <http://argocd.localhost:8000> |

Unlike `kubectl port-forward`, which is tied to one pod and breaks when a
rollout replaces it, Traefik sends each request to a ready pod behind the
Service. It is plain HTTP on 127.0.0.1 only: fine for a laptop, not for
anything shared.

Deploying a new version means changing `image.tag` in
`deploy/argocd/apps/uptime-watch.yaml` to a commit SHA pushed by CI.

### Targets as code

Targets added with `curl` live only in the database: a new cluster starts
empty, and nothing records who added what. Targets can instead be listed in
the chart values, in git:

```yaml
gitTargets:
  enabled: true
  targets:
    - name: github
      url: https://github.com
      interval_seconds: 30   # optional, default 60
```

The chart turns the list into a ConfigMap, and the `sync-targets` Job (a
post-install/post-upgrade hook, PostSync in Argo CD) makes the database match
it after every deploy, in one transaction:

| The target is… | The sync… |
|---|---|
| in the list, not in the database | creates it (`source: git`) |
| in the list, with another URL, interval or timeout | updates it, keeping its checks |
| in the list, added earlier through the API | takes it over (`source: git`), keeping its checks |
| a git target that left the list | deletes it, with its checks |
| added through the API and not in the list | leaves it alone |

A bad entry (a typo in a field name, an interval below 10 s) fails the Job and
changes nothing. `GET /api/targets` and the status page show each target's
`source`. A git target deleted through the API comes back at the next deploy,
so change git targets in git. To try the sync locally:

```bash
echo '{"targets":[{"name":"go-dev","url":"https://go.dev"}]}' > /tmp/targets.json
DATABASE_URL='postgres://uptime:uptime@localhost:5433/uptime?sslmode=disable' \
  go run ./cmd/uptime-watch sync-targets /tmp/targets.json
```

### Alerts

| Alert | Fires when |
|---|---|
| `TargetDown` | a target's latest checks have been down for 2 minutes |
| `TargetChecksStale` | a target has no new result for 3 intervals (worker stopped or cannot write) |
| `UptimeWorkerDown` | no worker pod can be scraped for 5 minutes |
| `UptimeAPIDown` | no API pod can be scraped for 5 minutes |

### Notifications

Alertmanager sends the alerts of the `uptime-watch` namespace, and every
`critical` alert of the cluster, to a Discord channel, including a message when
they resolve. Alerts are grouped (30 s), and a still-firing alert is repeated
every 4 hours. The webhook URL is a secret, so it is not in git:

```bash
make kind-discord-secret   # asks for the webhook URL without echoing it, stores it as a Secret
```

Without that Secret, Alertmanager still runs and the Discord sends fail, which
the stack's own `AlertmanagerClusterFailedToSendAlerts` alert reports.

## CI

`.github/workflows/ci.yaml` on every pull request and push to `main`:

1. **lint and test**: gofmt, go vet, staticcheck, govulncheck, `go test -race`
   (Postgres tests run in Docker on the runner)
2. **image**: build with BuildKit cache, scan with Trivy (fails on fixable
   HIGH/CRITICAL), push to `ghcr.io` on `main`, tagged with the commit SHA
3. **e2e**: create a kind cluster, install Postgres and the chart with one git
   target, add one target through the API and wait until the worker reports
   both up

## Design decisions

- **Scheduling in the database, not in memory.** In-memory timers would check
  every target once per worker replica. Claiming with `SKIP LOCKED` lets
  replicas scale out, survives restarts and costs one small query per poll.
  The trade-off: `next_check_at` moves forward when a target is claimed, so a
  worker that crashes mid-check loses that one check; the target is checked
  again one interval later.
- **Migrations as a separate step.** Running them at start-up would let two
  pods migrate at once. `uptime-watch migrate` runs once per release as a Job.
- **Constraints in the database.** Interval and timeout rules are `CHECK`
  constraints, so even a manual `INSERT` cannot store a bad target; the API
  turns constraint violations into readable 400 errors.
- **Alerting in the platform, not the app.** The app reports state through
  metrics; when and whom to notify is Alertmanager configuration.
- **Standard library first.** `net/http` routing (Go 1.22 patterns) and
  `html/template`; external modules are only `pgx`, `goose`,
  `prometheus/client_golang`, and `testcontainers-go` for tests.

## Ideas for later

- Show TLS certificate expiry and alert a few days before it.
- Check that a page contains a word, to catch "200 with an error page".
- A page per target with its response-time history and an add-target form.
- Run checks from two regions, to tell "the site is down" from "our network is".
- Postgres through an operator (CloudNativePG) with backups, instead of a
  plain StatefulSet.

## Layout

```
cmd/uptime-watch/        commands: serve, worker, check, migrate, prune, sync-targets
internal/checker/        one HTTP check with a timeout; the up/down rule
internal/store/          Postgres: targets, checks, claiming, migrations (embedded)
internal/worker/         check loop, concurrency limit, graceful stop
internal/targetsfile/    reads and checks the targets file from git
internal/api/            handlers, status page template, admin token
internal/metrics/        Prometheus metrics and the status collector
internal/config/         environment variables
internal/testdb/         real Postgres in Docker for tests
deploy/helm/             Helm chart
deploy/kind/             kind cluster, development Postgres, e2e script
deploy/monitoring/       kube-prometheus-stack values
deploy/traefik/          Traefik values (Gateway API controller)
deploy/routes/           HTTPRoutes for the platform UIs
deploy/argocd/           Argo CD root app and Applications
docs/images/             screenshots for this README
```
