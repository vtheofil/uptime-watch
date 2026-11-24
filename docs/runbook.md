# Runbook

What to do when something is wrong. Every alert links to its section here
(`runbook_url`). Commands assume the kind setup from the README; `k` is

```bash
alias k="kubectl --kubeconfig $PWD/.kube/kind-uptime-watch.yaml"
```

so it can never reach another cluster by mistake.

- [Alerts](#alerts): [TargetDown](#targetdown) ·
  [TargetChecksStale](#targetchecksstale) · [UptimeWorkerDown](#uptimeworkerdown) ·
  [UptimeAPIDown](#uptimeapidown) · [No Discord messages](#no-discord-messages)
- [Deploys](#deploys): [an app stays Progressing](#an-app-stays-progressing) ·
  [the root sync failed](#the-root-sync-failed) ·
  [sync-targets failed](#the-sync-targets-job-failed) ·
  [migrate failed](#the-migrate-job-failed) ·
  [secrets is Degraded](#secrets-is-degraded) · [roll back](#roll-back-a-release)
- [Access](#access): [a UI does not open](#a-ui-does-not-open)
- [Rebuild the cluster](#rebuild-the-cluster)

## Alerts

### TargetDown

A target's latest checks have failed for 2 minutes.

1. Open the status page (`make kind-urls`): the "Response" column shows the
   status code or the error (timeout, DNS, connection refused).
2. Recent results: `curl http://status.localhost:8000/api/targets/<id>/checks?limit=20`.
3. Check the site from outside the cluster (`curl -I <url>`). Down for
   everyone: not our problem, wait or tell its owner. Up from outside: the
   cluster cannot reach it (DNS, egress). Our image is distroless (no shell),
   so test from a throwaway pod:
   `k -n uptime-watch run tmp --rm -it --restart=Never --image=busybox:1.36 -- wget -qO- -T 5 <url>`.
4. Several outside targets down at the same time while `self` (inside the
   cluster) stays up: most likely our own network, not theirs. The status page
   shows `lookup … server misbehaving` or `context deadline exceeded`. Check
   that this machine is online (`curl -I https://github.com`), then CoreDNS,
   which sends every name outside the cluster to the host's DNS:
   `k -n kube-system logs -l k8s-app=kube-dns --since=30m | grep ERROR`.
   `i/o timeout` towards that server means there is no DNS outside the
   cluster (for example a laptop that woke up without Wi-Fi). Nothing to fix
   in the cluster: the checks recover when the network does, and Alertmanager
   then sends everything it queued at once, often firing and resolved together.
5. A target that is meant to be down for a while: silence the alert in
   Alertmanager (`http://alertmanager.localhost:8000`) rather than deleting it.

### TargetChecksStale

A target has no new result for 3 intervals. Nothing is being checked, so
`TargetDown` cannot fire either.

1. `k -n uptime-watch get pods`: is the worker running? Restarting?
2. `k -n uptime-watch logs deploy/uptime-watch-worker --tail=50`: database
   errors? The worker's liveness probe fails when its loop is stuck, so a
   stuck worker is restarted by itself.
3. Database reachable? `k -n uptime-watch get pods postgres-0`, and the API's
   `/readyz` (it pings the database).

### UptimeWorkerDown

Prometheus cannot scrape any worker pod for 5 minutes.

1. `k -n uptime-watch get deploy uptime-watch-worker` and
   `k -n uptime-watch describe pod -l app.kubernetes.io/component=worker`:
   image pull errors, crash loops, pending (resources)?
2. Logs of the last crash: `k -n uptime-watch logs deploy/uptime-watch-worker --previous`.
3. Pods fine but no scrape: check the target in Prometheus
   (`http://prometheus.localhost:8000/targets`).

### UptimeAPIDown

No API pod can be scraped for 5 minutes: the status page, the API and
`uptime_target_up` are gone, so `TargetDown` cannot fire.

1. As for the worker, with `-l app.kubernetes.io/component=api`.
2. Pods running but not ready: `/readyz` fails when the database does not
   answer. Check `postgres-0`.

### No Discord messages

Alerts fire in Alertmanager but nothing reaches Discord. The stack's own
`AlertmanagerClusterFailedToSendAlerts` fires too.

1. Is the Secret there? `k -n monitoring get secret alertmanager-discord`.
   It comes from OpenBao through External Secrets. If it is missing, check
   `k -n monitoring get externalsecret alertmanager-discord`: not Ready means
   OpenBao is down or sealed (`make openbao-up`), the webhook is not stored
   (`make openbao-discord`), or the cluster has no OpenBao token
   (`make kind-openbao-token`); then `make kind-refresh-secrets`. Without
   OpenBao, `make kind-discord-secret` sets it by hand. No restart is needed:
   the kubelet adds it to the pod within a minute or two, and Alertmanager
   reads it on every send.
2. Alertmanager's log: `k -n monitoring logs statefulset/alertmanager-monitoring-kube-prometheus-alertmanager -c alertmanager --tail=30`.
   `404`/`401` from Discord: the webhook was deleted or the URL is wrong; make
   a new one in the channel settings and store it again.

## Deploys

### An app stays Progressing

`k -n argocd get applications`. A new app is Progressing for a minute or two;
on a fresh cluster the monitoring stack takes a few minutes. Longer than
5 minutes: find the resource that is not healthy:

```bash
k -n argocd get application <app> -o jsonpath='{range .status.resources[*]}{.kind}{"\t"}{.name}{"\t"}{.health.status}{"\t"}{.health.message}{"\n"}{end}'
k -n argocd get application <app> -o jsonpath='{.status.operationState.message}{"\n"}'
```

Or open the app in the Argo CD UI (`http://argocd.localhost:8000`) and look
for the yellow or red resource.

### The root sync failed

`root` shows `OutOfSync` and its operation says "one or more synchronization
tasks completed unsuccessfully (retried N times)". A wave waited for an app
that was Degraded for longer than the retries lasted; typically Prometheus and
Alertmanager on a fresh cluster. Once the retries are used up, Argo CD does not
try the same commit again by itself. When the app it waited for is healthy,
start the sync again:

```bash
k -n argocd patch application root --type merge -p '{"operation":{"initiatedBy":{"username":"runbook"},"sync":{}}}'
```

(the same as SYNC in the UI). A new commit also starts a new sync.

### The sync-targets Job failed

The PostSync hook failed, so Argo CD shows the sync as failed; the app keeps
running with the targets it had (the sync is one transaction: nothing
changed).

```bash
k -n uptime-watch logs job/uptime-watch-sync-targets
```

The message names the problem: `targets[2] "x": url must be ...`, `unknown
field "intervall"`, `listed twice`, `interval_seconds must be at least 10`.
Fix `gitTargets` in `deploy/argocd/apps/uptime-watch.yaml`, commit, push.

### The migrate Job failed

The PreSync hook failed: the new version did not roll out, the old pods keep
running. `k -n uptime-watch logs job/uptime-watch-migrate`. A migration that
works on an empty database can fail on one with rows (for example a new
`NOT NULL` column without a default). Fix it in a new migration, never by
editing one that has run.

### secrets is Degraded

`secrets` is Degraded, and so is `root`: the root app takes its health from
its Applications, so one Degraded child is enough. Its sync is still
`Succeeded`, and nothing else is held up (`secrets` is the last wave).
The ExternalSecrets cannot read OpenBao:

```bash
k get clustersecretstore openbao
k -n monitoring get externalsecret alertmanager-discord -o jsonpath='{.status.conditions[0].message}{"\n"}'
```

External Secrets' log has the exact reason:
`k -n external-secrets logs deploy/external-secrets --since=15m | grep error`.

| Cause | How to tell | Fix |
|---|---|---|
| OpenBao is not running | `docker ps` has no `uptime-watch-openbao` | `make openbao-up` |
| OpenBao restarted (a reboot) and is sealed | `curl -s 127.0.0.1:8200/v1/sys/seal-status` says `"sealed":true` | `make openbao-up` |
| The cluster has no OpenBao token | the log says `cannot get Kubernetes secret "openbao-token"` | `make kind-openbao-token` |
| The token expired (it lasts 32 days) | the log says `permission denied` | `make kind-openbao-token` |
| The webhook is not stored | the store is Ready, the ExternalSecret is not | `make openbao-discord` |

Then `make kind-refresh-secrets`. After failing for a long time, External
Secrets waits longer and longer between attempts (up to about 16 minutes);
the refresh changes the ExternalSecret, which makes it try at once. Meanwhile
Alertmanager keeps the last copy of the Secret, so Discord keeps working.

### Roll back a release

The running version is `image.tag` in `deploy/argocd/apps/uptime-watch.yaml`.

```bash
git log --oneline -- deploy/argocd/apps/uptime-watch.yaml   # find the commit that changed it
git revert <commit> && git push
```

Argo CD deploys the previous tag. `kubectl rollout undo` does not work here:
`selfHeal` puts back what git says. Migrations are not undone by a rollback;
they are written so the previous version still works with the new schema.

## Access

### A UI does not open

- `make kind-urls` lists the addresses (Traefik on `localhost:8000`).
- `404 page not found` from Traefik: no `HTTPRoute` matches that host name.
  `k get httproute -A` lists them; check the spelling of the host.
- Connection refused: is the Traefik pod up (`k -n traefik get pods`)? Is the
  cluster's port mapping there (`docker ps --filter name=uptime-watch-control-plane`
  should show `127.0.0.1:8000->30080/tcp`)? A cluster created before the
  mapping existed must be rebuilt.
- `kubectl port-forward` reporting "lost connection to pod": the pod it was
  attached to was replaced (a rollout). Start it again, or use the Traefik
  addresses, which follow the Service.

## Rebuild the cluster

Everything comes back from git except the database contents (check history
and targets added through the API).

```bash
make kind-down && make kind-up
make openbao-up             # OpenBao survives the rebuild; this unseals it
make kind-argocd            # Argo CD, the generated Secrets, the OpenBao token, the root app
make kind-argocd-password   # new cluster, new password
```

Then watch `k -n argocd get applications -w` until every app is Synced and
Healthy (5 to 10 minutes on a fresh cluster), and open `make kind-urls`.
