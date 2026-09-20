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
