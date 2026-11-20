# 0005. Targets as code, with a source column

- Status: Accepted
- Date: 2026-11-19

## Context

Targets were added with `curl` and lived only in the database: a rebuilt
cluster started empty, and nothing recorded who added what. Everything else
about the deployment was already in git. But quick, temporary targets (and,
later, the add form) must keep working.

## Decision

- The permanent targets are listed in the Helm values (`gitTargets.targets`,
  set in `deploy/argocd/apps/uptime-watch.yaml`). The chart renders them into a
  ConfigMap; a `sync-targets` Job after every install and upgrade makes the
  database match the list (see 0002 for why a Job).
- A column `source` (`api` or `git`) records where each target came from. The
  sync creates and updates targets and **deletes only `git` targets** that
  left the list. Targets added through the API are never touched, unless the
  list names one: then git takes it over (same id, so it keeps its history).
- The whole sync runs in one transaction with the rows locked
  (`FOR UPDATE`): a bad entry changes nothing.
- The file is checked strictly before the database is touched: unknown
  fields (`intervall`), duplicate names, bad URLs and a missing `targets` key
  (as opposed to an empty list) are errors.

## Consequences

- A rebuilt cluster gets its targets back by itself; changes are reviewed in
  pull requests and recorded in git history.
- API targets are temporary by design: they are lost with the database. A
  target that must always exist goes in git.
- Removing a git target deletes its check history too.
- A git target deleted through the API comes back at the next deploy.
- Enabling the list needs an image that has `sync-targets`: the image tag and
  the list were changed in one commit, after the code was merged and built.

## Alternatives considered

- **Delete every target not in the list**: each deploy would delete targets
  added by hand, and the form would be pointless.
- **Never delete**: git and the database would drift apart forever.
- **Sync at API start-up**: two API pods would sync at once, and a failure
  would hide in pod logs instead of showing as a failed Job.
- **A `Target` custom resource with a controller**: more Kubernetes-native;
  kept as an idea for the operator project.
