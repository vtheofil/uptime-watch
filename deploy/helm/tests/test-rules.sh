#!/usr/bin/env bash
# Unit-test the chart's alert rules with promtool (`make test-rules`).
#
# 1. render the chart with the values Argo CD uses (deploy/argocd/apps/uptime-watch.yaml)
# 2. take the rule groups out of the PrometheusRule (promtool reads plain rule files)
# 3. run the cases in rules.test.yaml: given these series, at this time, these
#    alerts fire. Prometheus' own rule engine evaluates them, nothing is faked.
#
# Needs helm and Docker (yq and promtool run as containers).
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
YQ_IMAGE=mikefarah/yq:4.44.3
PROMETHEUS_IMAGE=quay.io/prometheus/prometheus:v3.16.0 # the version the cluster runs

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp "$HERE/rules.test.yaml" "$work/"

yq() { docker run --rm -i "$YQ_IMAGE" "$@"; }

yq '.spec.source.helm.valuesObject' <"$ROOT/deploy/argocd/apps/uptime-watch.yaml" >"$work/values.yaml"
helm template uptime-watch "$ROOT/deploy/helm/uptime-watch" -n uptime-watch -f "$work/values.yaml" \
  --show-only templates/monitoring.yaml \
  | yq 'select(.kind == "PrometheusRule") | .spec' >"$work/rules.yaml"

# promtool runs as the user "nobody" in its container: let it read the files.
chmod -R a+rX "$work"
docker run --rm -v "$work:/w:ro" -w /w --entrypoint promtool "$PROMETHEUS_IMAGE" test rules rules.test.yaml
