# ADR 0014: `.nibb/init.scm` convention

- Status: accepted
- Date: 2026-09-20

## Context
This repo needs dev tools (`go-test`, `git-diff`, …) that nibb's model can
call, and they must cross the guard like everything else.

## Decision
On start, after `prelude/`, nibb loads `./.nibb/init.scm` if present. This
repo's `init.scm` loads `dev/*.scm`, which register tools composed from host
primitives via `register-tool`. Nothing in `dev/` bypasses the guard.

## Consequences
- Loading happens before the first message (ADR 9).
- A project can ship its own tools the same way.
