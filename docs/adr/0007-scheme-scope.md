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
  special forms. `TestKernelFixed` checks that loading Scheme grows the
  primitive table and no new form appears (amended 2026-10-16).
- Scope growth is a documented decision, not drift.

## Amendment 2026-10-16: no kernel fingerprint

The kernel fingerprint (a hash of a version string and the special-form
names) was said to check that loading code cannot change evaluation. It could
not: no Scheme code reaches either input, so the test passed by construction,
and a change to how `define` or `guard` evaluates left it unchanged. It is
removed. What `TestKernelFixed` really asserts stays (the primitive table grew
by exactly the registered tools; `define-syntax` stays a plain binding), and
evaluation semantics are held by the conformance suite.

## Amendment 2026-10-16: nesting is bounded

The evaluator had no depth limit. A call that is not in tail position recurses
in Go, so a runaway recursion (a model's mistake, or an `init.scm` wrapper
that captured itself on `(reload)`) grew the Go stack to its gigabyte limit
and ended the process: the daemon and every session in it. So did a vector
that holds itself, through the printer and `equal?`, a file that loads itself,
and a datum nested deeply enough in the reader.

Evaluation now counts the `Eval`s in progress and raises an ordinary,
catchable error past `scheme.MaxDepth` (10,000); tail calls loop inside one
`Eval` and do not count. A nested `Eval` costs one to five kilobytes of Go
stack, measured on 2026-10-16 with and without `-race`, so the limit stays
near 50 MB. The reader, the printer, `equal?` and `json-emit` take the same
bound, and `load` refuses a file it is already loading. The kernel's
semantics are otherwise unchanged (docs/scheme.md §3).

## Amendment 2026-10-22: the interpreter is Brote

The interpreter was extracted from `internal/scheme` into its own module,
`github.com/fclaude/brote` (package `scheme`), so that nodwyn and the Coigüe
editor embed one implementation of this language. What this amendment decides:

- **Brote owns the language.** Values, reader, `eval`/`apply`, builtins, the
  primitive table, the prelude, the conformance suite (`TestConformance`,
  `TestDocCoverage`, `TestKernelFixed`, `TestTailCalls`, `TestGateIsTheSingleExit`
  and the rest of `scheme_test.go`) and the language reference now live in
  Brote. Nodwyn keeps the host primitives, the guard, credentials, the loop and
  everything that registers a primitive or installs a gate.
- **The extraction changed nothing.** Brote's `scheme/` is `internal/scheme` at
  commit `3846cd7f`, file for file, but for the package comment and the docs
  path in one test. Error strings and doc strings that nodwyn's tests and
  prompts assert on are unchanged, and the API still carries what nodwyn
  needed (egress declarations, provenance tags, tool schemas, origin, the gate).
  Narrowing that API waits until a second host shows which parts it uses.
- **How nodwyn gets it.** `go.mod` requires Brote at its tag, v0.1.0 (a later change to the language is pinned by commit until it is tagged).
  Brote is public on GitHub, so a stock Go environment fetches it with no
  `GOPRIVATE` and no git configuration (a clean `git archive` of this tree
  builds that way). Brote's MIT licence is bundled into
  `nodwyn licenses` like any dependency's.
- **`nodwyn repl` and `nodwyn eval`** run Brote's `repl` package and
  `scheme.LoadFile`; the standalone interpreter for everyone else is `brote`.
- **`docs/scheme.md` keeps the language sections** so that it reads whole, and
  says that Brote's reference wins where they differ. Two copies drift; once
  Brote's repository is public this file links to it for those sections instead.


## Amendment 2026-11-08: Brote v0.9.1, in step with Raulí

nodwyn links Raulí (ADR 62), whose recipes are Brote, so the two must agree
on one Brote: a module that requires both gets the newer anyway. Both now
require Brote v0.9.1, the latest release, and nodwyn requires Raulí v0.2.0,
which requires the same. The language reference (`docs/scheme.md` §§1-6,
§10) follows Brote's for v0.9.1. This reverses two lines of the Decision
above, on purpose:

- **Macros are in.** `define-syntax`, `let-syntax` and `letrec-syntax` with
  `syntax-rules`, not fully hygienic (a name a template only refers to means
  what it means where the macro is used), and the prelude's
  `define-record-type`, `let-values` and `case-lambda` are macros. "Never in
  v0: hygienic macros" was about keeping evaluation fixed. What still holds:
  a macro only rewrites code, and what it expands to crosses the guard
  primitive by primitive, as `TestGateIsTheSingleExit` says; a macro cannot
  take a special form's name (`(define-syntax if …)` is an error, tried); and
  the model's `scheme` tool runs in an environment of its own, so a macro it
  defines ends with its program. Only user-origin code (init.scm, `:eval`)
  can define one for the session.
- **Pairs can change.** `set-car!` and `set-cdr!`, in a program running
  alone, never making a list hold itself; what is stored keeps its
  provenance.

Also new, from Brote 0.3.0 to 0.9.1: hash tables; characters; string ports,
`read`, `read-line` and the rest, with `current-input-port` empty in every
session (it reads EOF even with input piped to `nodwyn eval`, tried), so no
program reads the daemon's or the terminal's input; nested quasiquote;
positions and a trace in errors; `gensym`, `doc`, `apropos`; prelude
procedures for lists, strings and vectors, and Scheme's test procedures. In
all, 67 more builtins (110 to 177 in `nodwyn eval`). Provenance survives
the new paths: a tagged string written to a string port, read back a
character, a line or a datum at a time, split into characters and joined,
mapped through `char->integer` and `integer->char`, copied, or stored with
`set-car!`, keeps its tag (tried, one by one); only predicates and
measurements (`char?`, `digit-value`, the comparisons) drop it, as before.

Brote's tasks, channels and promises, and its `std` library, are not
granted: nodwyn gives no `scheme.WithTasks` and imports no `std`, so those
names are unbound and the prelude's `delay` is an error.

Moving needed one fix in Brote and one line here. Brote 0.9.0 deadlocked a
gate that evaluates Scheme inside its own check, which nodwyn's judge does:
every judge test stopped at its 60-second wait and three packages hit the
ten-minute test timeout. Brote 0.9.1 gives a check a turn of its own
(Brote `fac397f`). Brote's vectors hide their items behind `Elements()`,
which `prim/schemetool.go` now calls. With both, the whole suite passes;
the only other failures were the two tests that every primitive is
documented.
