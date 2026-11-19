#!/usr/bin/env bash
# End-to-end test on a kind cluster:
#   Postgres -> Secret -> helm install (migrate Job runs first, sync-targets
#   Job last, creating the git target "self") -> add the target "self-api"
#   through the API -> wait until the worker has checked both ->
#   check the status in the API and in /metrics.
# Usage: deploy/kind/e2e.sh <image>   (the image must already be loaded into kind)
set -euo pipefail

IMAGE=${1:?usage: e2e.sh <image>}
NS=uptime-watch
HERE=$(cd "$(dirname "$0")" && pwd)
CHART="$HERE/../helm/uptime-watch"

step() { printf '\n==> %s\n' "$*"; }

step "namespace and Postgres"
kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f -
if ! kubectl -n "$NS" get secret postgres >/dev/null 2>&1; then
  PGPASS=$(head -c 18 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9')
  kubectl -n "$NS" create secret generic postgres --from-literal=password="$PGPASS"
fi
kubectl apply -f "$HERE/postgres.yaml"
kubectl -n "$NS" rollout status statefulset/postgres --timeout=180s

step "app Secret (DATABASE_URL + ADMIN_TOKEN), made from the Postgres password"
PGPASS=$(kubectl -n "$NS" get secret postgres -o jsonpath='{.data.password}' | base64 -d)
if ! kubectl -n "$NS" get secret uptime-watch >/dev/null 2>&1; then
  kubectl -n "$NS" create secret generic uptime-watch \
    --from-literal=DATABASE_URL="postgres://uptime:${PGPASS}@postgres.${NS}.svc:5432/uptime?sslmode=disable" \
    --from-literal=ADMIN_TOKEN="$(head -c 24 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9')"
fi
TOKEN=$(kubectl -n "$NS" get secret uptime-watch -o jsonpath='{.data.ADMIN_TOKEN}' | base64 -d)

step "helm install $IMAGE"
# With the monitoring stack installed (its CRDs exist), also install the
# ServiceMonitor, alert rules and Grafana dashboard. Without it, skip them.
MONITORING_ARGS=()
if kubectl get crd servicemonitors.monitoring.coreos.com >/dev/null 2>&1; then
  echo "monitoring stack found: enabling ServiceMonitor, PrometheusRule and dashboard"
  MONITORING_ARGS=(--set metrics.serviceMonitor.enabled=true
                   --set metrics.prometheusRule.enabled=true
                   --set metrics.dashboard.enabled=true)
fi
helm upgrade --install uptime-watch "$CHART" -n "$NS" --wait --timeout 3m \
  --set image.repository="${IMAGE%:*}" \
  --set image.tag="${IMAGE##*:}" \
  --set image.pullPolicy=Never \
  --set worker.pollInterval=1s \
  --set gitTargets.enabled=true \
  --set-json "gitTargets.targets=[{\"name\":\"self\",\"url\":\"http://uptime-watch.${NS}.svc/healthz\",\"interval_seconds\":10}]" \
  "${MONITORING_ARGS[@]}" \
  ${HELM_EXTRA_ARGS:-}
kubectl -n "$NS" get pods

step "the sync-targets Job ran after the install"
kubectl -n "$NS" logs job/uptime-watch-sync-targets

step "port-forward the API"
kubectl -n "$NS" port-forward svc/uptime-watch 18080:80 >/dev/null 2>&1 &
PF=$!
trap 'kill $PF 2>/dev/null || true' EXIT
for _ in $(seq 30); do curl -fsS localhost:18080/readyz >/dev/null 2>&1 && break; sleep 1; done
curl -fsS localhost:18080/readyz

# target NAME prints that target's JSON object from /api/targets, one line.
target() {
  curl -fsS localhost:18080/api/targets | sed 's/},{"id"/}\n{"id"/g' | grep "\"name\":\"$1\"," || true
}

step "the git target from the values is there"
target self | grep '"source":"git"' || { echo "self is missing or not a git target"; exit 1; }

step "add a target through the API: the API's own /healthz, reached through the Service"
code=$(curl -s -o /tmp/e2e-create.json -w '%{http_code}' -X POST localhost:18080/api/targets \
  -H "Authorization: Bearer $TOKEN" \
  -d "{\"name\":\"self-api\",\"url\":\"http://uptime-watch.${NS}.svc/healthz\",\"interval_seconds\":10}")
cat /tmp/e2e-create.json; echo
[[ $code == 201 || $code == 409 ]] || { echo "create failed: $code"; exit 1; }

step "wait until the worker has checked both"
both_up() { target self | grep -q '"ok":true' && target self-api | grep -q '"ok":true'; }
for _ in $(seq 60); do both_up && break; sleep 1; done
curl -fsS localhost:18080/api/targets; echo
both_up || { echo "a target never came up"; exit 1; }

step "metrics"
curl -fsS localhost:18080/metrics | grep 'uptime_target_up{target="self"} 1'

echo
echo "e2e passed"
