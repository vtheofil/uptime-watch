# 0012. Argo CD Image Updater commits new image tags to git

- Status: Accepted
- Date: 2026-11-24

## Context

Deploying new code took two pull requests: the change itself, then, once CI
had pushed its image, a second one that only copied the new commit SHA into
`image.tag`. The second one decides nothing, waits for another CI run, and a
wrong SHA means `ImagePullBackOff`. At the same time CI pushed an image for
every push to main, docs included, so "a new image" did not mean "new code".

## Decision

- **Argo CD Image Updater** (chart 1.5.0, Application `image-updater`) watches
  `ghcr.io/vtheofil/uptime-watch`: tags that are commit SHAs, strategy
  `newest-build`, configured with an `ImageUpdater` resource
  (`deploy/image-updater/`).
- **Git write-back**: it commits the tag to main, in
  `deploy/helm/uptime-watch/.argocd-source-uptime-watch.yaml`, which Argo CD
  reads as Helm parameters of the Application. The tag is no longer in the
  Application itself, so one file says which version runs.
- It pushes over SSH with a **GitHub deploy key** that may write to this
  repository only. The key is made by `make openbao-deploy-key`, kept in
  OpenBao and copied into the cluster by External Secrets.
- **CI pushes an image only when the app changed** (`cmd/`, `internal/`,
  `go.mod`, `go.sum`, `Dockerfile`), and runs on main are no longer cancelled
  by newer ones, so no change is skipped.
- The updater's commits say `[skip ci]`: they change one tag, of an image CI
  already tested. Without that, and without the filter above, each of its
  commits would build an image that it would deploy, and so on.

## Consequences

- One pull request per change: merge, and a few minutes later it runs.
- Git is still the record: every deploy is a commit by `argocd-image-updater`,
  and Argo CD still deploys only what git says.
- A key with write access to the repository exists in the cluster. It is
  limited to this repository, but it could push anything to main: there is
  no branch protection yet.
- A rollback is no longer "revert the tag commit", which the updater would
  undo: revert the code, or pin a tag in `allowTags` (runbook).
- Main moves by itself: pull before working on it.
- `newest-build` trusts the build date; it would pick an old commit rebuilt
  later. CI only builds the commit that was pushed, so that does not happen.

## Alternatives considered

- **CI opens the tag pull request** after pushing the image: no new
  component, and every deploy is approved, but a click and a CI run per
  deploy remain. The simpler choice for one repository; the updater was
  chosen to learn the tool most Argo CD setups use, which also follows images
  built elsewhere.
- **CI commits the tag to main directly**: the same result as the updater
  without a component in the cluster, but CI would need write access to main.
- **A pull request from the updater** (its `pullRequest` mode): needs a
  GitHub token instead of a deploy key; worth it once main is protected.
- **Write-back to the Argo CD Application only** (method `argocd`): nothing in
  git, so git would no longer say what runs.
