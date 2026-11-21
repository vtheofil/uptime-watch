# Shared by the OpenBao scripts (sourced, not run). They talk to OpenBao's HTTP
# API with curl: everything the bao CLI or the UI does is one of these calls.
OPENBAO_PORT=${OPENBAO_PORT:-8200}
OPENBAO_ADDR=http://127.0.0.1:$OPENBAO_PORT
# Unseal key and root token, written once by setup.sh. Outside the repository.
OPENBAO_INIT_FILE=${OPENBAO_INIT_FILE:-$HOME/.config/uptime-watch/openbao-init.json}

# api METHOD PATH [curl options]: call the API with the root token. The token
# goes to curl through a file descriptor (-H @file), so it never appears in a
# command line that `ps` could show.
api() {
  local method=$1 path=$2
  shift 2
  curl -sS --fail-with-body -X "$method" "$OPENBAO_ADDR/v1/$path" \
    -H @<(printf 'X-Vault-Token: %s\n' "$(jq -r .root_token "$OPENBAO_INIT_FILE")") "$@"
}

# seal_status prints {"initialized":..,"sealed":..} or fails if OpenBao does not answer.
seal_status() {
  curl -sS --fail-with-body "$OPENBAO_ADDR/v1/sys/seal-status"
}

# openbao_ready succeeds when OpenBao answers, is initialised and unsealed.
openbao_ready() {
  seal_status 2>/dev/null | jq -e '.initialized and (.sealed | not)' >/dev/null 2>&1
}
