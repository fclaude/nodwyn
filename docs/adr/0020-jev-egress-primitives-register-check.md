# ADR 0020: Jev as egress primitives; register-check

- Status: accepted; implemented in P5 (`v0.6-plan`, 2026-09-22)
- Date: 2026-09-21

## Context

ADR 19 established that Jev is an egress edge: its payload passes the same
redaction controller as the model context, and every call is logged. The P5
plan, however, describes `internal/jev` as a Go client package that the
loop calls directly. This creates a second path for Jev calls that bypasses
the guard, the binding check, and the guard-decision log — even though
ADR 19 says the call should be treated as egress.

SPEC invariant 4: *the Scheme interpreter is the only Scheme→Go surface;
the guard sits on it.* A Go-only Jev client called by the loop is a second
surface for a data-plane egress.

The spec's LLM-as-tool split resolves the one apparent exception: the
harness's own LLM key is **control-plane** — it is the harness's identity,
standing, not grant/revoke, and not audited by Jev. The loop's `Chat()`
call is not a guarded egress; it is the harness talking to itself.
Everything else, including Jev, is data plane.

## Decision

### 1. Jev is a set of Scheme egress primitives

The three Jev tools are registered as host primitives (Go handlers,
`Meta.Egress: true`), not as a separate Go client package called by the
loop:

| Primitive | Args (positional) | Purpose |
|---|---|---|
| `jev-noul` | `(handle question)` | Yes/no gate: does this message need a plan? |
| `jev-score` | `(handle payload ask)` | Score a plan against the user's ask (Seam A) |
| `jev-choice` | `(handle situation)` | Score an ambiguous egress situation (Seam C: class + dest + scope) |

Each takes a credential handle as its first argument
(`Param.Role: "credential"`). The handle is the Jev API key, stored in
the keychain (P3) and bound to `api.typesafe.ai` (P3 binding table).
The remaining arguments are text payloads.

The loop's Seam A call is `(jev-score handle plan-text ask-text)`,
evaluated through `interp.Apply` — the same path a model tool call takes:
guard intercept → binding check → redaction → handler makes the HTTP call
→ result returned as a Scheme value.

The loop does not call Jev through a Go-specific code path.

### 2. The guard sees Jev calls like any other egress

Because the Jev primitives cross the guard, none of the existing machinery
is special-cased:

- `guard.decision` is logged for every Jev call: prim name, origin,
  verdict, checker, reason, args summary.
- The binding check (E3) verifies the credential is bound to
  `api.typesafe.ai` before the call is allowed. Unbound ⇒ Deny (default)
  or Confirm (policy).
- The payload is redacted before the handler sends it: L1 at P5,
  L1+L3 at P7 (ADR 19: no bespoke sanitiser).
- `jev.req` is logged with the Jev-specific fields: content level
  (`jev.content`), byte count, payload hash, redaction events, and the
  score that came back.

### 3. User tools can call Jev under the same policy

A user can register a tool that wraps a Jev call:

```scheme
(register-tool "review-diff"
  (lambda (handle)
    (jev-score handle
               (git-diff ".")
               "is this change complete and safe for the task?"))
  '((doc . "Ask Jev to score the current diff against the task.")
    (params . (("handle" "credential" "Jev credential handle")))))
```

When the model calls `review-diff`, the guard sees the outer tool call
(origin: model, prim: `review-diff`, a Scheme-registered tool). The handler
applies
the Scheme lambda, which calls `jev-score` — crossing the guard a second
time as an egress prim with a credential. The binding check fires on the
inner call; the payload is redacted; the decision is logged.

The model can only call `review-diff` if the user has registered it.
The user is the principal who decides which Jev-backed tools exist.
The guard's policy for the inner `jev-score` call is identical whether
the call came from the loop's Seam A or from a user-registered wrapper.

### 4. register-check: tightening-only Scheme checkers

The guard's checker pipeline is currently Go-only. A user who wants to add
a custom guard rule must modify Go code. `register-check` closes this gap
without weakening the guard:

```scheme
(register-check NAME VERDICT PREDICATE)
```

- `NAME` — a string, logged as the checker name in `guard.decision`.
- `VERDICT` — `"deny"` or `"confirm"` (not `"allow"`).
- `PREDICATE` — a Scheme function `(prim-name args) → boolean`. When it
  returns `#t`, the checker returns the specified verdict; otherwise it
  returns Allow (no opinion).

The checker is added to the guard's pipeline after all Go checkers.
The strictest-wins rule (deny > confirm > allow) already guarantees that a
`register-check` checker can only **tighten**: its Deny or Confirm stands
regardless of what another checker says, and its Allow (no-op) never
overrides a Deny or Confirm from any other checker.

