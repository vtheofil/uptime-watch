#!/usr/bin/env bash
# Give External Secrets Operator in the cluster a way into OpenBao: create a
# token that can only read the uptime-watch secrets (policy uptime-watch-read)
# and store it as the Secret external-secrets/openbao-token.
#
# This is the one secret a new cluster needs by hand ("secret zero"); every
# other external secret then comes from OpenBao. bootstrap.sh runs this when
# OpenBao is up; `make kind-openbao-token` runs it again.
#
# The token is periodic: it lives 32 days unless renewed, and External Secrets
# does not renew it. Rebuilding the cluster makes a new one. Without any static
# token, OpenBao's Kubernetes auth would check the pod's service account
# instead: the next step after this one.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/lib.sh"

# Safety: this writes a Secret into the current kubectl context, so refuse
# anything but a kind cluster, like bootstrap.sh does. `make kind-openbao-token`
# always points kubectl at the kind cluster.
context=$(kubectl config current-context)
if [[ $context != kind-* && ${FORCE:-} != 1 ]]; then
  echo "current kubectl context is '$context', not a kind cluster."
  echo "Run 'make kind-openbao-token', or set FORCE=1 if you really mean this cluster."
  exit 1
fi

openbao_ready || { echo "OpenBao is not running or is sealed: run make openbao-up"; exit 1; }

echo "==> read-only OpenBao token for External Secrets"
token=$(api POST auth/token/create-orphan \
  --data '{"policies": ["uptime-watch-read"], "period": "768h", "display_name": "external-secrets-kind"}' \
  | jq -r .auth.client_token)
[[ -n $token && $token != null ]] || { echo "no token in OpenBao's reply"; exit 1; }

kubectl create namespace external-secrets --dry-run=client -o yaml | kubectl apply -f - >/dev/null
printf '%s' "$token" | kubectl -n external-secrets create secret generic openbao-token \
  --from-file=token=/dev/stdin --dry-run=client -o yaml | kubectl apply -f -
