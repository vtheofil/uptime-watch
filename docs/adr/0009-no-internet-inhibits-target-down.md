# 0009. Tell our network from theirs: NoInternet holds back TargetDown

- Status: Accepted
- Date: 2026-11-24

## Context

uptime-watch runs on a laptop. Without Wi-Fi, every outside target fails at
once. Each one fires `TargetDown` after 2 minutes, and when the network
returns, Alertmanager sends everything it queued: a burst of firing and
resolved messages in Discord, all saying a site was down when the site was
fine. The cause was one thing, our network, but we were told about it as many
symptoms, with the wrong cause.

## Decision

- **Canary targets**: `github`, `cloudflare` and `google`, three big sites on
  different providers, checked every 15 seconds. One of them going down is an
  outage of that site; all three at once is our network.
- **A `NoInternet` alert** in the chart fires when every canary is down while
  `self` (checked inside the cluster) is up. With `self` down too, the problem
  is the cluster, not the internet, and the usual alerts are the right ones.
- **An inhibit rule** in Alertmanager mutes `TargetDown` while `NoInternet`
  fires. NoInternet itself goes to no receiver on the laptop.
- **Timing**: NoInternet fires after 1 minute, TargetDown after 2, so the
  inhibition is in place first. NoInternet keeps firing 5 minutes after the
  network returns (`keep_firing_for`), so targets with longer intervals
  recover while still muted.
- **Unit tests** for the rules (`make test-rules`, also in CI): promtool
  evaluates them on made-up series. A mutation check showed each test fails
  when the rule is broken on purpose.

## Consequences

- No Discord burst after the laptop was offline; Alertmanager still lists
  every muted alert as inhibited.
- A real outage that starts while NoInternet fires is held back until it ends.
- Three more targets, checked every 15 seconds: a little more traffic.
- Overriding `inhibit_rules` replaces the stack's defaults, so they are
  repeated in our values and must be compared after a chart upgrade.
  [0010](0010-app-inhibit-rules-as-alertmanagerconfig.md) removes this: the
  inhibit rule moved to an `AlertmanagerConfig` in the chart.

## Alternatives considered

- **Silences** by hand when going offline: easy to forget, and they hide
  everything for a fixed time.
- **Turning alerts off when the laptop sleeps**: hides real problems and
  needs a script outside the cluster.
- **Any N outside targets down**: depends on which sites happen to be
  monitored; a user monitoring two sites on one provider would see NoInternet
  for a provider outage.
- **Checks from two locations** (README, ideas for later): the real answer
  for a service that must be up, since one place cannot tell "the site is
  down" from "I am cut off". On an always-on server, NoInternet would also
  go to a receiver: monitoring that is blind should say so.
