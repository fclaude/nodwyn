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

This page records what each one can do as of 2026-10-17 (the judge choice,
ADR 47, a task's helpers, ADR 27, a task without turn limits, ADR 49,
retrieval, ADR 50, and the console as a phone's app, ADR 52, included), so
work on a
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
| Resume a stored session | yes, the sidebar | yes, the chooser (paged, typing filters it) or `--resume` | yes, `--resume` | — |
| Switch between open tasks | yes, the sidebar; several open at once | one session per process; quit and choose again | one session | — |
| Tasks needing an answer, across tasks | yes, Home (`attention.list`) | not built | — | — |
| Told when a task needs an answer while looking elsewhere | yes, Settings › This browser › Notifications; on a phone through the console's service worker (ADR 52) | not built | — | — |
| Installed on a phone's Home Screen (ADR 52) | yes, over https: the console is a web app | deliberate gap: a terminal client | — | — |
| Create and manage schedules (ADR 49) | yes, Home’s **Options › Schedule…** and a task’s **Schedule…** (in its **⋯** menu); Home’s cards pause and run now; paired devices can pause or run now | not built | not built | not built |
| Name a task | yes (`session.rename`) | not built; lists first messages | — | `nodwyn sessions` lists first messages |
| Archive, delete (ADR 39) | yes; delete only on this computer | not built | — | — |
| Quit while a turn runs | the task goes on in the daemon | daemon mode detaches and the turn goes on; local mode aborts it | aborts it | — |

## The conversation

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| Jump from a search match to bounded, read-only turn history (`log.context`) | yes; load earlier/later within that turn, or open the live task | deliberate gap: replays whole history | replays whole | `memory recall` reads an event and the turn’s card |
| Open at the latest run; page back through collapsed earlier runs (ADR 49) | yes; earlier contents load on expansion | deliberate gap: replays the conversation whole | replays whole | `replay` reads whole |
| Send a message, abort a turn | yes | yes; Ctrl+C aborts, and clears an unsent draft before it quits | yes | — |
| `/compact`, `/optimize` | yes | yes | yes | — |
| Evaluate Scheme (`session.eval`) | deliberate gap: the console never offers it (protocol.md) | yes, `:eval` | yes, `:eval` (recorded as `eval.req` and `eval.result`, as in the TUI) | `nodwyn eval`, `nodwyn repl` |
| Thinking | inline, folded | `:thinking` toggles it | `--show-thinking` | `--show-thinking replay` |
| Tool calls | grouped into one Activity row per stretch between replies | one line per call; `:details` expands them | a card per call | — |
| Retrieval calls said in words (`index-search` and where it searched, `recall` and its seq, `index-update`; ADR 50) | yes, folded as looks (`searched 3 times · recalled 1 event · updated the index`) | yes | yes | `replay` |
| An index build or refresh (`index.update`) | a line under the call that made it | a line under the call | a line | `replay` |
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
| Don't ask (`session.approve_all`) | yes, when Settings offers it; a paired device sees it while on and turns it off | not built | `--yolo` at start | `nodwyn run --yolo` |
| A task without turn limits (`limits`, `session.limits.set`, ADR 49) | yes: Home's **Limits** and the task's, on this computer | read: `:budget` says "no limit", and the conversation each change; not built: choosing it | not built: `--budget-*` set limits, none removes them | `nodwyn run`: not built, its run limits are its own |
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

## Changing a task while a turn runs

A task can be changed while a turn runs in four ways, set once here (the
checks that keep a turn, an attachment and a change from overlapping are one
function, `Session.busyWith`, in `internal/core/session.go`):

| Change | While a turn runs |
|---|---|
| Move to another model | queued, applied when the turn ends (ADR 40) |
| Move to another judge, or none | refused: "choose its judge when this turn ends" |
| Attach a file | refused until the turn ends |
| Turn limits on or off | applies from the next turn |
| Don't ask | applies at once |

The model queues and the judge refuses on purpose, for now: the queue and
its "will apply when the turn ends" state were built for the model (ADR 40)
and no client shows one for the judge. Making the judge queue is a change in
every client's picker, not in the core's guard.

## The judge (ADR 46, ADR 47)

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| See the task's judge, and the judges set up | yes, the picker's Judge select | yes, `:judge` | — | — |
| Choose a task's judge as it opens | yes, New task's picker | not built: `[judge]`'s; use `:judge` before the first message | `[judge]`'s | `nodwyn run`: `[judge]`'s |
| Move a task to another judge, or none | yes, between turns | yes, `:judge NAME`, `:judge none` | not built | — |
| Set a judge up: Jev's key, an OpenAI API key | Settings › Judge, in place | — | — | edit `config.toml`, `:eval (credential-register …)` |
| The default judge | Settings › Judge (Models links to it) | — | — | `[judge] server` |
| Choosing from a paired device | refused (ADR 30, ADR 47) | — | — | — |

## Memory (ADR 51)

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| The model sees what it found under each turn's card, and recalls a turn's message with the card and its events (`index-search`, `recall`) | yes, as any tool's result | yes | yes | `nodwyn run`: yes |
| A person searches this conversation or earlier tasks | yes, the sidebar's search box (**Search conversations for "…"**) or a task's **Search**; task/turn groups, matches in words with the query marked, and links into recorded history | `:eval (index-search "…" "earlier")`, with `:earlier on` | `:eval`, likewise | `nodwyn memory search`, `nodwyn memory recall`, whatever the switch |

## Files and changes

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| A worktree's changes since its base (`worktree.diff`) | yes, Review in the centre panel | deliberate gap: `git diff` in the worktree (ADR 34, amended 2026-10-08) | — | `git diff` in the worktree |
| Line comments sent as one message (`worktree.review`) | yes, Review in the centre panel | deliberate gap: write the message | — | — |
| Kept versions of a file, restore (`versions.*`, `session.restore`) | yes, Changes | not built | — | `nodwyn versions`, `nodwyn undo` |
| Recorded write approvals, attachments list | yes, Changes | the conversation only | the conversation only | `replay` |
| Project notes (`notes.read`, `session.notes.write`) | yes, Workspace | not built | — | — |
| The repository index: what it holds, and forgetting it (`index.status`, `index.forget`; ADR 50) | yes, Workspaces (a folder's line and **Forget the index**) and a task's Workspace tab | yes, `:index`, `:index forget` | — | delete `projects/<key>/index` in the data directory |

## The model's terminal (ADR 48, experimental and temporary)

The terminal tools are the model's (`term-open` and five more, offered only
with `[experimental] terminal = true` in `config.toml`), so every client
that runs a session has them; none of the clients shows a terminal. It is
temporary: a row here goes with the feature if it is removed.

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| Turn it on | edit `config.toml`; Settings does not offer it (deliberate: a switch would suggest a feature) | same | same | same; `nodwyn run` too |
| The opening prompt, saying it is experimental | yes, as any command's | yes | yes | `nodwyn run` follows its flags |
| The session's note saying it is experimental | yes, in the transcript | yes | yes | — |
| The model's calls and the screens it read | as tool calls and results, like any tool | same | same | `replay` |
| A live view of the terminal for the person | not built | not built | — | — |

## Inspection

The console's **Details** (Workspace tab) has a view for each TUI inspector
but `:replay`. Both read the same log.

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| Log records filtered by kind | Details › Log (latest run and inherited setup; earlier runs expand in Conversation) | `:log [n] [prefix]` | `:log [n]` | `replay` |
| Plan record, step marks | Progress tab, Details › Plan record | `:plan` | — | — |
| Indexed whole-task totals and turn/run counts (`session.totals`, ADR 49) | yes, Statistics and Changes, including closed tasks | deliberate gap: its inspectors read the log | — | — |
| Budget, spend, checkpoints | Statistics tab, Details › Budget | `:budget` | — | `replay` shows the records |
| Live gauges (spend, time, context, cache hit, 90% warnings) | yes, the summary above the tabs; session totals from `session.totals` | the context share in the title | — | — |
| Tools offered, with schemas | Details › Tools | `:tools [name]` | `:tools` | — |
| Bindings and approvals in effect | Details › Policy | `:bindings`, `:allow` | `:allow` | — |
| `docs/friction.md`, guarded (nodwyn's own development log: a developer command, not offered to every project) | deliberate gap: no Details entry, since only nodwyn's repository has the file | `:friction` | — | — |
| A transcript replayed from the log | — (the conversation is it) | `:replay` | — | `nodwyn replay` |

## Setup and the computer (ADR 30)

All deliberate gaps outside the console. They are forms over `config.toml`,
the keychain and the config directory, and the CLI covers what a terminal
needs.

| | Console | TUI | Line mode | CLI |
|---|---|---|---|---|
| Providers, models, efforts, judges, limits, timeouts | Settings: Models, Judge, and General (limits, timeouts and the context window under Advanced); one Save bar for the settings | — | — | edit `config.toml` |
| MCP servers: add, inspect, approve, pull | Connections › Tools | — | — | `nodwyn mcp` |
| Credentials: add, replace, allow a host, take a host back, remove; OAuth2 sign-in | Connections › Credentials (a ChatGPT plan is connected on its provider, Settings › Models) | — | — | `nodwyn credential` (taking a host back is an edit of `bindings.toml`) |
| Keychain: create, unlock, passphrase | Settings › Keys & devices, and beside a provider form that needs it | unlocks at start | unlocks at start | `nodwyn keychain` |
| Named workspaces and mounts | Workspaces | — | — | edit `workspaces.toml` |
| Let the model search a folder's earlier tasks (ADR 50) | Workspaces, the folder's editor; a task's Workspace tab says which | yes, `:earlier`, `:earlier on`, `:earlier off` | — | edit `workspaces.toml` (`earlier_tasks = true`) |
| Project trust (ADR 45) | deliberate gap: no grant button or remote grant (browser.md, "Project startup") | — | — | `nodwyn trust` |
| Pair devices | no: `nodwyn ui --listen` prints the pairing code in its terminal (Settings › Keys & devices says so) | — | — | `nodwyn ui --listen` |
| List and revoke devices | yes, on this computer, Revoke after a confirm | — | — | `nodwyn devices`, `nodwyn devices revoke NAME` |

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

- The TUI's local mode makes worktrees without the daemon's task-root
  refusal, as it opens any `--repo` without it. The refusal moving into core
  is still not built (ADR 34, amended 2026-10-08).
- The console reads whole-task totals from the daemon's log index; its
  current-turn gauges still read the loaded conversation. The TUI computes
  its own in `gauge.go` and `plan_budget.go`.
