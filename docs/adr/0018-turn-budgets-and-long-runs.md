# ADR 0018: Turn budgets and long-running turns

- Status: accepted; implemented (turn budgets, checkpoints, progress guard and supervised driver 2026-09-22; unattended runs and judged approval 2026-10-01; the run's plan carried across its turns 2026-10-02); amended by ADR 35 (soft limits off by default; the console starts only chats) and ADR 49 (a task's turn limits can be off)
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

## The plan is the run's — 2026-10-02

§4 has the driver send "continue with the plan" and §5 makes "a plan whose
steps become checkpoint boundaries". As implemented, "a plan for every
turn" was a new plan every turn. Plan state was the turn's (the core's
`planned` flag, the loop's steps and marks, the window's plan, all reset
at each `user.msg`), and a run's continuation is a `user.msg`. So each turn
explored the repository again and drafted a plan for the whole task in a
context that did not show what earlier turns had done, while the run prompt
asked for "one unfinished plan step". The unattended checkpoint counted
steps marked against a plan drafted that same turn.

A turn stays what it was: one message and the work up to a reply with no
tool call, or a budget stop. It is the unit of the per-turn budgets, the
run checkpoint and resuming. The plan belongs to the run:

- The first turn of a run drafts the plan as before. Each later turn carries
  it: `plan-gate` records the same plan again as `gate.plan`, `noul`
  `carried`, with `marks`, the `plan.step` marks of the run's earlier turns,
  the latest per step. Recording it keeps the core's invariant (a required
  turn records a plan) and the window's rule (a turn's plan is the
  `gate.plan` after its `user.msg`) unchanged. The window shows the carried
  marks under the plan (recipe v13); they are fixed for the turn, so the
  system prompt is too. The loop's `record-step` starts from them, so step
  numbers and "next unmarked" hold across turns.
- `(run-plan)` reads the run's plan from the log: the latest plan a run
  turn recorded (a `user.msg` after a `run.turn`) and the marks since. Chat
  turns between do not count, a resume keeps the plan, and a `run.state`
  `running` with no turns yet, a fresh run, starts with none.
- A new plan is drafted only once every step is marked and the run goes on.
  The planner gets the previous plan and how each step went. There is no
  separate "replan" tool: a model that finds the plan wrong marks the steps
  that no longer apply skipped, with why, and the next turn plans again.
- The unattended checkpoint still counts the `plan.step` marks a turn made.
  Carried marks are in `gate.plan`, not `plan.step`, so they are not counted
  again, and progress is now measured against a plan that stays put.

All of this is Scheme (`plan.scm`, `prompts.scm`) over one read-only
primitive and an extra field of `record-plan`. A project that wants a plan
per turn redefines `plan-gate`.

## Cached tokens count a tenth — 2026-10-02

"Cached tokens still count in full against the token budget" (Spend,
above) made the default soft limit (2M) a pause every few minutes on a
model with a large context: a chat at 100k-140k tokens of context reached
it after about twenty requests, nine tenths of what it counted read from the
provider's cache, and waited two hours for a person. A prompt token the
provider says it read from its cache now counts a tenth of one
(`budget.CountedPrompt`), about what providers charge for it and what it asks
of the server. A provider that does not report its cache counts in full, as
before, and the console's "Tokens sent" counts the same way. The spend
limit is unchanged: it counts what requests cost.

## A chat is the way to work — 2026-10-02

ADR 35 changes two things here. The soft limits default to the hard ones
(500 tool calls, 10 million tokens, 200 actions, 4 h), so no checkpoint
asks unless the person sets one lower; a soft limit the file leaves unset
follows its hard one. And "Runs from the console" is withdrawn: Home starts
every task as a chat, `[tasks]` is gone (a file that has it still loads),
and the console neither starts nor resumes runs. `nodwyn run` stays as a
batch tool for scripts and benchmarks, with its flags as above; its
unattended runs' writes in the folder are a standing approval by default
(`[approval] writes`), which §5 already allowed.

## Tokens used — 2026-10-05

The console called the token budget's count "Tokens sent", and Settings
explained it as "every request's prompt in full". Neither was true once
cached tokens counted a tenth (above), and the reply's tokens, which come
back rather than go out, were always in it. It is now "Tokens used" in
Settings and the task's side panel, explained the same way everywhere:
each request's prompt and reply, a prompt token the provider read from its
cache counted as a tenth. What is counted is unchanged.

## Amendment 2026-10-05: a stall is one tool failing on one file

