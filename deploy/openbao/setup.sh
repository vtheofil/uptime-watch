#!/usr/bin/env bash
# Start the local OpenBao and get it ready for uptime-watch. Safe to run again:
# each step is skipped when it is already done. `make openbao-up` runs this.
#
#   1. start the container (compose.yaml), on kind's Docker network
#   2. initialise it, only the first time: this creates the unseal key and the
#      root token, saved to $OPENBAO_INIT_FILE (mode 600, outside the repo,
#      never printed). Lose that file and the secrets cannot be opened again.
#   3. unseal it: OpenBao starts sealed after every restart, unable to decrypt
#      anything until it gets the unseal key
#   4. enable the key/value store at secret/ and the read-only policy
#
# One unseal key and a root token kept in a file are fine for a laptop. In
# production the key is split between several people (or a cloud KMS unseals
# automatically) and the root token is revoked after setup.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/lib.sh"
PROJECT=${OPENBAO_PROJECT:-uptime-watch-openbao}

docker network inspect kind >/dev/null 2>&1 || {
  echo "the Docker network 'kind' does not exist yet: run make kind-up first"
  exit 1
}

echo "==> start OpenBao"
docker compose -p "$PROJECT" -f "$HERE/compose.yaml" up -d
status=""
for _ in $(seq 30); do
  status=$(seal_status 2>/dev/null) && break
  sleep 1
done
[[ -n $status ]] || { echo "OpenBao does not answer on $OPENBAO_ADDR"; exit 1; }

if [[ $(jq -r .initialized <<<"$status") != true ]]; then
  echo "==> initialise (first run only)"
  if [[ -e $OPENBAO_INIT_FILE ]]; then
    echo "OpenBao is new, but $OPENBAO_INIT_FILE already exists (from an older OpenBao?)."
    echo "Move it away first, so its keys are not overwritten."
    exit 1
  fi
  mkdir -p "$(dirname "$OPENBAO_INIT_FILE")"
  (
    umask 077 # the file is created readable by you only
    curl -sS --fail-with-body -X PUT "$OPENBAO_ADDR/v1/sys/init" \
      --data '{"secret_shares": 1, "secret_threshold": 1}' >"$OPENBAO_INIT_FILE"
  )
  echo "unseal key and root token saved to $OPENBAO_INIT_FILE"
fi
[[ -r $OPENBAO_INIT_FILE ]] || {
  echo "OpenBao is initialised, but $OPENBAO_INIT_FILE is missing: without its unseal key it cannot be opened"
  exit 1
}

if [[ $(seal_status | jq -r .sealed) == true ]]; then
  echo "==> unseal"
  # The key goes in the request body through stdin, never on a command line.
  jq '{key: .keys_base64[0]}' "$OPENBAO_INIT_FILE" \
    | curl -sS --fail-with-body -X PUT "$OPENBAO_ADDR/v1/sys/unseal" --data @- >/dev/null
fi

# Unsealed is not yet ready: Raft first elects a leader (itself) and only then
# accepts requests. /v1/sys/health answers 200 once this server is active.
for _ in $(seq 30); do
  [[ $(curl -s -o /dev/null -w '%{http_code}' "$OPENBAO_ADDR/v1/sys/health") == 200 ]] && break
  sleep 1
done

if ! api GET sys/mounts | jq -e '.data["secret/"]' >/dev/null; then
  echo "==> enable the key/value store (version 2) at secret/"
  api POST sys/mounts/secret --data '{"type": "kv", "options": {"version": "2"}}'
fi

echo "==> policy uptime-watch-read"
jq -Rs '{policy: .}' "$HERE/uptime-watch-read.hcl" | api PUT sys/policies/acl/uptime-watch-read --data @-

echo
echo "OpenBao is ready: UI on $OPENBAO_ADDR/ui (token: the root_token in $OPENBAO_INIT_FILE)"
if api GET secret/data/uptime-watch/discord >/dev/null 2>&1; then
  echo "The Discord webhook is stored."
else
  echo "No Discord webhook stored yet: run make openbao-discord"
fi
