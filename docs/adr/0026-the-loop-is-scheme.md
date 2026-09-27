# ADR 0026: The agent loop is a Scheme procedure

- Status: accepted
- Date: 2026-09-26

## Context
The SPEC divides the work: Scheme owns orchestration, Go owns I/O. The agent
loop had drifted to Go: `loop.Turn` both decided how a turn goes and kept its
invariants. Deciding how a turn goes is what we want to experiment with:
plan first when the judge says a message is multi-step, have the judge
check each step, stop a batch at its first failure. The right choice depends
on the model and the judge, so it belongs where users can change it, not in
a release.

## Decision
A turn is the Scheme procedure `agent-turn`, applied to the recorded
(redacted) user message. The default is `internal/loop/strategy.scm`,
installed before `.nibb/init.scm` loads so a project can redefine it
(ADR 14). It is built from three primitives (docs/scheme.md §9):
`model-turn`, `run-tools` and `skip-tools`.

What a strategy must not be able to skip stays in Go, inside those
primitives:

- windows are built from the log and every request is recorded (ADR 9);
- only calls from the latest model response can be run or skipped, each
  once, with the arguments the model sent, and each run goes through the
  guard with model origin (ADR 3);
- every call gets exactly one result: calls left unanswered are skipped
  when the model is asked again or the turn ends;
- budgets, the tool-call limit and the progress guard (ADR 18) end the turn
  with `scheme.Halt`, which `guard` cannot catch, like a denial or a
  cancellation; so does a failure to write the log, since a strategy that
  caught it would carry on unrecorded;
- tool results keep their provenance (L2).

The primitives are hidden from the model and refused outside a turn and
from model-originated code. The strategy runs with harness origin. Because a
strategy handles every tool result, `wipe` and `box` are now refused to
harness origin as well as model origin: provenance is stripped only by the
user.

## Consequences
- The default strategy reproduces the previous Go loop; the existing session
  tests pass unchanged.
- A strategy chooses which calls run and when to ask again, but cannot yet
  add text to the conversation. The plan gate and the system prompt are still
  Go; moving them into Scheme is the next step, keeping the system prompt
  fixed for a session (ADR 9). *Update:* every prompt, the system
  prompt included, is now Scheme (internal/loop/prompts.scm, read once and
  fixed by Freeze); the plan gate's control flow is the part still in Go.
  *Update 2026-09-27:* the plan gate is Scheme too (internal/loop/plan.scm,
  docs/scheme.md §9.5). Go keeps one invariant for it: a turn that must plan
  runs nothing irreversible until a plan is recorded, and fails if it
  records none.
- Go is left with I/O and invariants. New turn behaviour should be a
  strategy first and a primitive only when it needs an invariant.

## Addendum — the userland standard (2026-09-26)

A pressure test asked whether a user could write the behaviours we want
with what the core offers, instead of asking what features to add. A plan
cycle ran as a plain `init.scm`: tools are already the channel between a
strategy and the model (a tool's description instructs, its result and a
skip reason give feedback). It stopped at two walls, both at the model
boundary, which two primitives now remove:

- `(credential class)`: the handle of a stored key. Scheme could call
  `jev-*` and `http-request` but had no way to get the handle they need, so
  no keyed oracle — a judge, a stronger model, a review bot — was reachable
  from user code.
- `(add-message text)`: the strategy speaking to the model when there is no
  call to answer, logged as `harness.msg` (window recipe v3).

The standard is kept as tests: `internal/core/userland_test.go` holds each
behaviour written as a user would write it. Two walls remained for routing
work to another model: `model-turn` could not choose a model, and a
strategy could not choose what a model sees. ADR 27 removes both.
