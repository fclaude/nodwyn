# Clients: what each front-end does

nodwyn has four ways in, over one core:

- **The console**: the browser view, also shown by `nodwyn app` and
  `nodwyn ui` (`internal/frontend/web`, [browser.md](browser.md),
  [app.md](app.md)). It always talks to a daemon, through the bridge in
  `internal/frontend/web/server.go`.
- **The TUI**: `nodwyn tui` (`internal/frontend/tui`, [tui.md](tui.md)).
  It either owns a session in its own process (local mode) or attaches to a
  daemon with `--socket` (daemon mode). Every operation has both
  implementations: `backend.go` and `remote.go`, and in `inspect_backend.go`
  and `models.go`.
- **Line mode**: `nodwyn` or `nodwyn chat` (`internal/frontend/plain`). It
  owns its session and is scriptable.
- **The CLI**: the other subcommands in `cmd/nodwyn/cli.go` (`sessions`,
  `replay`, `versions`, `undo`, `credential`, `keychain`, `mcp`, `trust`,
  `devices`, `run`, `summary`, `eval`, `repl`).

This page records what each one can do as of 2026-10-09 (the judge choice,
ADR 47, and a task's helpers, ADR 27, included), so work on a
client starts from it instead of a fresh survey of the code. It does not
replace the per-client docs. A change that gives one client something
updates its row here in the same commit, and a gap is either **not built**,
saying where that is recorded, or **deliberate**, saying why.

In the tables, **yes** means built and tested. **—** means absent: the
notes say whether that is deliberate or simply not built. **read** means
it can see the thing but not change it.

## Tasks and sessions

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| Start a task in a folder | yes, Home's ask bar | yes, the chooser's row or `--repo` | yes, `--repo` | — |
| Start in a git worktree (ADR 34) | yes, the default for a git workspace | yes, the chooser's first row in a repository's top folder; `--worktree` | not built (ADR 34) | `nodwyn run`: not built |
| The worktree's branch shown | yes, the header and sidebar | yes, the title and the chooser | — | — |
| Resume a stored session | yes, the sidebar | yes, the chooser or `--resume` | yes, `--resume` | — |
| Switch between open tasks | yes, the sidebar; several open at once | one session per process; quit and choose again | one session | — |
| Tasks needing an answer, across tasks | yes, Home (`attention.list`) | not built | — | — |
| Name a task | yes (`session.rename`) | not built; lists first messages | — | `nodwyn sessions` lists first messages |
| Archive, delete (ADR 39) | yes; delete only on this computer | not built | — | — |
| Quit while a turn runs | the task goes on in the daemon | daemon mode detaches and the turn goes on; local mode aborts it | aborts it | — |

## The conversation

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| Send a message, abort a turn | yes | yes; Ctrl+C aborts | yes | — |
| `/compact`, `/optimize` | yes | yes | yes | — |
| Evaluate Scheme (`session.eval`) | deliberate gap: the console never offers it (protocol.md) | yes, `:eval` | yes, `:eval` | `nodwyn eval`, `nodwyn repl` |
| Thinking | inline, folded | `:thinking` toggles it | `--show-thinking` | `replay --show-thinking` |
| Tool calls | grouped into one Activity row per stretch between replies | one line per call; `:details` expands them | a card per call | — |
| Usage per reply (tokens, cache share, cost) | yes | yes | yes | — |
| Rendered markdown | yes | — (deliberate: plain text in a terminal) | — | — |
| Attach a file (ADR 22) | yes, the composer's paperclip (staged upload) | yes, `:attach PATH` | not built | — |
| An image's confirm | yes, inline | yes, as any prompt | not built | — |

## Approvals and policy

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| Answer a confirm once, for this turn, or for the session | yes | yes: `y`, `n`, `t`, `a` | yes | — |
| Save a binding (`session.binding.create`) | yes, on this computer | yes, `b` | yes | `nodwyn credential` |
| Masked secret prompts | yes | yes | yes | — |
| Budget checkpoints | yes | yes | yes | — |
| Don't ask (`session.approve_all`) | yes, when Settings offers it | not built | `--yolo` at start | `nodwyn run --yolo` |
| Edit `allow.toml` (`session.policy.write`) | yes, the policy editor | deliberate gap: `:bindings` reads only | — | — |

## Models (ADR 40, ADR 41)

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| See the model and its efforts | yes, the picker | yes, `:model` | the banner names the model | — |
| Move the conversation to a configured model | yes | yes, `:model NAME [EFFORT]` | not built | — |
| Choose a model as a task opens | yes, New task's picker | not built: it opens on `[control]`; use `:model` before the first message | `--model`/`--url` override `[control]` | `nodwyn run`: not built |
| A model on a server with no `[models.NAME]` entry (`server` + `model`) | not offered: the picker lists names | not offered | — | — |
| A choice while a turn runs is queued | yes | yes, and the conversation says so | — | — |
| Choosing from a paired device | refused (ADR 30) | — | — | — |
| Edit a model's labels (ADR 27, amended 2026-10-09) | yes, Settings › Models; reaches open tasks | not built | — | edit `config.toml` |
| Let a task's model hand work to other models (`delegates`) | yes, New task's Helpers fold, this computer only | not built: a task opens with none | none | `nodwyn run`: none |

## The judge (ADR 46, ADR 47)

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| See the task's judge, and the judges set up | yes, the picker's Judge select | yes, `:judge` | — | — |
| Choose a task's judge as it opens | yes, New task's picker | not built: `[judge]`'s; use `:judge` before the first message | `[judge]`'s | `nodwyn run`: `[judge]`'s |
| Move a task to another judge, or none | yes, between turns | yes, `:judge NAME`, `:judge none` | not built | — |
| Set a judge up: Jev's key, an OpenAI API key | Settings › Judge, in place | — | — | edit `config.toml`, `:eval (credential-register …)` |
| The default judge | Settings › Judge, or beside the default model under Models | — | — | `[judge] server` |
| Choosing from a paired device | refused (ADR 30, ADR 47) | — | — | — |

## Files and changes

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| A worktree's changes since its base (`worktree.diff`) | yes, Review in the centre panel | deliberate gap: `git diff` in the worktree (ADR 34, amended 2026-10-08) | — | `git diff` in the worktree |
| Line comments sent as one message (`worktree.review`) | yes, Review in the centre panel | deliberate gap: write the message | — | — |
| Kept versions of a file, restore (`versions.*`, `session.restore`) | yes, Changes | not built | — | `nodwyn versions`, `nodwyn undo` |
| Recorded write approvals, attachments list | yes, Changes | the conversation only | the conversation only | `replay` |
| Project notes (`notes.read`, `session.notes.write`) | yes, Workspace | not built | — | — |

## Inspection

The console's **Details** (Workspace tab) has a view for each TUI inspector
but `:replay`. Both read the same log.

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| Log records filtered by kind | Details › Log | `:log [n] [prefix]` | `:log [n]` | `replay` |
| Plan record, step marks | Progress tab, Details › Plan record | `:plan` | — | — |
| Budget, spend, checkpoints | Statistics tab, Details › Budget | `:budget` | — | `replay` shows the records |
| Live gauges (spend, time, context, cache hit, 90% warnings) | yes, the summary above the tabs | the context share in the title | — | — |
| Tools offered, with schemas | Details › Tools | `:tools [name]` | `:tools` | — |
| Bindings and approvals in effect | Details › Policy | `:bindings`, `:allow` | `:allow` | — |
| `docs/friction.md`, guarded | Details › Friction | `:friction` | — | — |
| A transcript replayed from the log | — (the conversation is it) | `:replay` | — | `nodwyn replay` |

## Setup and the computer (ADR 30)

All deliberate gaps outside the console. They are forms over `config.toml`,
the keychain and the config directory, and the CLI covers what a terminal
needs.

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| Providers, models, efforts, judges, limits, timeouts | Settings, one tab each (General, Models, Judge, Context) | — | — | edit `config.toml` |
| MCP servers: add, inspect, approve, pull | Connections › Tools | — | — | `nodwyn mcp` |
| Credentials, OAuth2 sign-in | Connections › Credentials | — | — | `nodwyn credential` |
| Keychain: create, unlock, passphrase | Settings › Keys & devices, and beside a provider form that needs it | unlocks at start | unlocks at start | `nodwyn keychain` |
| Named workspaces and mounts | Workspaces | — | — | edit `workspaces.toml` |
| Project trust (ADR 45) | deliberate gap: no grant button or remote grant (browser.md, "Project startup") | — | — | `nodwyn trust` |
| Pair and revoke devices | yes, on this computer | — | — | `nodwyn devices` |

## Runs (ADR 35)

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| Start, stop, resume a run | shows a run and **Stop** aborts it; starts none (ADR 35) | follows a run in daemon mode and answers its checkpoint | — | `nodwyn run` |

## Not built, in the order proposed

1. Attention across tasks in the TUI, with a session switcher, so a task
   waiting on an approval is not invisible there.
2. `:rename` and `:archive` in the TUI, and titles in its chooser.
3. Kept versions and restore in the TUI.
4. A model at open in the TUI (`--model NAME`), and `--worktree` for line
   mode and `nodwyn run`, with `--worktree-base` (ADR 34).
5. A test that every RPC method is either used by the TUI or named here as a
   deliberate gap, so a new console feature cannot widen the difference
   unnoticed.

## Found while writing this

- [protocol.md](protocol.md) said the console starts runs from Home with
  `run.start`. It does not (ADR 35, [browser.md](browser.md), "Runs"), though
  the bridge still lets the method through; protocol.md says so now.
- The TUI's local mode makes worktrees without the daemon's task-root
  refusal, as it opens any `--repo` without it. The refusal moving into core
  is still not built (ADR 34, amended 2026-10-08).
- The console computes its gauges in JavaScript, and the TUI computes its
  own in `gauge.go` and `plan_budget.go`. Figures with one definition, such
  as cache hit, can drift between them.
