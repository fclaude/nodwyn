# ADR 0012: Two binaries, always

- Status: accepted
- Date: 2026-09-20

## Context
From week 5 nibb builds nibb. A broken working tree must never strand
development.

## Decision
`bin/nibb` is the working-tree build. `~/.local/bin/nibb-stable` is the last
promoted build. All development sessions run `nibb-stable`. nibb never edits
itself through the binary being edited.

## Consequences
- Editing the interpreter's own dispatch path (P6) is safe.
- Rollback is `mv nibb-stable.prev nibb-stable`.
