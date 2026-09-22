# ADR 0018: Turn budgets and long-running turns

- Status: **partially implemented** (progress guard implemented; budgets, checkpoints and run driver proposed)
- Date: 2026-09-21

## Context
`loop.max_tool_calls` (50) is a single hard stop. It is the right kind of
guard — a turn that has made fifty tool calls is either doing a lot of real
work or is stuck — but it cannot tell the two apart, and the point of nibb
is to run long, supervised tasks. A count is also the wrong unit: a turn
that reads fifty files is cheap; one that runs `make accept` ten times or
holds workshop's single slot for an hour is not.

The spec's loop is one turn: ask, tool, feed back, ask, until a plain
answer. Long-running work should be a *policy on top of turns*, not a
change to the loop, and it must stay inside the same guard.

## Decision

### 1. Budgets, not a count
A turn has a budget in four dimensions, each with a **soft** and a **hard**
limit:

| Dimension | Soft (checkpoint) | Hard (stop) | Why it is measured |
|---|---|---|---|
| tool calls | 50 | 500 | the old limit, kept as one signal |
| wall clock | 20 m | 4 h | the model holds a shared slot |
| model tokens (prompt + output) | 1 M | 10 M | cost and drift |
| irreversible actions (writes, commands) | 25 | 200 | blast radius |

Configured under `[budget]`; overridable per run (`--budget-wall 2h`).

### 2. Checkpoints go through the confirmer
Crossing a soft limit raises a **checkpoint**: a prompt through the same
`Confirmer` the guard uses, so any front-end can answer and a
non-interactive run gets a policy answer. The prompt shows what was done
(files touched, commands run, tokens, elapsed) and offers: continue (the
soft limit doubles), stop, or continue with a budget of the user's choice.
Every checkpoint is a log event (`turn.checkpoint`, a new kind) with the
budget state and the answer. A hard limit stops the turn with a note; it
cannot be answered away.

### 3. Progress guard
Independent of budgets, the loop stops a turn early when it can see it is
not progressing:

- the same tool with the same arguments four times in a row;
- a denied call retried three times;
- ten consecutive tool errors.

These are cheap to detect from the events already in the window. A stop
here is a note and an error to the front-end, never silent.

### 4. Long runs are chained turns
The loop stays one turn. A long run is a **driver** that starts a turn,
waits for it to end, and — while the *run* budget allows and the model has
not said it is done — sends the next message ("continue with the plan").
Because the log is the source of truth, a run can be stopped and resumed at
any turn boundary, and a turn that hit a hard limit is simply followed by
another. The driver is the front-end's job (`nibb run --task FILE --budget
wall=2h`), not the core's; the core exposes budgets and checkpoints, and a
`Session.Resume(id)` that rebuilds from a log (friction.md item).

### 5. Unattended runs have more guards, not fewer
`nibb run` without a human present is allowed only with:

- no interactive confirms — every write must be covered by a standing
  approval and every command by an allow entry; anything else is a deny,
  logged, and the model is told;
- the progress guard on, hard budgets set, and a run-level budget;
- a plan (P5: Seam A) whose steps become checkpoint boundaries, so the run
  can be reviewed step by step from the log;
- the same envelope: no network, repo pin, denylist. Unattended is a
  reason to trust the guard more, never to relax it.

## Consequences
- `loop.max_tool_calls` becomes `budget.tool_calls_hard`; the soft limit
  is what users notice.
- The plan gate (P5) and this ADR compose: a plan's steps are natural
  checkpoint points, and a checkpoint is where Seam A can re-score.
- Chained turns rebuild the window each time (ADR 9): no special memory
  path is needed for long runs; P10's summaries make them cheaper.
- Implementation order: budgets + checkpoints + progress guard in the loop
  (small); `Resume`; then the `run` driver. Nothing here changes the guard.

## Progress guard implementation — 2026-09-22

Section 3 is implemented independently of the larger budget/checkpoint change.
Each turn observes completed tool calls and stops after four identical
consecutive calls, an original denied call plus three retries of that same call
(even with intervening calls), or ten consecutive failures. A successful call
resets the error streak and clears prior denials for that call. Call identity
uses tool name and JSON arguments; object ordering and whitespace do not matter,
arrays retain order and numeric spellings retain full precision. Denials are
recognized by their typed Scheme error, not text matching.

The triggering result remains in history. The stop is both a durable note and a
frontend error. Remaining calls in that response receive explicit "Not executed"
results without evaluation, including at the existing hard tool-call limit, so
a follow-up turn has a complete transcript. A new user message starts fresh
progress counters. This adds no authority, background driver or automatic retry.
The existing `loop.max_tool_calls` setting and its defaults remain until the
budget dimensions and checkpoint UI are implemented together.