A chat on a coding task spent five minutes and about thirty calls failing
to edit one line of one test file, answering "old text not found" each time,
and ended only when the person declined the approval it kept raising. None
of the guard's three rules saw it: the calls were not identical, a read or an
edit that did work came between the failures, and so no ten failures in a
row. The model could not see why. Ingress redaction (ADR 25) shows it a
credential-shaped text as `[REDACTED <class>]` wherever it reads one, so
`old`, copied from what it read, held a placeholder the file did not.

The guard has a fourth rule: the same tool failing on the same target in six
of the last sixteen model responses, whatever else came between them. The
target is the call's `path` argument, or the call itself for a tool that has
none. A response counts once however many of its calls failed, since a batch
repeats one mistake in parallel. Nothing resets it, neither a success nor a
different edit; the failures leave it as the responses pass. Like the others
it ends the turn with a note and an error naming the tool and the file, and
the next message starts a fresh turn.

The numbers were chosen by replaying the 73 session logs on one machine
through candidate rules. Keyed on the error text instead of the file, a rule
also fired on a long docs edit that went on to succeed, and on five parallel
calls making one mistake. Keyed on the file and counted by response, six of
sixteen fires once, on the stall above, at its sixth failure; five fires
earlier, on a turn that recovered.

Its cause was `fs-edit`, which could not match text a model reads as a
placeholder; ADR 43 has it match in the text the model reads, so a line with a
credential on it can be edited, and an `old` that guesses a credential says to
write the placeholder instead.

## Amendment 2026-10-05: polling is not a loop, a stop says where the work was, and a provider's pause is waited for

Three ways a turn ended that said nothing about the work, in the 75 local
session logs of 2026-09-30 to 10-05.

- **Polling a running command.** The same `shell-read` four times in a row
  ended two turns that had done nothing wrong: a promote that had succeeded
  (20261003T002506Z-f564a28c, seq 5889 to 5907) and a browser check that
  was still running (20261003T143916Z-48f31857, seq 7323 to 7341). A read
  of a background command whose answer says it is still running
  (`: running (`) no longer counts as a repeated call; such reads have a
  count of their own, and thirty in a row, with no other call between,
  end the turn. A wait is up to a minute, so thirty is half an hour: a
  command that hangs still ends it. Any other call starts the count again.
  A read of a command that has exited is an ordinary call. This is the
  rule's weakest edge: a model alternating a poll with another call is not
  caught by either count, as before.
- **A stop says where the work was.** A turn that ended in an error, a
  budget or a progress stop recorded the reason and nothing else, so a
  person read the transcript to learn whether it was safe to go on. Its
  `turn.end` now carries `where`, one line: how many of the plan's steps are
  marked and the next one, the files the file tools wrote in the turn, and
  the last `shell-exec` with how it ended. The console shows it beside the
  stop, the plain chat and the TUI print it. It is read from the log, and
  runs nothing: `git diff --stat` was not used, since which program a
  repository's git configuration would run is not the host's to leave to a
  stranger's clone. A turn that completed, or that the person cancelled,
  carries none. Not built: the last check by name (a test run) apart from
  the last command, and files changed through the shell.
- **A provider's pause and an account out of credit.** A model request is
  sent again twice, two and four seconds after it failed. Of the 44 retried
  429s in the logs, 39 were "tokens per minute" in one session
  (20261004T182412Z-747045aa), where both retries land in the minute that
  was full. A server's `Retry-After` (seconds or a date) or
  `Retry-After-Ms` is now waited for, at most a minute (`llm.MaxRetryAfter`);
  a longer one ends the request at once with the server's words. The logs
  keep no response headers, so whether the provider of that session sends
  one is not known: a provider that does not gets the old delays. A 402,
  an account out of credit, is not retried: it is out of credit a few
  seconds later too (seq 6842 to 6847 of the same session), and says so with
  a hint to add credit or choose another model.

TestPollingARunningCommandIsNotAProgressStop,
TestProgressPollsOfARunningCommandHaveTheirOwnCeiling,
TestAStoppedTurnSaysWhereItStopped, TestOpenAIRateLimitWithRetryAfterIsWaitedFor,
TestChatRetryingWaitsForRetryAfterAndStopsAtPaymentRequired and the
transcript browser check cover them.

## Amendment 2026-10-06: a stopped unattended turn goes back to its last passing state

A hard limit stops a turn where it happens to be, and so does the progress
guard, and neither can be caught by the strategy. In a turn nobody reads
(`--yolo`, judged or unattended), that is often one edit past the last state
its own tests accepted, and the work is collected as it lies.

