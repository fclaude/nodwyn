# ADR 0007: Scheme scope: written from scratch, documented first

- Status: accepted
- Date: 2026-09-20

## Context
No interpreter exists. The kernel is the surface everything else sits on, and
the model will read its documentation when writing `.scm` for nodwyn in P1b.

## Decision
`docs/scheme.md` is written before the code and is the contract: syntax, data
types, special forms, builtins, the error model, the primitive-table contract,
and an explicit list of what is *not* in nodwyn Scheme. A builtin without a doc
entry and a conformance snippet fails the suite.

Bootstrap kernel: `define lambda if let let* begin set! quote quasiquote cond
and or`; int64 and float64; strings; lists; association lists; `error`;
proper tail calls. Phase 1b adds `letrec case do`, vectors, string ports,
`guard`, and `define-macro` only if the prelude needs it.

Never in v0: hygienic macros, `call/cc`, `dynamic-wind`.

## Consequences
- `eval` and `apply` are two fixed Go functions; loading `.scm` cannot add
  special forms. A kernel-fingerprint test enforces this.
- Scope growth is a documented decision, not drift.
