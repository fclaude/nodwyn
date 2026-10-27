# ADR 0035: A chat is the way to work, and it asks less

- Status: accepted; implemented 2026-10-02; amended 2026-10-03 (a turn that did not finish leaves its plan to the next message)
- Date: 2026-10-02
- Amends: ADR 17 (writes need no approval in the folder), ADR 18 (soft
  limits; runs from the console), ADR 30 (what a device may start)

## Context
The console offered three ways to start a task ("Runs from the console",
ADR 18): a chat, a run and an unattended run. A run had a setting for who
answers its requests (ask, judge or yes), and a turn and time limit.
They differ in who decides that the work goes on. From the person's seat:

- a run is a chat in which the program types "continue" and asks before
  each turn;
- an unattended run is that with nobody asked;
- who answers requests applied only to runs, since a chat always asked.

So a person chose among a handful of combinations before typing a
request. The default, a chat, was the one that asked the most:

- every write and every command no standing approval covered was a
  prompt;
- a turn stopped to ask at its soft limits (50 tool calls, 20 minutes, a
  million tokens or 25 actions), which a long request reaches.

ADR 17 named the failure this leads to: confirm fatigue turns "y" into a
reflex, which is worse than no prompt.

## Decision

### 1. One way to work: a chat
The console starts every task as a chat. A request that takes long is a
message like any other. The turn goes on until the model answers or a
hard limit stops it.

- Home has no mode picker, and Settings has no "Starting tasks".
- `[tasks]` is gone from config.toml. A file that still has the table
  loads, and the table is ignored.
- `nodwyn run` stays as a batch tool for scripts and benchmarks: it carries
  a task to its end with no conversation ([runs](../runs.md)). The console
  does not start or resume runs. It shows a run started elsewhere, and its
  Stop ends it. A message afterwards goes on with the task as a chat.

### 2. Writes in the folder go ahead
`[approval] writes = "allow"`, the default, makes every write the pin
lets through a standing approval (`guard.FolderWrites`). That covers the
repository or folder, the scratch directory and a mount opened for
writing.

- Each write is logged with its diff and shown in the transcript.
- Its previous content is kept. Git keeps it when git has the file
  committed and unchanged. Otherwise nodwyn keeps it in `.nodwyn/versions`.
  Until now that store skipped every file in a git work tree, uncommitted
  changes included, which git cannot give back. `nodwyn undo` and the
  console's Restore put a version back.
- What still asks, every time: a protected path (`.git/`, `.nodwyn/`, CI
  definitions, `scripts/`, `Makefile`) and content that looks like a
  credential. These are security confirms, which no standing approval
  waives.
- What still refuses: the pin, the credential denylist and a read-only
  mount.
- `writes = "ask"` brings the prompt back. "Always" and "this turn" answer
  it as before.

### 3. Commands: the judge, then the person
`[approval] commands` answers the other ordinary confirms that no
standing approval covers: a command, input to a running program, an MCP
tool that acts. It takes three values:

- `"judge"`, the default. The judge (Jev) is asked first, as a judged run
  asks it (ADR 18, "Judged approval"). It is shown the task, the turn's
  plan and the call. The decision is made in Go against
  `jev.approve_threshold`, and `(approve-refuse?)` may refuse more.
  - If the judge allows the call, it runs, recorded with actor `judge`.
  - If it refuses or fails, the person is asked, and the prompt says what
    the judge answered.
  - Without a judge, the person is asked. That means no jev-key stored,
    `jev.content = "none"` or a locked keychain.
- `"ask"`: the person.
- `"allow"`: the call runs, recorded with actor `setting`.

What this setting never answers goes to the person:

- a security confirm, such as a command built from tool output, a write to
  a protected path or content that looks like a credential;
- a write, which is `writes`' to decide;
- a call that carries a credential, or proposes a binding;
- an upload.

Unlike in a run nobody watches, these are asked, not declined.

A person's "always this session" still makes a command prefix standing,
so the judge is not asked about it again. Standing approvals in
`allow.toml` and `policy.allow_commands` come first, as before. The
console's "Don't ask" switch for one task (ADR 17, amended the same day)
answers what `[approval]` would leave to the person, and while it is on
the judge is not asked either.

`[approval]` applies to sessions a person can answer: the console's, the
TUI's, the plain front-end's and a supervised run's. The flags of
`nodwyn run` (`--yolo`, `--judge` and `--unattended`) and `--no-confirm`
replace it for their sessions, deciding their confirms as before. `writes`
applies to every session.

Settings changes `[approval]` (`settings.write`), which only the computer
nodwyn runs on may do (ADR 30). A device's tasks get what was chosen
there.

### 4. Soft limits do not interrupt by default
A turn's soft limits default to its hard ones: 500 tool calls, 10 million
tokens, 200 actions and 4 hours. So no checkpoint asks.

- The hard limits stop a runaway turn as before, and the progress guard
  still stops a stuck one.
- A soft limit the file leaves unset follows its hard one. Lowering a hard
  limit therefore moves the checkpoint with it.
- A person who wants checkpoints sets "ask at" lower in Settings.
- A spend limit asks at its soft limit, as before. The person sets it.

## Consequences
- A long request in a chat runs to its end unless one of these happens:
  a command needs the person (there is no judge, or it refused), a
  security confirm comes up, or a hard limit stops the turn.
