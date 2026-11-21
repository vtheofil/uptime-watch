#!/usr/bin/env bash
# Store the Discord webhook URL in OpenBao at secret/uptime-watch/discord.
# The URL is read without echo and sent in the request body through stdin:
# it is never printed, saved to a file or put on a command line.
# External Secrets Operator copies it into the cluster (deploy/secrets/).
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/lib.sh"

openbao_ready || { echo "OpenBao is not running or is sealed: run make openbao-up"; exit 1; }

read -rsp "Paste the Discord webhook URL (hidden), then Enter: " url
echo
case $url in
  https://discord.com/api/webhooks/*) ;;
  *) echo "that does not look like a Discord webhook URL"; exit 1 ;;
esac

printf '%s' "$url" | jq -Rs '{data: {"webhook-url": .}}' \
  | api POST secret/data/uptime-watch/discord --data @- >/dev/null
echo "stored in OpenBao; the cluster gets it within 15 minutes (or at once: make kind-refresh-secrets)"
