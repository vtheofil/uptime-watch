#!/usr/bin/env bash
# Install Argo CD into the current cluster, create the Secrets that must
# never be in git, and hand everything else to Argo CD via the root app.
# Prefer `make kind-argocd`, which always targets the kind cluster.
#
# Needs: a fresh cluster, the repo on GitHub (public) and the image in ghcr.io
# (pushed by CI on main).
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ARGOCD_VERSION=v3.6.1
NS=uptime-watch

# Safety: this installs things cluster-wide, so refuse anything but a kind
# cluster unless asked explicitly.
CONTEXT=$(kubectl config current-context)
if [[ $CONTEXT != kind-* && ${FORCE:-} != 1 ]]; then
  echo "current kubectl context is '$CONTEXT', not a kind cluster."
  echo "Run 'make kind-argocd', or set FORCE=1 if you really mean this cluster."
  exit 1
fi
echo "==> cluster: $CONTEXT"

echo "==> Argo CD $ARGOCD_VERSION"
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n argocd --server-side --force-conflicts \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml"

# Since Argo CD 1.8 an Application counts as healthy as soon as it exists, so
# sync waves between Applications do not wait for each other. This health
# check makes the root app wait until each child app is really healthy, so
# wave -1 (monitoring, which brings the CRDs) is done before wave 1 starts.
kubectl -n argocd patch configmap argocd-cm --type merge -p '{"data":{"resource.customizations.health.argoproj.io_Application":"hs = {}\nhs.status = \"Progressing\"\nhs.message = \"\"\nif obj.status ~= nil and obj.status.health ~= nil then\n  hs.status = obj.status.health.status\n  if obj.status.health.message ~= nil then\n    hs.message = obj.status.health.message\n  end\nend\nreturn hs\n"}}'

# Traefik sends plain HTTP to argocd-server (http://argocd.localhost:8000).
# Without server.insecure the server redirects every request to HTTPS, which
# loops. This is for the laptop only: elsewhere, terminate TLS at the Gateway.
kubectl -n argocd patch configmap argocd-cmd-params-cm --type merge -p '{"data":{"server.insecure":"true"}}'
kubectl -n argocd rollout restart deploy/argocd-server

kubectl -n argocd rollout status deploy/argocd-server --timeout=5m
kubectl -n argocd rollout status deploy/argocd-repo-server --timeout=5m
kubectl -n argocd rollout status statefulset/argocd-application-controller --timeout=5m

echo "==> Secrets (created here, not in git)"
kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f -
if ! kubectl -n "$NS" get secret postgres >/dev/null 2>&1; then
  kubectl -n "$NS" create secret generic postgres \
    --from-literal=password="$(head -c 18 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9')"
fi
PGPASS=$(kubectl -n "$NS" get secret postgres -o jsonpath='{.data.password}' | base64 -d)
if ! kubectl -n "$NS" get secret uptime-watch >/dev/null 2>&1; then
  kubectl -n "$NS" create secret generic uptime-watch \
    --from-literal=DATABASE_URL="postgres://uptime:${PGPASS}@postgres.${NS}.svc:5432/uptime?sslmode=disable" \
    --from-literal=ADMIN_TOKEN="$(head -c 24 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9')"
fi

# External secrets (the Discord webhook) come from OpenBao, which runs outside
# the cluster. The cluster only needs a token to read them: the one secret
# that still has to be put in by hand.
echo "==> OpenBao token for External Secrets"
source "$HERE/../openbao/lib.sh"
if openbao_ready; then
  "$HERE/../openbao/k8s-token.sh"
else
  echo "OpenBao is not running or is sealed. The rest works; the 'secrets' app stays"
  echo "Degraded until you run: make openbao-up && make kind-openbao-token"
fi

echo "==> root app"
kubectl apply -f "$HERE/root.yaml"

cat <<'EOF'

Argo CD is syncing from GitHub. To follow it:
  make kind-urls              # every UI, once Traefik is up (a few minutes)
  make kind-argocd-password   # user: admin
Before Traefik is up, `make kind-ports` reaches Argo CD on http://localhost:8443.
EOF
