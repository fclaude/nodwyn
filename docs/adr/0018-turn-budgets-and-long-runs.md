# ADR 0018: Turn budgets and long-running turns

- Status: accepted; implemented (turn budgets, checkpoints, progress guard and supervised driver 2026-09-22; unattended runs and judged approval 2026-10-01)
- Date: 2026-09-21

## Context
`loop.max_tool_calls` (50) is a single hard stop. It is the right kind of
guard — a turn that has made fifty tool calls is either doing a lot of real
work or is stuck — but it cannot tell the two apart, and the point of nodwyn
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
another. The driver is the front-end's job (`nodwyn run --task FILE --budget
wall=2h`), not the core's; the core exposes budgets and checkpoints, and a
`Session.Resume(id)` that rebuilds from a log (friction.md item).

### 5. Unattended runs have more guards, not fewer
`nodwyn run` without a human present is allowed only with:

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

Since ADR 26 the loop is a Scheme strategy; these checks and the budgets run
inside its `run-tools` and `model-turn` primitives, and a stop cannot be
caught by the strategy's `guard`.

## Budgets and checkpoints implementation — 2026-09-22

Sections 1–3 are implemented. `internal/budget` owns one turn's limits and
accounting. Core starts its lifetime before the plan gate; loop accounts for
model calls and reported control-provider tokens. A session gate delegates all
authorization to guard first, then charges irreversible host attempts. Nested
Scheme tools cannot hide host actions, and denials consume no action allowance.
Counts reserve capacity before execution; failed authorized attempts still count.

Budget prompts share core's pending-confirmation map, IDs and `Answer` route.
The existing boolean `guard.Confirmer` stays intact: a narrow budget callback
returns a structured choice through the same session confirmation service.
`y` doubles the soft threshold (at least above observed usage, capped at hard),
`n` stops, and a number/duration chooses an absolute soft threshold. Invalid
choices retain the prompt. Budget answers never create standing approvals.
No-confirm runs decline; cancellation is logged. `turn.checkpoint` carries the
snapshot, choice and any error; replay displays it without prompting.

The hard wall deadline includes model waits, tools, planning and confirmations.
Soft wall checks happen between operations. Token accounting uses reported
prompt plus output counts; one response may overshoot, after which no further
operation starts. The judge API's usage is unavailable and is not counted here.
A budget stop cancels the turn context and cannot be caught to continue executing
Scheme. Partially executed tools are reported as potentially partial; remaining
batch calls are explicitly not executed.

New configurations use the proposed defaults, including 500 hard tool calls
(the prior implemented default was 200). Explicit `loop.max_tool_calls` and
`--max-tool-calls` keep their hard-override meaning; the file rejects conflicting
legacy and new hard-call settings. CLI wall/token/action overrides lower the
soft threshold if necessary. Every new turn and resumed turn gets fresh limits.
Sections 4–5, the automatic run driver and its unattended policies, remain
proposed and are not enabled by this change.

## Supervised driver implementation — 2026-09-22

Section 4 is implemented in `internal/frontend/run`, owned by the RPC daemon.
`nodwyn run --task FILE --budget-wall 2h --max-turns 20` requires finite run limits
and records a plan for every turn. Planning now charges reported control-model
tokens to the same turn budget. Typed budget stops and additive `turn.end`
outcomes distinguish hard ceilings from declined checkpoints and failures.

Continuation after a successful or hard-limited turn requires a one-time run
checkpoint. It grants no tool authority. Completion is an exact final-line
marker in a successful plain model response, explicitly model-reported rather
than verified. Logs preserve limits and turn count across explicit resume;
missing outcomes and truncated tails prevent automatic continuation. The daemon
joins drivers before session shutdown. See [runs](../runs.md).

Section 5 remains pending. This implementation does not expose unattended mode
or remove confirmations to approximate it.

## Unattended runs implementation — 2026-10-01

Section 5 is implemented as an option of the run driver:
`nodwyn run --unattended` (`run.start {unattended: true}`).

- The session gets `core.Options.Unattended`: its confirmer declines every
  tool confirm (`guard.DenyAll`), or answers as `--yolo` does when the run
  also auto-approves (ordinary yes, security no). Nothing reaches a
  front-end as a prompt. A soft budget checkpoint continues up to the hard
  limit and is recorded with actor `unattended`; `--no-confirm` keeps
  declining it, as before.
- "A plan whose steps become checkpoint boundaries": the driver counts the
  `plan.step` marks with state `done` each turn records (the model marks
  steps with `plan-step`, the strategy records them). After a turn that
  marked one or more, the run goes on; after a turn that marked none, it
  stops with the reason, for a person to review and resume. Each answer is
  a `run.checkpoint` with actor `unattended` and a `reason`.
- `run.state` records `unattended` and `auto_approve`. A resume restores
  neither: `run.resume` takes them as parameters, and the CLI's
  `nodwyn run resume --unattended [--yolo] SESSION`.
- `(unattended?)` is true for these sessions too, so they get the
  unattended system prompt and the evidence loop.

"Every write must be covered by a standing approval and every command by an
allow entry; anything else is a deny" holds without `--yolo`. With it, the
run trades that for ordinary confirms answered yes, as a supervised `--yolo`
run already did; security confirms and every deny are unchanged.

### Judged approval — 2026-10-01

Between declining every confirm and answering them all yes, the judge can
answer: `nodwyn run --judge` (`run.start {judge: true}`, with or without
`unattended`). The session's confirmer asks the judge a `jev-choice` between
allowing and refusing each ordinary call, given the task, the turn's plan
and the call, and allows it only if the judge's probability of "allow" is
at least `jev.approve_threshold` (config.toml, default 0.8). Security
confirms are declined without asking; a failure anywhere is a no; a judged
answer is for that call only (scope once) and is attributed to `judge`.

The decision is in Go (`internal/core/judge.go`): what the judge is shown,
the question, how the answer is read, and the threshold from the user's
config. It calls the `jev-choice` primitive from the primitive table with
the key Go holds, so neither a Scheme definition shadowing it nor one
replacing the procedure that asks can approve a call. Scheme may only
refuse more: a project's `(approve-refuse? call)`, if defined, is asked
about a call the judge allowed, and true, or an error, declines it. (A
first version put the whole decision in Scheme, approve.scm, which a
repository's `.nodwyn/init.scm` could redefine to approve everything.) What
Scheme can still shape is the plan the judge reads, since a project's
strategy records the plan. The judge's call crosses the
guard like the plan gate's, so the `jev-key` binding is checked and what is
sent is redacted. This is ADR 20's Seam C ("score an ambiguous situation")
put where an unattended run needs it; ADR 19 says what it may be shown.

## Spend — 2026-10-01

A turn's state also counts what its model requests cost, as far as known
(`cost`, `priced`, `unpriced`, `cached_tokens`), and a checkpoint shows it.
It is not a budget dimension: nothing stops a turn on spend. Cached tokens
still count in full against the token budget, which measures context, not
money. (Amended 2026-10-02 below: spend may be limited, if the person says
so.)

## Spend limit — 2026-10-02

Spend is a fifth dimension, `cost`, and the only one off by default:
`[budget] cost_soft` and `cost_hard`, in US dollars, set by the person, or
`--budget-cost` for one session or run. Unlike the others it is the task's,
not the turn's. A turn starts from what the task's earlier requests cost
(the `cost` the `llm.resp` records carry, so a resumed session counts its
history), and a soft limit raised at a checkpoint holds for the turns after
it. Its hard stop is a `budget.Stop` with `Spend` set, recorded as `turn.end`
outcome `spend_limit`. A driver does not go on after it, as it would after a
hard turn budget: the next turn would start past the limit. A run ends
`limited`. A run's own limit is recorded in `run.state` (`cost`) and kept on
resume, as the deadline is.

It counts only known costs, reported by the provider or priced from the
model's `price_*`. Counting an unknown cost as zero is the only honest choice
short of refusing to run, so the first turn with an unpriced request says so
in a `note`. Checks run where the token budget's do, before each model
request and tool call, so the request that crosses the limit completes and
nothing starts after it. The unit is a millionth of a dollar, so a limit is
an integer like the others. An unattended run's soft checkpoint goes on to
the hard limit.

## Runs from the console — 2026-10-02

The console's Home starts a run as well as a chat. How it starts a task
unless the person chooses otherwise for that one is the user's
configuration, `[tasks]` in `config.toml`: `mode` (`chat`, the default;
`run`; or `unattended`), `approval` (who answers a run's ordinary confirms:
`ask`, `judge` or `yes`), and a run's `turns` and `wall`. They are only
what the console sends: Home calls `run.start` with them as explicit
parameters, so `auto_approve`, `judge` and `unattended` remain properties
of a run, never of the daemon, and a run's turns and lifetime stay finite.
`nodwyn run` keeps its own required flags.

Answering a run's requests without a person is chosen on the computer
nodwyn runs on, as "always allow" for uploads is (ADR 30): `run.start` and
`run.resume` with `auto_approve`, `judge` or `unattended` are refused to an
actor relayed `via` a paired device, and the web bridge refuses them to any
remote connection. A device may start a supervised run whose confirms it
answers itself.

A run's first message is the run prompt around its task, so the task list
titles a run by its task (`run.state`), redacted as a first message is.

### The judge's spend — 2026-10-02

The judge (Jev) reports each request's tokens and never its cost. A turn
counts its judge requests apart from its model requests: calls, tokens, and
the cost `[jev] price_input` and `price_output` estimate. The token budget
leaves them out, since it measures the model's context. A spend limit counts
their cost, since it measures money, and `knownSpend` reads it from the
`jev.req` records as it reads model costs from `llm.resp`. A judge request
with no prices is not counted, and the first turn with one says so, as for
model requests.