- **Keeping.** After a test or check run that exits 0 in a turn that has
  changed the repository, the evidence loop calls `(keep-passing)`
  (strategy.scm, `keep-passed`). The host (`prim.Keeper`) keeps the changes
  git lists against HEAD, as the bytes on disk, in the scratch directory,
  replacing the earlier keep. A turn that changed nothing keeps nothing: the
  state its tests passed on before any edit is no result to go back to
  (`TestAHardStopBeforeAnyEditPutsNothingBack`).
- **Putting back.** When the turn ends in a hard budget stop or a
  `loop.ProgressStop` (now a type, so the session can tell it), in a session
  that is `--yolo`, judged or unattended, the core restores
  (`Session.restoreKept`): a kept file as it was, a path changed since and not
  kept as HEAD has it or gone, a file made since removed. Each file it
  replaces is saved first under `kept/discarded`, in the same relative path.
  What it did goes to the log as a note and to the end of `turn.end`'s
  `where`, so the person is told. It refuses, saying so, when HEAD is not the
  commit the state was kept on, and when nothing was kept it says why a keep
  failed, if one did.
- **What it never touches.** A file the credential denylist refuses, a path
  `guard.IsProtected` names (`.nodwyn/`, `.git/`, `.github/`, `.gitea/`,
  `scripts/`, `Makefile`: a write there is a security confirm), links and
  directories, the index, and a path whose directory a link now leads out of
  the repository. Files over 8 MiB, more than 64 MiB or 4000 files are not
  kept at all. Git is asked for the list of changes and for HEAD's blobs only,
  as the git tools ask it, with nothing that runs another program, and not
  through `git checkout`, whose hooks a stranger's clone could make run.
- **Not for** a chat, a person's own Stop, a soft limit the person answered,
  or an ordinary error: where someone is there to decide, the files stay as
  they are.
- **Why this, and what it costs.** Asked for as "keep the last passing patch
  at the round limit". Of the 190 turn ends in the 161 local session logs of
  2026-09-21 to 10-05, three were a hard or progress stop (an actions budget,
  and two progress stops on a poll that was only waiting, 20261003T143916Z-48f31857
  and 20261003T002506Z-f564a28c), and all three were chats a person was
  answering, which this leaves alone. So, like the failing-run reminder of ADR
  32's same-day amendment, it comes from the code and not from a logged
  failure: a hard stop ends the strategy with it, and a benchmark harness
  collects whatever is on disk. It deletes
  nothing the model wrote, but it does change what is on disk, for the
  sessions that nobody reads. A passing run of one test is not a proof of the
  whole change; it is the best state the turn's own evidence names. A file
  changed through the shell before the keep is in it; a change the shell makes
  to a protected path is not undone.
- **Tests.** `internal/prim` (`TestKeeper*`, `TestKeepPassingPrimitive`: the
  state put back, HEAD moved, a link in the way, too large, nothing kept) and
  `internal/core` (`TestHardStopPutsBackTheLastPassingState` runs it through
  a scripted model and the real strategy; it fails with `keep-passing` taken
  out, as `TestRestoreKeptOnlyForAHardStopOfAnUnattendedTurn` covers which
  stops and sessions it applies to).

## Amendment 2026-10-15: a task's turn limits can be off (ADR 49)

§1's limits apply to every turn of every task unless this computer turns
them off for one task: Home's **Limits** box as it starts, or the task's own
between turns (`session.open {limits: false}`, `session.limits.set`). Off,
the task's turns have no limit on tool calls, tokens, actions or time, so
"The hard wall deadline includes model waits, tools, planning and
confirmations" no longer stops a turn that waits all night for an answer.
The spend limit, the timeouts and §3's progress guard stay. In the budget a
hard limit of 0 is now none in every dimension, as it was for spend, and
`budget.Start` sets no deadline without a wall limit; `config.toml` still
requires positive turn limits. The choice is a `session.limits` record,
kept on resume. A paired device can neither make the choice nor change it.
Runs keep theirs: `session.limits.set` is refused during a run.

## Amendment 2026-10-18: a sandbox run is unattended and auto-approved (ADR 54)

`run.start` and `run.resume` take `sandbox: "container"`, which makes the
run unattended and auto-approved whatever `unattended` and `auto_approve`
say, and excludes `judge`; `run.state` records `sandbox`. Like the other
flags it is never read back from the log: a resume says it again.
