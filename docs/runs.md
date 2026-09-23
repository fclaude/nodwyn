# Bounded supervised runs

`nibb run` submits a task to a running daemon. The driver is frontend policy
above the existing core session, plan gate and turn loop. It adds no tool
permissions, bypasses no checker, and does not change the loop's host interface.

```sh
nibb daemon
# In another terminal:
nibb run --task task.txt --repo . --budget-wall 2h --max-turns 20
nibb ui
# Or nibb tui --socket /absolute/path/to/daemon.sock
```

The task is UTF-8 text, at most 64 KiB. Both limits are required; the turn limit
is 1–10000. `--budget-wall` is the total run lifetime, including model waits,
approval waits and time spent stopped. The daemon's configured per-turn budgets
still apply, including planning tokens reported by the control provider. A
provider response can exceed its token ceiling; no further operation then starts.
Judge token usage remains unavailable.

Each turn requires a nonempty, durably recorded plan before execution. With a
Jev credential the existing score/revise path still runs through Scheme and the
guard; the noul decision cannot skip a required plan. Without a judge, the model
still drafts a plan. Judge outages retain the existing plain-plan fallback.
Planner or plan-recording failure prevents execution.

The continuation asks for one unfinished plan step and a progress report. This
is an instruction to the model, not a parser that proves a step was completed.
Before another turn, the operator gets a **run checkpoint**. A one-time yes
continues the run; it grants no filesystem, command, credential or network
permission. The checkpoint's prompt ID and answering peer are recorded.
Tool prompts and soft-budget checkpoints remain separate.

A successful plain model response whose exact final line is
`NIBB_RUN_COMPLETE` ends the run as **model-reported completion**. This is not
proof that the task is correct or complete. Tool results, responses containing
tool calls, truncated responses, and marker substrings do not signal completion.
Review the transcript and normal project checks. A hard turn budget permits a
checkpoint and another turn; user refusal, cancellation, progress stops and
provider failures stop continuation.

## Stop, inspect and resume

```sh
nibb run status SESSION
nibb run stop SESSION
nibb run resume SESSION
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

This slice supports supervised runs only. Unattended continuation and its tighter
policy envelope are still pending; removing the checkpoint requirement is not an
unattended implementation.
