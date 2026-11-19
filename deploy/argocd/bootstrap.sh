#!/usr/bin/env bash
# Install Argo CD into the current cluster, create the Secrets that must
# never be in git, and hand everything else to Argo CD via the root app.
#
# Needs: the repo pushed to GitHub (public), and the image in ghcr.io
# (pushed by CI on main; make the package public, or add an imagePullSecret).
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ARGOCD_VERSION=v3.6.1
NS=uptime-watch

echo "==> Argo CD $ARGOCD_VERSION"
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n argocd --server-side --force-conflicts \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml"
kubectl -n argocd rollout status deploy/argocd-server --timeout=5m
kubectl -n argocd rollout status deploy/argocd-repo-server --timeout=5m

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

echo "==> root app"
kubectl apply -f "$HERE/root.yaml"

cat <<'EOF'

Argo CD is syncing. To watch it:
  kubectl -n argocd get applications -w
  kubectl -n argocd port-forward svc/argocd-server 8443:443      # https://localhost:8443
  user: admin, password:
  kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d; echo
EOF
