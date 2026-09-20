# ADR 0003: Model tool calls are Scheme applications

- Status: accepted
- Date: 2026-09-20

## Context
SPEC invariant 4: the Scheme interpreter is the only Scheme→Go surface and the
guard sits on it. If the loop dispatched model tool calls straight to Go
handlers there would be a second surface.

## Decision
The loop turns a model tool call `{name, input}` into the Scheme application
`(name arg …)` — argument order comes from the primitive's declared parameter
list — and evaluates it through `apply`. The guard call site inside `apply`
is therefore the one Scheme→Go exit, crossed identically by the model's
calls, the prelude, and the user's `.scm` files.

## Consequences
- A CI lint asserts that `Prim.Handler` is invoked from exactly one place.
- Tool argument JSON maps to Scheme values through the same `json-parse`
  rules the language documents.
- A Scheme-registered tool (`register-tool`) is indistinguishable from a
  builtin at the dispatch site.
