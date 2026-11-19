#!/usr/bin/env bash
# Install kube-prometheus-stack into the current cluster with Helm.
# (With Argo CD, deploy/argocd/apps/monitoring.yaml does the same thing.)
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
VERSION=93.2.2

# --repo avoids `helm repo add`, which would change your global Helm settings.
helm upgrade --install monitoring kube-prometheus-stack \
  --repo https://prometheus-community.github.io/helm-charts --version "$VERSION" \
  --namespace monitoring --create-namespace \
  --values "$HERE/values.yaml" \
  --wait --timeout 10m

echo
echo "Grafana:      kubectl -n monitoring port-forward svc/monitoring-grafana 3000:80   (admin / admin)"
echo "Prometheus:   kubectl -n monitoring port-forward svc/monitoring-kube-prometheus-prometheus 9090"
echo "Alertmanager: kubectl -n monitoring port-forward svc/monitoring-kube-prometheus-alertmanager 9093"
