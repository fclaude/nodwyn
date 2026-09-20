# ADR 0001: Value representation

- Status: accepted
- Date: 2026-09-20

## Context
Every Scheme value must be able to carry a taint tag (SPEC L2) and a credential
handle must be a value that can never be coerced into its secret.

## Decision
`scheme.Value` is one tagged struct: `Kind`, a payload, and a `Taint` field.
Not an interface hierarchy. `Handle` is a first-class `Kind` from the start;
it prints as `#<cred:class:id>` and no builtin converts it to a string. The
`Taint` field is reserved in bootstrap and left unset until Phase 6.

## Consequences
- Taint propagation (P6) is a field copy in one dispatch path, not a refactor.
- Values are copied by value; the struct is wider than a pointer. Irrelevant
  next to LLM latency.
- Adding a kind is a deliberate act with a doc entry (see ADR 7).
