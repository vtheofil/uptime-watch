# 0011. External Secrets logs in to OpenBao with Kubernetes auth, not a token

- Status: Accepted
- Date: 2026-11-24

## Context

External Secrets read OpenBao with a token stored in the cluster as the
Secret `external-secrets/openbao-token` ([0008](0008-external-secrets-from-openbao.md)).
That token was the one secret put into a new cluster by hand ("secret zero"),
it expired after 32 days because nothing renewed it, and anyone who could read
that Secret could read our secrets in OpenBao from anywhere, for a month.

## Decision

- OpenBao's **Kubernetes auth** at `auth/kubernetes`, pointed at the kind API
  server (`https://<cluster>-control-plane:6443` on kind's Docker network) and
  its CA. `deploy/openbao/k8s-auth.sh` sets it up; `bootstrap.sh` runs it,
  because every new cluster has a new CA.
- A **ServiceAccount `external-secrets/openbao-auth`** with no permissions on
  cluster resources, only `system:auth-delegator` (token reviews), in git
  (`deploy/secrets/openbao-auth.yaml`).
- The **ClusterSecretStore** logs in as that ServiceAccount: at each login ESO
  asks Kubernetes for a short-lived token of it, OpenBao checks the token with
  the API server (TokenReview) and returns a 1-hour token with the policy
  `uptime-watch-read`.
- The OpenBao **role** `external-secrets` accepts only that ServiceAccount, in
  that namespace, with the audience `openbao`.
- OpenBao makes the TokenReview **with the login token itself**, not with a
  long-lived reviewer token of its own. That token therefore also carries the
  API server's audience.

## Consequences

- No secret in the cluster, nothing to renew, nothing that expires. A stolen
  login token is useless within minutes; an OpenBao token within an hour.
- The identity is the ServiceAccount: whoever can create tokens for
  `openbao-auth` (ESO, and cluster admins) can read the uptime-watch secrets.
  RBAC on the cluster now guards them.
- OpenBao must reach the API server. That is easy here (same Docker network);
  for a cluster elsewhere it needs a route and a trusted certificate, or the
  JWT/OIDC auth method, which checks tokens against the cluster's public keys
  without calling it.
- The login token is valid at the API server too (two audiences), with only
  the token review permission.

## Alternatives considered

- **Keep the token** and renew it from a CronJob: still a secret in the
  cluster, and a job that must not fail.
- **A reviewer token for OpenBao** (a long-lived ServiceAccount token stored
  in OpenBao's config): login tokens could carry only the `openbao` audience,
  but a long-lived cluster credential would live in OpenBao instead.
- **JWT/OIDC auth** with the cluster's service account issuer: OpenBao checks
  signatures offline, no TokenReview. The usual choice for clusters OpenBao
  cannot reach; here the API server is right next to it.
