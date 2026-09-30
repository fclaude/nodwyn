# ADR 0012: Two binaries, always

- Status: accepted
- Date: 2026-09-20

## Context
From week 5 nodwyn builds nodwyn. A broken working tree must never strand
development.

## Decision
`bin/nodwyn` is the working-tree build. `~/.local/bin/nodwyn-stable` is the last
promoted build. All development sessions run `nodwyn-stable`. nodwyn never edits
itself through the binary being edited.

## Consequences
- Editing the interpreter's own dispatch path (P6) is safe.
- Rollback is `mv nodwyn-stable.prev nodwyn-stable`.
