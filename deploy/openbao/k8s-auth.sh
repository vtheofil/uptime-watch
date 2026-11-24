#!/usr/bin/env bash
# Let External Secrets Operator (ESO) in the kind cluster log in to OpenBao
# with Kubernetes auth: no OpenBao token is stored in the cluster.
#
# One login, step by step:
#   1. ESO asks Kubernetes for a short-lived token of the ServiceAccount
#      external-secrets/openbao-auth (deploy/secrets/openbao-auth.yaml).
#   2. ESO sends that token to OpenBao: auth/kubernetes/login, role external-secrets.
#   3. OpenBao asks the cluster's API server whether the token is valid and
#      whose it is (a TokenReview). It makes that request with the same token,
#      which the ServiceAccount allows through system:auth-delegator.
#   4. If it is openbao-auth in external-secrets, with audience "openbao",
#      OpenBao answers with a token of its own: policy uptime-watch-read, 1 hour.
#
# OpenBao must know the API server's address and trust its CA. A new kind
# cluster has a new CA, so bootstrap.sh runs this; `make kind-openbao-auth`
# runs it again. Safe to repeat.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/lib.sh"

# Only for kind: the API server address below is the one of kind's control
# plane container. `make kind-openbao-auth` always points kubectl at kind.
context=$(kubectl config current-context)
if [[ $context != kind-* ]]; then
  echo "current kubectl context is '$context', not a kind cluster. Run 'make kind-openbao-auth'."
  exit 1
fi
cluster=${context#kind-}

openbao_ready || { echo "OpenBao is not running or is sealed: run make openbao-up"; exit 1; }

if ! api GET sys/auth | jq -e '.data["kubernetes/"]' >/dev/null; then
  echo "==> enable Kubernetes auth at auth/kubernetes"
  api POST sys/auth/kubernetes --data '{"type": "kubernetes"}'
fi

echo "==> point it at the kind cluster '$cluster'"
# The API server as OpenBao reaches it on kind's Docker network (its
# certificate is valid for the container name), and the CA that signed it.
# disable_local_ca_jwt: OpenBao does not run in a pod, so there is no local
# service account to fall back on.
ca=$(kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d)
jq -n --arg host "https://$cluster-control-plane:6443" --arg ca "$ca" \
  '{kubernetes_host: $host, kubernetes_ca_cert: $ca, disable_local_ca_jwt: true}' \
  | api POST auth/kubernetes/config --data @- >/dev/null

echo "==> role external-secrets: openbao-auth in external-secrets may read uptime-watch secrets"
api POST auth/kubernetes/role/external-secrets --data '{
  "bound_service_account_names": ["openbao-auth"],
  "bound_service_account_namespaces": ["external-secrets"],
  "audience": "openbao",
  "token_policies": ["uptime-watch-read"],
  "token_ttl": "1h",
  "token_max_ttl": "1h"
}' >/dev/null

# Try one login the way ESO does, if the ServiceAccount exists yet (Argo CD
# creates it, in wave 3). Prints only the policies of the token OpenBao returns.
if kubectl -n external-secrets get serviceaccount openbao-auth >/dev/null 2>&1; then
  echo "==> test login as external-secrets/openbao-auth"
  jwt=$(kubectl -n external-secrets create token openbao-auth --duration 10m \
    --audience openbao --audience "$(kubectl get --raw /.well-known/openid-configuration | jq -r .issuer)")
  jq -n --arg jwt "$jwt" '{role: "external-secrets", jwt: $jwt}' \
    | curl -sS --fail-with-body -X POST "$OPENBAO_ADDR/v1/auth/kubernetes/login" --data @- \
    | jq -r '"logged in, policies: \(.auth.policies | join(", ")), valid \(.auth.lease_duration) s"'
else
  echo "(no ServiceAccount external-secrets/openbao-auth yet: Argo CD creates it; run this again to test a login)"
fi