- With writes free and the judge allowing commands, the model can write
  code and run it without a person looking at either. The judge decides
  each command with the task and plan in view.
  - Some fences do not depend on it: the pin, the denylist, the binding
    table and L1 on egress, taint on a command built from tool output,
    protected paths, the scrubbed environment and the hard limits.
  - `commands = "ask"` is there for a person who wants every command
    asked.
  - The exfil suite runs with writes in the folder allowed. It adds
    scenarios showing that a hook, a credential-shaped write and a write
    through a link still ask or refuse.
- In a git work tree, each write asks git (`git status`) whether the file
  is committed and unchanged. A file with uncommitted changes, an untracked
  file and an ignored one keep a version, so `.nodwyn/versions` appears in
  git repositories. Its `.gitignore` keeps it out of `git status`.
- A person's Restore is a write like any other, so it no longer asks.
- The interactive system prompt no longer says that the user approves
  every write and command.
- ADR 18's driver is unchanged as a CLI tool. Its unattended runs get
  writes in the folder too, since a standing approval was always enough
  for them (§5).

## Amendment 2026-10-03: a turn that did not finish leaves its plan to the next message

A chat is a run whose "continue" a person types (§1), but only a run's plan
outlived its turn (ADR 18, "The plan is the run's"). A chat's plan was the
turn's: the window pins the plan recorded after the latest `user.msg`, and
`plan-step` marks only that. In session `20261003T063228Z-73791682`
(meta/muse-spark-1.3 through OpenRouter) the turn "do it!" wrote a plan of
five steps with `plan-write` and marked two done. Then the provider
answered 429, three times with the retries, and the turn ended with an
error. The person typed "continue". That turn had no plan: the console's
plan panel said "none recorded for this ask", and the model began again
from `git status` and twelve reads. 47 requests later it wrote the plan
again and marked steps 1 and 2 a second time ("WT1 landed (same note as
above)").

A turn that did not finish now leaves its plan to the next message. When
its `turn.end` outcome is `error`, `stopped`, `hard_budget`, `spend_limit`
or `canceled`, or nothing ended it, `plan-gate` carries the plan into the
next turn as a run's later turn carries one: `gate.plan` decision
`carried`, with the marks made so far. The steps keep their numbers, the
window shows the marks under the plan, and `plan-step` goes on from the
next unmarked step. `(unfinished-plan)` reads that plan from the log, in
the shape `(run-plan)` gives, and the plan goes on through as many such
turns as follow. A turn that finished (`completed`, or no outcome, as in
an older log) leaves none. Its answer was given, so the next message is a
new request, gated as before. A plan whose steps are all marked is not
carried either.

The person's Stop counts as not finishing. So "stop it to steer", as the
console's composer says, keeps the plan for the correction that follows.
The model revises the plan with `plan-write` if the correction changes it.
The window's rule and its recipe are unchanged. The front-ends no longer
say "the run's plan" for a carried one.

## Amendment 2026-10-05: inspect recorded previews and kept versions

The Changes tab previously listed files without letting a person inspect their
changes. Each row now expands the latest recorded approval preview per file,
newest first, with line numbers and addition/deletion counts. It is labelled
"Approved preview—not proof of a successful write": guard decisions precede
execution. Older records without Detail explicitly say preview unavailable;
no current-file diff is invented to stand in for the recorded data.

The pane is about 320 px wide, so the inspector wraps long lines: the first
build scrolled them sideways, which hid up to 241 px of a line at desktop width
with no sign that it could be scrolled (found by looking at the screenshots,
which the browser check now asserts). An oversized diff is recognised by the
host's note being a line of its own, not by its text appearing anywhere, since
a file may mention it.

History offers a separate read-only `versions.diff` comparison from a selected
kept version to the current file. Removed lines are from the version, additions
from the current file; missing current files are deletions. Empty and oversized
comparisons are explicit. Restore remains separate. Open state survives redraws;
request identity, History identity and task generation reject stale replies.

The repository writer controls the kept index and objects; they supply content,
not authority. The daemon requires a recognised workspace/task root, restricts
paths and links to that root, applies its denylist, checks file/hash membership
and object integrity, and bounds comparison inputs. Worktree versions are read
from their parent repository's store. Comparison is available to paired readers
and does not change policy, files or session state.

## Amendment 2026-10-27: a message can wait behind a turn, or steer it

"Stop it to steer" was the only way to correct a turn that was running: stop
it, wait for it to end, type again. A message can now be sent while a turn runs
in two ways, per message. *Queue* holds it, and the session runs it as the next
turn when the running one ends, after any queued before it. *Steer* stops the
running turn, as Stop does, and sends it next, ahead of what is queued; the
turn it stops keeps its plan, as the amendment above says, so the message can
be a correction. A plain send while a turn runs is still refused, so the
choice is the person's each time, never a default that changes what Enter
does. The wire has one optional `mode` on `session.send`, `queue` or `steer`;
the console has Queue and Steer beside Send, and the TUI has Ctrl+O and
Ctrl+S. Line mode reads no line while a turn runs and is unchanged.

The queue follows ADR 40's queued model choice: the session holds it, a
model chosen while the turn ran is applied first, so a queued message runs on
the model that was chosen, and the session stays busy from one turn to the
next, so no other send slips in and no client sees it idle between them. Stop
discards the queue with the turn, so it stops everything the person had
waiting; a steer cancels the turn without emptying it. A queue is refused where
a send is: no model chosen, an attachment or a model switch under way, a run
driving the task; and an evaluation is never queued. A client shows what it
queued itself until the message's `user.msg` arrives: there is no event for
the queue. The queue is in memory, so a daemon that stops loses what waited.
