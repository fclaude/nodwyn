# ADR 0006: Unbound egress defaults to deny

- Status: accepted
- Date: 2026-09-20

## Context
SPEC: bound → silent pass; unbound/unknown → deny or confirm, never fail open.
SPEC Risk 2: a bare confirm can be uninformed consent.

## Decision
`policy.unbound = "deny"` by default. `"confirm"` is opt-in. When a confirm
is shown it names the secret class, the destination host, the position and
the method, and offers to append the corresponding row to `bindings.toml` so
the next occurrence is a silent, logged pass instead of another prompt.

## Consequences
- The first weeks with a new credential produce a few prompts, each of which
  converts into a binding. That is the intended way the table gets written.
- A confirm for an *unknown* class remains a separate, explicit policy switch.
