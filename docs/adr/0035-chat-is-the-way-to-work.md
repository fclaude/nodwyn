# ADR 0035: A chat is the way to work, and it asks less

- Status: accepted; implemented 2026-10-02
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
