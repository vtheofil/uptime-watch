#!/usr/bin/env bash
# Make the SSH deploy key that Argo CD Image Updater pushes to GitHub with,
# and store its private half in OpenBao (secret/uptime-watch/image-updater).
# External Secrets copies it into the cluster (deploy/secrets/).
#
# The private key exists only in a temporary directory while this runs, and is
# sent to OpenBao through stdin. The public key is printed: add it on GitHub,
# repository Settings > Deploy keys > Add deploy key, with "Allow write access".
#
# Refuses to replace a stored key unless FORCE=1: a new key also needs the old
# one deleted on GitHub.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/lib.sh"

openbao_ready || { echo "OpenBao is not running or is sealed: run make openbao-up"; exit 1; }
if api GET secret/data/uptime-watch/image-updater >/dev/null 2>&1 && [[ ${FORCE:-} != 1 ]]; then
  echo "A deploy key is already stored. FORCE=1 makes a new one (then replace the key on GitHub)."
  exit 1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
ssh-keygen -q -t ed25519 -N "" -C "argocd-image-updater (uptime-watch)" -f "$tmp/key"
jq -Rs '{data: {"ssh-private-key": .}}' <"$tmp/key" \
  | api POST secret/data/uptime-watch/image-updater --data @- >/dev/null

cat <<EOF
The private key is stored in OpenBao (secret/uptime-watch/image-updater).
Add this public key on GitHub: Settings > Deploy keys > Add deploy key,
title "argocd-image-updater", and tick "Allow write access":

$(cat "$tmp/key.pub")

Then: make kind-refresh-secrets
EOF
