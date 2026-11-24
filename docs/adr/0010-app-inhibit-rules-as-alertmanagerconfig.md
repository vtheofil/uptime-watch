# 0010. The app brings its own inhibit rules, as an AlertmanagerConfig

- Status: Accepted
- Date: 2026-11-24

## Context

The NoInternet inhibit rule ([0009](0009-no-internet-inhibits-target-down.md))
first lived in `inhibit_rules` of the monitoring stack's values. Helm merges
maps in values but replaces lists, so that list replaced the stack's four
default inhibit rules: we had to copy them, and compare them after every
upgrade of the chart. It also mixed two owners in one file: the platform
(receivers, routes, defaults) and one application's alert logic.

## Decision

- The uptime-watch chart renders an **`AlertmanagerConfig`** next to its
  `PrometheusRule`, holding the inhibit rule: NoInternet mutes TargetDown.
- The **Prometheus Operator** adds it to Alertmanager's configuration, after
  the platform's own rules, and adds `namespace="uptime-watch"` to both sides,
  so an app cannot mute alerts outside its namespace.
- `inhibit_rules` is no longer set in `deploy/monitoring/values.yaml`: the
  stack's defaults come back untouched.
- Where alerts go stays with the platform: the route that sends NoInternet to
  no receiver remains in the monitoring values, next to the receivers.

## Consequences

- An app's alert logic lives in one place, its chart: the alert, its tests and
  what it mutes. The platform config only has receivers, routing and defaults.
- Nothing to re-check after a monitoring chart upgrade.
- `AlertmanagerConfig` is `v1alpha1`. It is widely used, but its fields may
  change in a later operator version, and the chart would follow.
- The Alertmanager must select AlertmanagerConfigs (kube-prometheus-stack's
  default selects all of them, in every namespace).

## Alternatives considered

- **Keep the copied list**, with a CI check that fails when the stack's
  defaults change: it guards the copy but keeps it, and keeps two owners in
  one file.
- **Routes in the AlertmanagerConfig too**: the operator places them as
  first-level routes; we kept routing in one place, the platform's.
