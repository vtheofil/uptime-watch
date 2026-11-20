# 0006. Gateway API with Traefik for traffic into the cluster

- Status: Accepted
- Date: 2026-11-20

## Context

The UIs (status page, Grafana, Prometheus, Alertmanager, Argo CD) were opened
with `kubectl port-forward`. A port-forward attaches to one pod: when a
rollout replaced that pod, the forward broke silently and only failed at the
next request. We wanted one entry point that routes by host name to the
Services, like a real cluster.

## Decision

- Use the **Gateway API** (`GatewayClass`, `Gateway`, `HTTPRoute`), the
  successor of `Ingress`, with **Traefik** as the controller.
- The Gateway API CRDs (v1.6.3, the version Traefik 3.7 supports) come from
  the `kubernetes-sigs/gateway-api` repository at a pinned tag, as their own
  Argo CD Application. The Traefik chart creates the `GatewayClass` and one
  `Gateway` ("web") that accepts routes from every namespace.
- Each component owns its route: the app's `HTTPRoute` is in its Helm chart;
  the platform UIs' routes are in `deploy/routes/`, each in its Service's
  namespace (cross-namespace backends would need a `ReferenceGrant`).
- On kind, Traefik's Service is a NodePort (30080) and the kind node publishes
  it on `127.0.0.1:8000` (`extraPortMappings`). `*.localhost` names resolve to
  this machine, so `http://status.localhost:8000` works with no DNS setup.

## Consequences

- Rollouts no longer break access: Traefik follows the Service's ready pods.
- One port for everything; adding a UI is one `HTTPRoute`.
- Port mappings are fixed when a kind cluster is created, so the cluster was
  rebuilt (a good test of rebuilding from git).
- Port 80 was taken on the development machine, hence 8000.
- Argo CD now serves plain HTTP behind Traefik (`server.insecure`); acceptable
  on 127.0.0.1 only. Anywhere shared, TLS belongs on the Gateway.
- The routes are not covered by CI (its kind cluster has no Traefik).

## Alternatives considered

- **ingress-nginx**: retired by the Kubernetes project (no releases or
  security fixes after March 2026).
- **Envoy Gateway**: very capable, but a separate control plane and data
  plane, and it wants a LoadBalancer; heavier for a laptop.
- **A LoadBalancer per Service** (cloud-provider-kind or MetalLB): one address
  per UI, no host routing, TLS in many places.
- **Keep port-forward with a restart loop**: hides the problem instead of
  routing through Services.
