# Architecture decision records

Each file here records one decision: what problem we had, what we chose, what
we gave up, and what we considered instead. They are short on purpose. The
code says *how* uptime-watch works; these say *why* it is this way, so the
reasoning is not lost when someone (including future us) wonders about it.

| # | Decision | Status |
|---|---|---|
| [0001](0001-schedule-checks-in-postgres.md) | Schedule checks in Postgres with `FOR UPDATE SKIP LOCKED` | Accepted |
| [0002](0002-one-binary-database-steps-as-jobs.md) | One binary, and database steps as Kubernetes Jobs | Accepted |
| [0003](0003-alerting-in-the-platform.md) | Alerting in the platform; target state exported from the database | Accepted |
| [0004](0004-gitops-with-argo-cd.md) | GitOps with Argo CD: app of apps, sync waves and pinned versions | Accepted |
| [0005](0005-targets-as-code.md) | Targets as code, with a source column | Accepted |
| [0006](0006-gateway-api-with-traefik.md) | Gateway API with Traefik for traffic into the cluster | Accepted |
| [0007](0007-add-form-with-token-field.md) | Add-target form: the admin token is a form field, not a login | Accepted |
| [0008](0008-external-secrets-from-openbao.md) | Secrets from outside: OpenBao next to the cluster, External Secrets inside | Accepted |
| [0009](0009-no-internet-inhibits-target-down.md) | Tell our network from theirs: NoInternet holds back TargetDown | Accepted |
| [0010](0010-app-inhibit-rules-as-alertmanagerconfig.md) | The app brings its own inhibit rules, as an AlertmanagerConfig | Accepted |
| [0011](0011-openbao-kubernetes-auth.md) | External Secrets logs in to OpenBao with Kubernetes auth, not a token | Accepted |

## How they are written

- One decision per file, numbered in the order they were made:
  `NNNN-short-title.md`.
- A decision that changes later is not edited away: a new record replaces it
  and the old one's status becomes "Superseded by NNNN".
- A record usually lands in the same pull request as the change it explains.

## Format

```markdown
# NNNN. Title in the form of the decision

- Status: Proposed | Accepted | Superseded by NNNN
- Date: YYYY-MM-DD

## Context
The problem and the forces at play.

## Decision
What we do.

## Consequences
What gets better, what gets worse, what we must now take care of.

## Alternatives considered
What else we looked at, and why not.
```
