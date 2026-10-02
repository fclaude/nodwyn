# ADR 0004: Primitive metadata

- Status: accepted
- Date: 2026-09-20

## Context
The guard must reason about what an argument *is* (a destination, a payload,
a credential slot) without parsing prim names or special-casing.

## Decision
Every primitive declares: `Doc`; `Egress` (may send bytes off-machine);
`Irreversible` (cannot be undone by the harness); a parameter list with name,
type and doc, from which the LLM tool schema is generated; and, for egress
prims, an `ArgSpec` naming which positions carry destination, payload and
credential slots. The guard reads metadata only.

## Consequences
- Adding a prim without metadata is a compile error, not a policy gap.
- The tool schema the model sees and the guard's view of the prim come from
  the same declaration and cannot drift.

## Amendment — 2026-10-01: the `source` role

Parameters carry a role the guard reads: `path`, `dest`, `payload`,
`credential`, `method`, `position`, `argv`, and now `source`, a file the
primitive only reads whatever else it does (the document `doc-export`
converts, the file `google-doc-upload` sends). A source is pinned and
checked against the credential-file denylist like a path, but a read-only
mount may supply it to an irreversible primitive, which judges only its
`path` as written. A write is an irreversible primitive with a `path` and
something to put there, a `payload` or a `source`: standing write
approvals, protected paths and the scratch directory treat `doc-export`'s
target as they treat `fs-write`'s, and an upload, which has a source and no
path, as no write at all.
