# ADR 0013: Promotion

- Status: accepted
- Date: 2026-09-20

## Context
A candidate must prove itself before it becomes the tool used to build the
next one.

## Decision
`scripts/promote.sh` runs `make accept` (lint, unit tests, exfil suite,
structural lints) and `scripts/selfcheck.sh` (the candidate completes a
scripted session: read a file, register a dev tool, run
`go test ./internal/log`, replay its own log). Only then: `nodwyn-stable` →
`nodwyn-stable.prev`, candidate → `nodwyn-stable`. The candidate's version is
the commit it was built from, so the tree must be clean (amended 2026-09-30;
it was a git tag `v0.N-<phase>`).

## Consequences
- Promotion is the only path to `nodwyn-stable`; copying a binary by hand is a
  process violation worth a friction.md line.

## Amendment — 2026-09-30
Promotion no longer tags. `make promote` takes no argument; the stable
binary's version (`nodwyn-stable version`) is the commit's 12-character id,
and `git log` says what that commit holds. A phase name added nothing the
commit does not, and a tag to invent stood between a green tree and a
promotion. The tags `v0.1-bootstrap` to `v0.42-console` stay as history.
