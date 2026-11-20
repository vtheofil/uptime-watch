# 0007. Add-target form: the admin token is a form field, not a login

- Status: Accepted
- Date: 2026-11-20

## Context

Adding a target needed `curl` with a bearer token. A form on the status page
is friendlier, but writing must stay protected by `ADMIN_TOKEN`, and the
status page reloads itself every 30 seconds.

## Decision

- The form has **its own page**, `GET /targets/new`, linked from the status
  page. The status page keeps its 30-second reload, which would clear a
  half-filled form.
- The form **posts to `POST /targets`** (HTML form encoding, 64 KB limit) and
  **asks for the admin token in a password field**, checked with the same
  constant-time comparison as the API. There is no login, session or cookie.
- Validation is shared with the API (`store.NewTarget.Validate`, and the
  database's CHECK constraints). On an error the page is shown again with the
  message and what was typed, except the token.
- On success the server answers **303 See Other** to `/?added=<name>`
  (post/redirect/get): reloading the status page does not post again. The
  status page confirms only names of targets that exist, so a crafted link
  cannot make it display arbitrary text.
- Without `ADMIN_TOKEN` the form says it is off and the link is hidden.

## Consequences

- Nothing is stored in the browser, so there is no session to steal, and
  cross-site request forgery does not apply: another site cannot submit the
  form for you without knowing the token. The browser's password manager can
  remember it.
- The token is typed for every addition: fine for an admin tool, tedious for
  many users. Users and roles would need real authentication (OIDC).
- Over plain HTTP the token crosses the network unencrypted, exactly like the
  API's bearer token; on kind that is 127.0.0.1 only.
- Targets added with the form are API targets: temporary (0005).
- No JavaScript was needed.

## Alternatives considered

- **Login page with a session cookie**: needs session storage or signing, and
  CSRF tokens on every form.
- **HTTP basic auth on the form**: the browser caches the credentials with no
  way to log out, and the prompt is not part of the page.
- **OIDC login (e.g. via an auth proxy in front of the Gateway)**: the right
  answer for many users; far more than one admin needs today.
- **Form inside the status page**: cleared by the 30-second reload, or the
  reload would need JavaScript.