`register-check` is a hidden builtin (`Meta.Hidden: true`): it is not
offered to the model as a tool, and the model cannot call it. Only
user-originated Scheme code (`.scm` files, `init.scm`, the REPL) can
register checkers.

### 5. The control-plane model is the one Go-only call

The loop's `provider.Chat()` is the harness's control-plane call to the
model. It is:

- **Not a guarded egress.** The control-plane key is the harness's identity
  (spec LLM-as-tool split), not a data-plane credential. It is not in the
  binding table, not subject to the binding check, and not audited by Jev.
- **Not redacted at the call site.** Redaction applies to what *enters*
  the context (E2/E4), not to the control-plane request itself.
- **Logged as** `llm.req` / `llm.resp`, not as `guard.decision` /
  `jev.req`.

This is the only Go-only path in the loop. Every other call — including
Jev — crosses the guard as a Scheme application.

## Consequences

- **P5 drops `internal/jev`** as a standalone client package. The Jev
  handler is a Go function registered as a Scheme host primitive (alongside
  `shell-exec`, `fs-read`, etc. in `internal/prim/`). The loop's Seam A is
  a Scheme application, not a Go function call.
- The Seam A gate and the Seam C score are both guarded, logged, and
  subject to the same binding and redaction as any other egress.
- User tools that call Jev are indistinguishable from model tool calls at
  the guard level; the `origin` field in the log distinguishes them, but
  the policy is identical.
- `register-check` extends the guard pipeline without modifying Go code.
  The strictest-wins rule ensures a user checker can never relax the guard.
- The P5 "fake Jev" test in `test/fakes` tests the Scheme primitives
  (their guard decisions and redaction), not a Go package API.
- The exfil suite gains a scenario: a Jev call with an unbound credential
  ⇒ Deny; a Jev call with a bound credential whose payload contains a
  canary ⇒ the canary is redacted in the sent payload and in `jev.req`.
- The repo layout in plan.md drops `internal/jev/`; the Jev handler lives
  in `internal/prim/` alongside the other host primitives.


## Fixed request scope implemented — 2026-09-22

All three Jev primitives now declare `EgressMethod: "POST"` and
`EgressPosition: "header:Authorization:Bearer"` alongside their configured host.
The guard uses these Go-owned facts when there is no corresponding role
parameter. A declared argument always takes precedence, including malformed or
missing arguments; fixed metadata never hides an invalid dynamic argument.

This closes an authorization gap: a Jev row matching only class and host could
previously authorize a request even if it listed GET or a query injection.
Jev now requires the actual POST and Bearer-header scope. Request bytes and
credential ownership are unchanged. Confirmations carry the same complete facts
used by authorization. Tests exercise all three primitives with matching and
mismatching method/position rows and assert no transport call on denial.

## Amendment — 2026-09-30: the model's judge tools

The three primitives were offered to the model as tools, which it could
never call: a handle cannot come from a model's JSON, so every call failed
in the handler after the guard had allowed it. Now:

- `jev-noul`, `jev-score` and `jev-choice` are hidden. They keep the handle
  argument; the plan gate and user code hold one.
- The model has `judge-noul`, `judge-score` and `judge-choice`: the same
  calls without the handle. Go looks up the stored `jev-key` at call time,
  and the primitive's `EgressClass` is `jev-key`, so E3 judges the call as
  carrying that class at the Jev host, POST, `header:Authorization:Bearer`,
  exactly the check a `jev-*` call gets (an MCP server's key works the same
  way, ADR 22 §4). With no key stored the call fails before anything is
  sent.
- They are registered only when the session starts with a `jev-key`
  stored, since the tool list is fixed for the session, and with
  `jev.content = "full"`. ADR 19's levels say what may reach Jev; the model
  chooses what these tools send, which may be raw file contents or tool
  output, and nothing can hold that to a plan. At the default `plan` level
  only the plan gate talks to Jev.
- Every argument of every judge primitive is a payload: `levels` and
  `options` had no role, so L1 and L2 never read them, and the handler did
  not redact them. Now the guard reads them and the handler redacts every
  string it sends.
- `jev.req` records the call's origin (`harness` for the plan gate, `model`
  for a judge tool, `user` for `:eval`).

Section 3's example is out of date in one more way: a tool body runs with
the model's origin, so it cannot call `credential`; a user tool closes over
a handle looked up in `init.scm` instead.

## Amendment — 2026-10-01: Seam C answers confirms

Seam C ("score an ambiguous egress situation") is used for judged approval
(ADR 18): the core asks `jev-choice` whether a call should run with nobody
watching, and allows it only above `jev.approve_threshold`. Unlike the plan
gate it is Go, because an approval must not be something a repository's
Scheme can grant: Scheme can only add refusals (`approve-refuse?`), as
`register-check` can only tighten the guard.
