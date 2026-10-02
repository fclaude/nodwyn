# Bounded supervised runs

`nodwyn run` submits a task to a running daemon. The driver is frontend policy
above the existing core session, plan gate and turn loop. It adds no tool
permissions, bypasses no checker, and does not change the loop's host interface.

```sh
nodwyn daemon
# In another terminal:
nodwyn run --task task.txt --repo . --budget-wall 2h --max-turns 20
# nodwyn run start ... is the same command, spelled out
nodwyn ui
# Or nodwyn tui --socket /absolute/path/to/daemon.sock
```

Each turn sends the message `(run-turn-prompt task complete-line turn
max-turns)` makes, a Scheme prompt a project may redefine
([docs/scheme.md](scheme.md) §9.1); the turn must plan before it acts (§9.5).

The task is UTF-8 text, at most 64 KiB. Both limits are required; the turn limit
is 1–10000. `--budget-wall` is the total run lifetime, including model waits,
approval waits and time spent stopped. The daemon's configured per-turn budgets
still apply, including planning tokens reported by the control provider. A
provider response can exceed its token ceiling; no further operation then starts.
Judge token usage remains unavailable.

A run's turn ends on evidence (docs/scheme.md §9): the default loop reminds a
model that stops before changing anything, or without a test run after its
last change, and offers an honest way out when the tests cannot run at all.
Running its own check script in the scratch directory counts as a test run.
In a git work tree git decides what changed, and files left in the
repository that git does not track are named once before the turn ends.
Before the plan, a read-only explorer looks at the repository, so the plan
names real files.

Each turn requires a nonempty, durably recorded plan before execution. The
Scheme `nodwyn-turn` runs the plan gate first in every run turn, whatever `agent-turn`
a project defines ([docs/scheme.md](scheme.md) §9.5): the control model drafts
the plan in a context of its own, offered no tools, and with a Jev credential
the score/revise path runs through the guard; the noul decision cannot skip a
required plan. Without a judge the model still drafts a plan. Judge outages
retain the plain-plan fallback. The core refuses anything irreversible until a
plan is recorded and fails a turn that records none; a project that redefines
`nodwyn-turn` to plan its own way is held to the same rule. Planner or plan-recording
failure prevents execution.

The continuation asks for one unfinished plan step, marked with `plan-step`
when it is done, and a progress report. This is an instruction to the model,
not a parser that proves a step was completed: a mark (`plan.step`) is the
model's word, shown as such in the console's plan panel, and the evidence is
in the transcript.
Before another turn, the operator gets a **run checkpoint**. A one-time yes
continues the run; it grants no filesystem, command, credential or network
permission. The checkpoint's prompt ID and answering peer are recorded.
Tool prompts and soft-budget checkpoints remain separate.

A successful plain model response whose exact final line is
`NODWYN_RUN_COMPLETE` ends the run as **model-reported completion**. This is not
proof that the task is correct or complete. Tool results, responses containing
tool calls, truncated responses, and marker substrings do not signal completion.
The final reply is the last one the conversation still shows: a reply inside a
folded context, such as a reviewer sub-agent's verdict, is not the model's
answer, so it neither completes a run nor hides the model's completion line.
Review the transcript and normal project checks. A hard turn budget permits a
checkpoint and another turn; user refusal, cancellation, progress stops and
provider failures stop continuation.

## Stop, inspect and resume

```sh
nodwyn run status SESSION
nodwyn run stop SESSION
nodwyn run resume SESSION
```

Each command accepts `--socket PATH` before the session ID. Browser/terminal
Abort stops the whole run, including a between-turn checkpoint. A client closing
or disconnecting does not stop it. The active run reserves its session: manual
send/eval/friction/policy operations are rejected until it stops. Prompt replies
and inspection of recorded history remain available.

The daemon stops and joins drivers before closing sessions on shutdown. There
is no automatic restart. Explicit resume reloads current session policy, retains
the original deadline and consumed turn count, and restores no temporary grants.
Completed, expired and exhausted runs cannot resume. An interrupted turn lacking
a durable `turn.end` outcome, or a truncated log tail, requires manual review;
the driver refuses to infer success or replay unfinished tools. Ordinary legacy
`turn.end` records without an outcome are unknown, not evidence of completion.

`run.state` records task, limits, disposition and initiating actor; `run.turn`
reserves a numbered turn; `run.checkpoint` records continuation choices. They go
through normal session redaction and persistence. Each submitted turn records
its task in the ordinary conversation and rebuilds context from history.
`turn.end` retains its historical text and adds optional outcome/error fields.

## Unattended runs

```sh
nodwyn run --task task.txt --repo . --budget-wall 2h --max-turns 20 --unattended
```

An unattended run is one nobody answers (ADR 18 §5). It gets more guards, not
fewer, and nothing in it ever waits for a person:

- **Tool confirms are declined** without asking: a write or a command runs
  only if a standing approval covers it (`.nodwyn/allow.toml`, config); any
  other is refused, logged, and the model is told. With `--yolo` too, ordinary
  confirms are answered yes instead. Security confirms (a credential-shaped
  write, a protected path, a command built from tool output) are declined
  either way.
- **A soft budget checkpoint goes on to the hard limit**, recorded as answered
  by `unattended`; the hard limits still stop the turn.
- **The run checkpoint is answered by progress.** A turn that marked at least
  one step of its plan done (`plan-step`, recorded as `plan.step`) is followed
  by the next; a turn that marked none stops the run, with the reason in its
  status ("turn 3 marked no plan step done"), for a person to look before
  another. Each answer is a `run.checkpoint` with actor `unattended` and its
  reason. A mark is the model's word: what the run proves is that the model
  kept claiming progress, step by step, in a log that can be reviewed step by
  step.
- **With `--judge`, the judge answers instead.** Each ordinary tool confirm is
  put to the judge (Jev): it is shown the task, the turn's plan and the call
  (the tool, why it needs approval, the file it writes or the command line
  it runs; at `jev.content = "full"` also what it would write or send), and
  asked whether a call like this should run with nobody watching. The call
  runs only if the judge's probability of "allow" is at least
  `jev.approve_threshold` in config.toml (0.8). The decision is Go's: no
  Scheme, a repository's `.nodwyn/init.scm` included, can approve a call the
  judge refused. A project may only refuse more, by defining
  `(approve-refuse? call)`. A judge that fails or cannot be reached is a no;
  security confirms are declined without asking it. Each answer is the
  guard's decision with actor `judge`, a note with the judge's probability,
  and the judge's `jev.req`. `--judge` needs a judge (a `jev-key` stored and
  bound, `jev.content` other than `none`) and excludes `--yolo`; it also
  works in a supervised run, where you still answer the run checkpoints.
- Everything else is as for a supervised run: the plan before every turn,
  finite turn and wall limits, the progress guard, completion only by the
  model's final line, the same pin, denylist and binding table.

`run.state` records how the run was started (`unattended`, `auto_approve`,
`judge`).
A resume never brings either back from the log: `nodwyn run resume SESSION`
resumes a supervised run, and `--unattended` (with `--yolo` or `--judge` if
wanted) must be said again, as the person resuming decides how it goes on. The console shows
an unattended run as such and can stop and resume it; its Resume button
resumes it supervised.
