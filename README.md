# nodwyn

A minimal, self-extending coding-agent harness: a Go host, a small Scheme
embedded in it for orchestration, one guard at the only Scheme→Go exit, and
a binding table as the sole authority over where a secret may go.

- [SPEC.md](SPEC.md) — the architecture and its invariants
- [plan.md](plan.md) — what is built next, in order, and why
- [docs/manual.md](docs/manual.md) — how to read the code and how to use nodwyn
- [Books on Gitea Releases](http://git.example.com/fc/nodwyn/releases) — the design book (`nodwyn.pdf`) and *Becoming a nodwyn Expert* (`expert.pdf`), with the complete Scheme sources and a Go source companion (`make book` and `make expert` rebuild them locally)
- [docs/adr/](docs/adr/) — decision records
- [docs/scheme.md](docs/scheme.md) — the language reference
- [docs/friction.md](docs/friction.md) — where nodwyn got in the way of building nodwyn

See [Security boundaries and user choices](docs/security.md) for project trust,
credential transport and migration, and [the maintenance review](docs/maintenance-review.md)
for a behaviour-preserving simplification plan.

## Maintainer map

The packages and what to read first in each are the package map in
[the manual](docs/manual.md#package-map), kept there once. Their
responsibilities, which stay useful as nodwyn gains more integrations and
front-ends:

| Packages | Responsibility |
|---|---|
| `core` | Own a session, assemble dependencies, coordinate its lifetime and prompts. |
| `budget` | Account for one turn, enforce ceilings, and request additional soft allowance. |
| `loop` | Provide the primitives and default Scheme for a turn: the strategy, the plan gate, prompts, compaction, sub-agents, working habits. |
| `event`, `log` | Describe front-end events; persist the history from which context and replay are derived. |
| `keychain`, `binding` | Hold encrypted credentials, acquire and refresh OAuth2 tokens, and inject them; authorize their destinations, methods, positions and scopes. |
| `policyfile` | Lock and atomically publish policy-file changes after the caller validates and authorizes them; never interpret policy or grant permission. |
| `scheme`, `guard`, `prim` | Evaluate extensions; authorize host operations; implement those operations. |
| `llm`, `window`, `redact`, `detect/l1` | Talk to models (Ollama, OpenAI-compatible, Anthropic) and price their requests; derive their context; redact text; detect credential shapes. |
| `memory`, `retrieval` | Make a task's history searchable by words and keep its per-turn cards, as views of the log that the log never records. |
| `schedule`, `catalogue` | Compute scheduled tasks' civil-time rules and say them in words; hold what providers publish about their models. |
| `watch`, `workflow` | Read what a workflow's source has now (a forge's pull requests, a Gmail search, a command's lines) through the credential check a task's requests meet; keep a workflow's log, its held answers and what it has seen. |
| `startup` | Write the login item that starts the daemon, only when asked. |
| `termemu`, `frontend/termvt` | Give the terminal primitives an emulator behind an interface, with the Charm library only under `frontend`. |
| `mcp`, `extract` | Talk to remote MCP servers (pinned manifests, bound keys); turn attached documents into redacted text, check attached images by their content, and read all a document carries before it is sent. |
| `frontend/workflows` | Read the daemon's workflows and held answers, say them in words and act on them, for the command line and the TUI alike. |
| `frontend/run` | Chain planned turns under a finite deadline and turn count; ask a person to continue, or, unattended, go on only after a turn that marked a plan step done. |
| `frontend/plain`, `frontend/tui`, `frontend/web`, `frontend/rpc`, `config`, `cmd/nodwyn` | Render and answer events; own local RPC connections and sessions; load settings; wire the application together. |

**A user message:** the front-end calls `Session.Send`. The session records
and redacts the message, and calls the Scheme procedure `nodwyn-turn`, which
plans first when the turn must (a run's turns must) and then applies
`agent-turn`. The default `agent-turn` runs the plan gate, then asks the
model, runs the tools it requested through Scheme and repeats until the
model answers without tools; a project may redefine either in
`.nodwyn/init.scm`. The loop's primitives build each window from
recorded events and record every result before the next model request. The
front-end consumes events from the session.

**A credential-bearing tool call:** Scheme applies a primitive; the guard
reads its metadata and arguments and checks the binding and other policies.
Only an allowed call reaches the handler. The handler builds the request and
asks the shared credential service to inject the opaque handle's secret at
the authorized position. Secret values stay out of Scheme and the log. Jev
uses this same path, including when called by the plan gate. The harness's
control-plane model call is the deliberate exception described in the spec.

**Adding a built-in integration:** declare its settings in `config`, then
register its handlers in `prim` with explicit egress/irreversible metadata
and credential, destination and payload roles. Supply its settings during
core construction and use host interfaces for credential injection,
transport, redaction and recording, as Jev does. Declare fixed request scope
with `EgressHost`, `EgressMethod` and `EgressPosition` when the primitive has no
corresponding role parameters, and `EgressClass` when Go supplies the
credential (as for `google-doc-upload` and the judge tools). Do not open the keychain or
read secrets in the integration. Test it through Scheme and the guard with
a local fake service, bound/unbound destinations and canary credentials.
Integration-specific settings should not become getters on `Session`.

The roadmap extends these boundaries: ADR 21 uses events for RPC clients;
ADRs 22–23 bring MCP tools through the primitive/guard path; ADR 24 extends
the credential lifecycle to OAuth. Local RPC, workspaces, attachments (text
and images), MCP servers (remote, and local ones contained by podman or
docker), OAuth2 credentials and documents out (pandoc, Google Docs) are
implemented. Keep `loop` and `event` separate, and keep credential
management shared across integrations.

## Build

```
make build      # bin/nodwyn, and bin/nodwyn-egress (the proxy of contained MCP servers)
make build-webview  # bin/nodwyn whose `nodwyn app` opens its own window (docs/app.md)
make test       # go test -race ./...
make lint       # vet, staticcheck, boundary and ingress lints
make accept     # everything a candidate must pass before promotion
make smoke      # manual two-turn session against the real control plane
make selfcheck  # the candidate proves itself (ADR 13)
make promote    # accept + selfcheck, then ~/.local/bin/nodwyn-stable and nodwyn-egress (webview build; version = commit)
```

Requires Go 1.26 or newer (ADR 33). `make lint` builds its `staticcheck`
from the module (`go.mod`'s `tool`), so nothing else needs installing.

The Scheme interpreter is a separate, public module,
`github.com/fclaude/brote`, which Go fetches like any dependency; no
`GOPRIVATE` or git configuration is needed.

Text search (`index-grep`) uses the shared guard and produces session-scoped
results. The model also searches by words (`index-search`): the
repository, from an index of the files git lists that nodwyn keeps in its
data directory and refreshes as files change (`index-update`), and its own
conversation, bringing an earlier event back whole (`recall`), elided and
folded results included, from the log; and, when the person turns it on for
a project, its earlier tasks. See
[memory and retrieval](docs/memory.md) for their limits.

## Run

```
bin/nodwyn daemon               # local sessions, turns and event delivery
bin/nodwyn tui                  # opt-in full-screen terminal client
bin/nodwyn app                  # the whole thing in one program: a window, and a daemon if none runs
bin/nodwyn ui                   # the browser console for a running daemon
bin/nodwyn ui --listen IP:7777  # also for other devices, which pair with `nodwyn devices invite`
make app                      # bin/Nodwyn.app for macOS (to share with people who never open a terminal)
bin/nodwyn                      # chat in the current repository (line mode, over the daemon)
bin/nodwyn --repo ~/src/x       # pin to another repository
bin/nodwyn --show-thinking      # show the model's redacted thinking too
# Exact request replay (new logs with recorded context):
# bin/nodwyn replay SESSION_ID --window REQUEST_SEQ
bin/nodwyn --no-confirm         # non-interactive: every confirm is a no
bin/nodwyn replay <session-id>  # print a session log as a transcript
bin/nodwyn sessions             # id, last activity, state, repo, first ask
bin/nodwyn summary SESSION      # inspect user-authored, revocable summaries
bin/nodwyn sessions --ids       # IDs only (scripts)
bin/nodwyn --resume <session-id> # continue in the recorded repository
bin/nodwyn trust --repo ~/src/x  # review project startup code and approvals
# Then: nodwyn trust --repo ~/src/x --accept REVISION_FROM_THE_REVIEW
bin/nodwyn repl                 # Scheme REPL
bin/nodwyn credential add --oauth2 google --client-id ID  # an OAuth2 credential, authorized in the browser (ADR 24)
bin/nodwyn credential           # list credentials, never a secret
```

The control model is the `[control]` table in `~/.config/nodwyn/config.toml`:
an Ollama server, or any chat-completions server with a key bound in
`bindings.toml`. There is no built-in default; when it is missing, nodwyn says
what to add. Model requests honor
`HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY`, so a sandbox whose only egress is
an allowlist proxy can still reach the model. What asks you is
`[approval]` (ADR 35). By default a write inside the repository goes ahead,
its diff logged and its previous content kept for undo, and a command no
standing approval covers (command prefixes in trusted `.nodwyn/allow.toml`, ADR 17)
goes to the judge first, then to you when it refuses or there is no judge.
Standing approvals also check arguments: flags that select programs, output
files or another working directory, make variable assignments, unknown flags,
and absolute or repository-escaping paths cannot inherit one. When a prefix
would otherwise cover such a command, a security confirm asks you. Approved
builds and tests still execute project code, write files and may use the network;
these checks do not sandbox the process. See [executable approvals](docs/security.md#executable-approvals).
A protected path, content that looks like a credential and a command built
from tool output always ask. Writes to the session's scratch directory,
outside the repository, ask only when they need a security confirm (ADR 28).
Session logs live in `~/.local/share/nodwyn/sessions/`.
Resume restores the conversation and appends to that same log. It reloads current
configuration, bindings, and trusted repository approvals and `init.scm`,
so its prompts and tools as they are now apply, which `(reload)` leaves fixed
until then; requests made before still replay under the recipe they used.
Session-only approvals and interactive Scheme definitions are reset. An explicit
`--repo` must match the recorded repository. Only one process may append to a
session at a time. Interrupted tool calls are never rerun automatically: a call
without a durable result is marked "outcome unknown" so the next turn can inspect
current state before retrying. `sessions` reports an unfinished turn as
`interrupted`, not as proof that another process is running. Missing final log
lines are repaired on resume; corrupt complete lines are rejected.

Repository startup code and approvals are skipped until you review and accept
their revision with `nodwyn trust`. Trust is stored outside the repository; changing
either file requires review again. Review any code the init loads too. Revocation
requires restarting existing tasks to discard their loaded code and grants.

Credentials require HTTPS except on loopback by default. For a remote HTTP service
you deliberately accept, set `[policy] allow_insecure_http = true` in the user's
config.toml, or **Settings > Approvals > Credentials over remote HTTP**. The switch
is off by default and applies to new/resumed tasks; it preserves binding checks,
redirect refusals and certificate verification. See [the security reference](docs/security.md).

The guarded file tools pin paths to the
repository; credential-shaped files are unreadable, network tools on a
command line need confirmation, and child processes get a scrubbed
environment. Redaction removes recognized credential shapes from user messages,
tool results, logs, and frontend events. Responses are buffered until complete
so split stream chunks cannot expose a key. Unknown or encoded secrets are
not guaranteed to be detected; a classifier for them (the optional L3) is
not built. Approved commands run with the
user account's privileges; these checks do not sandbox an approved executable.

Gitea runs `.gitea/workflows/ci.yml` on every push and pull request using the
isolated `self-hosted` runner. It executes `make accept` with fresh race tests,
then cross-compiles Linux and macOS binaries for amd64 and arm64 with cgo
disabled. Only the Linux amd64 binary is executed on that runner. Its selfcheck
uses a fake model, so no paid model or real model credentials are needed.
Pushes never publish: a release is a manual workflow run on `main` with a
version. [Releases](docs/releases.md) says what each one holds, how to check
its digest and how a run publishes. Publishing is separate from local
`nodwyn-stable` promotion.

Turns also stop when the model repeats an identical tool call four times in a
row, retries a denied call three times, gets ten consecutive tool errors, or
fails with one tool on one file in six of its last sixteen responses
(ADR 18). The reason is shown and logged. Any remaining calls in that response
are marked not executed; a new user message starts a fresh turn. Budgets and checkpoint prompts are implemented, and so is the bounded run driver (`nodwyn run`, below).

Turn limits live under `[budget]` in `config.toml`:

```toml
[budget]
tool_calls_hard = 500
wall_hard = "4h"
tokens_hard = 10000000
actions_hard = 200
# tool_calls_soft, wall_soft, tokens_soft, actions_soft: unset, each is its
# hard limit, so no checkpoint asks (ADR 35). Set one lower to be asked there.
```

The token limit counts each model request's prompt and reply, a prompt token
the provider read from its cache as a tenth of one; the console calls it
*Tokens used*.

At a checkpoint, `y` doubles the soft limit up to its hard ceiling, `n` stops,
and an absolute number (or a duration such as `40m` for wall time) chooses a
larger soft limit. A completed answer does not ask for more soft allowance. This never approves a tool or raises a hard ceiling.
`--no-confirm` stops at checkpoints. Each answer, including cancellation, is a
redacted `turn.checkpoint` log record. A new turn starts with fresh accounting.

A task stops at these limits wherever it is opened: line mode, the TUI and the
console all open tasks in the daemon, so line mode no longer takes limits for
one invocation (`--max-tool-calls`, `--budget-*` are refused, saying which
`[budget]` key to set). `nodwyn run` has a run's own `--budget-wall` and
`--budget-cost`. Explicit legacy
`loop.max_tool_calls` settings still work; do not also set
`budget.tool_calls_hard` in that file. Configurations without a legacy override
get the ADR 18 default of 500 hard tool calls.

Actions count authorized irreversible host attempts, including every nested
write or command inside a Scheme tool. Denials consume no action allowance.
The checkpoint lists attempted file paths and commands. Wall time includes
planning and approval waits; its hard deadline cancels blocked work, while soft
wall checkpoints occur at operation boundaries. Tokens are the prompt plus
output usage each model request reports, the control model's and any other
configured model's: a response can overshoot a token ceiling, but no
subsequent operation starts after that usage is observed. Judge requests are
not model requests and are not counted against tokens; their usage is
recorded and shown, and their cost, priced by `[judge] price_input` and
`price_output`, counts against a spend limit.

After each reply the front-ends show its tokens, the share read from the
provider's prompt cache and its cost, each only when known; a model's
`price_input`, `price_cached_input` and `price_output` (USD per million
tokens) estimate the cost when the provider does not report it. The TUI's
`:budget` shows the latest turn's spend, and a checkpoint what the turn has
cost so far.

A spend limit is yours to set, and off unless you do:

```toml
[budget]
cost_soft = 2.0    # US dollars: past this a checkpoint asks
cost_hard = 10.0   # the task stops here; cost_soft unset is cost_hard
```

It counts what the whole task's model requests cost, not one turn's: every
turn of a session or a run, a resumed session's history included. Past
`cost_soft` a checkpoint asks (answer in dollars, `2.50`), and a raised soft
limit holds for later turns. At `cost_hard` the turn stops (`turn.end`
outcome `spend_limit`), and no later turn starts: a run ends `limited`, and
a new message stops before it reaches the model. Raise `cost_hard` to go on.
It counts only known costs, reported by the provider or priced from the
model's `price_*`. A request with neither is not counted, and the first
turn that has one says so in a note. `nodwyn run --budget-cost USD` sets the
hard limit for one run (kept on resume).
An unattended run's soft checkpoint goes on to the hard limit, as for the
other budgets.

The default loop has two profiles. A session nobody approves (`--yolo`,
`--judge`, `--unattended`, a run's `auto_approve`) runs the lean one: a short
system prompt, `shell-exec` and `fs-edit` alone, no explorer or planner, and
nothing said to the model while it works. A session you answer runs the full one.

```toml
[loop]
profile = "auto"   # or "lean" or "full" for every session; NODWYN_PROFILE overrides it
```

See [the manual](docs/manual.md#unattended-runs) and `docs/scheme.md` §9.8.

### Local daemon

`nodwyn daemon` serves a versioned local session protocol over a private Unix
socket. Clients can manage sessions, send and abort turns, subscribe to events,
and page through durable history after reconnecting. Sessions belong to the
daemon and survive client disconnects; shutting down the daemon releases their
logs for later resume. A slow subscriber is disconnected without blocking turns.
The existing plain frontend stays usable.

Clients can answer approvals and masked credential prompts, explicitly grant
standing approval for a pending operation, and evaluate user Scheme. Replies
carry the kernel-provided Unix peer identity; subscriptions grant no authority.
Credential values stay in the shared host/keychain path and never enter Scheme.
The generated wire schema is checked for drift in the normal test suite.
See [the protocol guide](docs/protocol.md) for startup, methods, catch-up cursors,
queue limits and ownership.


### Terminal client

`nodwyn tui --repo /path/to/project` starts the full-screen client locally;
`nodwyn tui --socket /path/to/sock` attaches to a daemon. Choose or resume a
session, follow the conversation, inspect scrollable approval diffs, and reply
to budget or masked credential prompts. Tool calls group their arguments,
recorded guard decisions and full results or errors. Ctrl+C aborts busy work;
Ctrl+Q quits.
Quitting a daemon client detaches without stopping its session; Ctrl+R reconnects
and recovers history without resending operations. Plain mode remains the default.
Use `:tools` to list model-facing tools, or `:tools NAME` for an input schema.
`:plan` shows the latest message’s recorded planning outcome and the steps the
model marked done or skipped; `:budget` shows configured limits, the latest
turn’s checkpoint snapshots and its spend.
Use `:log`, `:bindings` (also `:allow`), `:replay` and `:friction` for read-only inspection;
Esc returns to the conversation and `r` refreshes a snapshot.
`:model` shows the conversation's model and the choices, and `:model NAME
[EFFORT]` moves it from its next turn; `:attach PATH` adds a file. In a git
repository the chooser offers a session in a worktree of its own first, and
`--worktree` opens one directly.
Enter sends the whole draft; Alt+Enter or Ctrl+J adds a newline. Bracketed paste
preserves line breaks without sending. Prompts keep their own single-line input.
See [the TUI guide](docs/tui.md) for controls, ownership and the acceptance coverage,
and [the clients](docs/clients.md) for what each front-end can do.

See [session memory](docs/memory.md) for exact request replay, summary creation
and revocation, and retrieval.

### The console (ADR 21, ADR 30)

The browser console is organized around tasks: Home starts one in a folder
and shows what needs an answer, what runs and what finished; a task shows
its conversation with the approval inline (the diff, and Y/T/A/N), and beside
it the plan with the steps the model marked, this turn's budget, changes
with their history, attachments (documents and images),
project notes and the workspace. Connections adds and reviews MCP servers
and manages credentials (keys, OAuth2 accounts, and where each may go);
Workspaces names folders and their mounted folders; Settings, in tabs, sets what a task does without asking you (writes
in its folder, and who answers a command: the judge, then you, by default),
the limits, the providers with their models, the judges (Jev or an OpenAI
API key, set up with the key alone) and the context window, and unlocks the
keychain or sets its passphrase (a keychain may have none, which is not
recommended). A task chooses its model, effort and judge beside each other,
as it opens and between turns. It runs as
`nodwyn ui` for a daemon, as `nodwyn app` without one (see [the app](docs/app.md)),
and on a phone paired by name. See [the browser guide](docs/browser.md).

### Background runs

Every task the console starts is a chat; a request that takes long is a
message like any other (ADR 35). For scripts and benchmarks, `nodwyn run`
carries a task to its end with no conversation. Start `nodwyn daemon`, then
submit a bounded task:

```sh
nodwyn run --task task.txt --repo . --budget-wall 2h --max-turns 20
nodwyn run status SESSION
nodwyn run stop SESSION
nodwyn run resume SESSION
```

Attach the browser or terminal client to the daemon to answer tool prompts and
between-turn checkpoints. Disconnecting the client leaves the run owned by the
daemon. Resume preserves the original deadline and consumed turn count; it never
replays an interrupted tool or restores temporary approvals. See [runs](docs/runs.md)
for completion semantics and recovery.

Without a person: `--yolo` answers a run's ordinary confirms yes (never a
security confirm). `--judge` puts each ordinary confirm to the judge
instead, which allows a call only when its probability of "allow" reaches
`judge.approve_threshold` (0.8 by default) and declines security confirms; the
decision is made in Go, so a project's Scheme can only refuse more. It
needs a judge (`[judge]` naming a provider whose key is stored; see
[the judge](docs/manual.md#the-judge)) and excludes `--yolo`.
`--unattended` means nobody answers anything: confirms are declined unless
`--yolo` or `--judge` answers them, soft budget checkpoints go on to the
hard limit, and the run goes on after a turn only if that turn marked a
plan step done. A resume restores none of these; `nodwyn run resume` takes
them again.

### Managed workstation packages

Gitea main pushes also queue private Beauties packages for Fedora 44 x86_64 and
Apple Silicon macOS, including the native window and Linux egress helper.
Enrolled workstations install them through routine DNF or Homebrew updates.
`nodwyn version` reports the packaged source commit. See
[workstation packages](docs/releases.md#workstation-packages-from-main).
