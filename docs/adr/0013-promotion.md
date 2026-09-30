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
`nodwyn-stable.prev`, candidate → `nodwyn-stable`, git tag `v0.N-<phase>`.

## Consequences
- Promotion is the only path to `nodwyn-stable`; copying a binary by hand is a
  process violation worth a friction.md line.
