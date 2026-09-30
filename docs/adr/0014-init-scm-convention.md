# ADR 0014: `.nodwyn/init.scm` convention

- Status: accepted
- Date: 2026-09-20

## Context
This repo needs dev tools (`go-test`, `git-diff`, …) that nodwyn's model can
call, and they must cross the guard like everything else.

## Decision
On start, after `prelude/`, nodwyn loads `./.nodwyn/init.scm` if present. This
repo's `init.scm` loads `dev/*.scm`, which register tools composed from host
primitives via `register-tool`. Nothing in `dev/` bypasses the guard.

## Consequences
- Loading happens before the first message (ADR 9).
- A project can ship its own tools the same way.
- A project can replace the agent loop the same way: the default
  `agent-turn` is installed before `init.scm` loads (ADR 26).
