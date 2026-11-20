# Architecture decision records

Each file here records one decision: what problem we had, what we chose, what
we gave up, and what we considered instead. They are short on purpose. The
code says *how* uptime-watch works; these say *why* it is this way, so the
reasoning is not lost when someone (including future us) wonders about it.

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
