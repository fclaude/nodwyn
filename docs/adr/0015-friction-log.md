# ADR 0015: Friction log

- Status: accepted
- Date: 2026-09-20

## Context
The TUI (P9) and memory (P10) should be built from observed need, not guessed
need.

## Decision
When `nodwyn-stable` gets in the way, hand-edit *and* add one line to
`docs/friction.md`. The file is reviewed at every promotion; its top items
are the backlog for the front-ends and memory work.

## Consequences
- Real data replaces speculation about UI and context-management features.
