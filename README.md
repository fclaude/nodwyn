# nibb

A minimal, self-extending coding-agent harness: a Go host, a small Scheme
embedded in it for orchestration, one guard at the only Scheme→Go exit, and
a binding table as the sole authority over where a secret may go.

- [SPEC.md](SPEC.md) — the architecture and its invariants
- [plan.md](plan.md) — the bootstrap-then-self-host build plan
- [docs/manual.md](docs/manual.md) — how to read the code and how to use nibb
- [docs/adr/](docs/adr/) — decision records
- [docs/scheme.md](docs/scheme.md) — the language reference
- [docs/friction.md](docs/friction.md) — where nibb got in the way of building nibb

## Maintainer map

The packages follow responsibilities that remain useful as nibb gains more
integrations and front-ends:

| Packages | Responsibility |
|---|---|
| `core` | Own a session, assemble dependencies, coordinate its lifetime and prompts. |
| `budget` | Account for one turn, enforce ceilings, and request additional soft allowance. |
| `loop`, `plan` | Run one model/tool turn; decide whether to plan and score the plan. |
| `event`, `log` | Describe front-end events; persist the history from which context and replay are derived. |
| `keychain`, `binding` | Hold encrypted credentials and inject them; authorize their destinations, methods and positions. |
| `scheme`, `guard`, `prim` | Evaluate extensions; authorize host operations; implement those operations. |
| `llm`, `window`, `redact`, `detect/l1` | Talk to the model; derive its context; redact text; detect credential shapes. |
| `frontend/plain`, `frontend/rpc`, `config`, `cmd/nibb` | Render and answer events; own local RPC connections and sessions; load settings; wire the application together. |

**A user message:** the front-end calls `Session.Send`. The session records
and redacts the message, runs the plan gate, and starts one `loop.Turn`.
The loop builds its window from recorded events, calls the model, and applies
each requested tool through Scheme. Results are recorded before they enter
the next model request. The front-end consumes events from the session.

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
transport, redaction and recording, as Jev does. Do not open the keychain or
read secrets in the integration. Test it through Scheme and the guard with
a local fake service, bound/unbound destinations and canary credentials.
Integration-specific settings should not become getters on `Session`.

The roadmap extends these boundaries: ADR 21 uses events for RPC clients;
ADRs 22–23 bring MCP tools through the primitive/guard path; ADR 24 extends
the credential lifecycle to OAuth. These are planned features, not claims
that RPC, MCP or OAuth are already implemented. Keep `loop`, `plan`, and
`event` separate, and keep credential management shared across integrations.

## Build

```
make build      # bin/nibb
make test       # go test -race ./...
make lint       # vet, staticcheck, boundary lint
make accept     # everything a candidate must pass before promotion
make smoke      # manual two-turn session against the real control plane
make selfcheck  # the candidate proves itself (ADR 13)
make promote TAG=v0.N-phase   # accept + selfcheck, then ~/.local/bin/nibb-stable
```

Requires Go 1.27 and `staticcheck` (`go install honnef.co/go/tools/cmd/staticcheck@latest`).

## Run

```
bin/nibb daemon               # local sessions, turns and event delivery (U2)
bin/nibb tui                  # opt-in full-screen terminal client (P9)
bin/nibb                      # chat in the current repository (plain front-end)
bin/nibb --repo ~/src/x       # pin to another repository
bin/nibb --show-thinking      # show the model's redacted thinking too
bin/nibb --no-confirm         # non-interactive: every confirm is a no
bin/nibb replay <session-id>  # print a session log as a transcript
bin/nibb sessions             # id, last activity, state, repo, first ask
bin/nibb sessions --ids       # IDs only (scripts)
bin/nibb --resume <session-id> # continue in the recorded repository
bin/nibb repl                 # Scheme REPL
```

The control plane is the Ollama server in `~/.config/nibb/config.toml`
(default: Qwen on workshop over the tailnet; no key). Every write and every
command is confirmed by you, with a diff for writes and edits; standing
approvals for read-only commands live in `.nibb/allow.toml` (ADR 17).
Session logs live in `~/.local/share/nibb/sessions/`.
Resume restores the conversation and appends to that same log. It reloads current
configuration, bindings, standing approvals from files, and repository `init.scm`;
session-only approvals and interactive Scheme definitions are reset. An explicit
`--repo` must match the recorded repository. Only one process may append to a
session at a time. Interrupted tool calls are never rerun automatically: a call
without a durable result is marked "outcome unknown" so the next turn can inspect
current state before retrying. `sessions` reports an unfinished turn as
`interrupted`, not as proof that another process is running. Missing final log
lines are repaired on resume; corrupt complete lines are rejected.

Bootstrap-era safety envelope (plan §3.3): paths are pinned to the
repository, credential-shaped files are unreadable, network tools on a
command line need confirmation, and child processes get a scrubbed
environment. P7 redacts recognized credential shapes from user messages,
tool results, logs, and frontend events. Responses are buffered until complete
so split stream chunks cannot expose a key. Unknown or encoded secrets are
not guaranteed to be detected; L3 remains P8. The old operator restrictions
remain in force for older stable binaries until P7 promotion.

Gitea runs `.gitea/workflows/ci.yml` on every push and pull request using the
isolated `self-hosted` runner. It executes `make accept` with fresh race tests,
then cross-compiles Linux and macOS binaries for amd64 and arm64 with cgo
disabled. Only the Linux amd64 binary is executed on that runner. The real-model
selfcheck remains a local promotion gate; CI needs no model or credentials.

The same Linux runner can produce release archives for those four targets.
Cross-compilation does not test macOS behavior or provide Developer ID signing
and notarization. Publishing downloadable releases is separate from the local
`nibb-stable` promotion; this workflow validates builds without publishing them.

Turns also stop when the model repeats an identical tool call four times in a
row, retries a denied call three times, or gets ten consecutive tool errors
(ADR 18). The reason is shown and logged. Any remaining calls in that response
are marked not executed; a new user message starts a fresh turn. Budgets and checkpoint prompts are implemented; the automatic long-run driver remains planned.

Turn limits live under `[budget]` in `config.toml`:

```toml
[budget]
tool_calls_soft = 50
tool_calls_hard = 500
wall_soft = "20m"
wall_hard = "4h"
tokens_soft = 1000000
tokens_hard = 10000000
actions_soft = 25
actions_hard = 200
```

At a checkpoint, `y` doubles the soft limit up to its hard ceiling, `n` stops,
and an absolute number (or a duration such as `40m` for wall time) chooses a
larger soft limit. A completed answer does not ask for more soft allowance. This never approves a tool or raises a hard ceiling.
`--no-confirm` stops at checkpoints. Each answer, including cancellation, is a
redacted `turn.checkpoint` log record. A new turn starts with fresh accounting.

`--max-tool-calls N`, `--budget-wall 2h`, `--budget-tokens N` and
`--budget-actions N` override hard ceilings for this invocation. Explicit legacy
`loop.max_tool_calls` settings still work; do not also set
`budget.tool_calls_hard` in that file. Configurations without a legacy override
now use the ADR 18 default of 500 hard tool calls (previously 200).

Actions count authorized irreversible host attempts, including every nested
write or command inside a Scheme tool. Denials consume no action allowance.
The checkpoint lists attempted file paths and commands. Wall time includes
planning and approval waits; its hard deadline cancels blocked work, while soft
wall checkpoints occur at operation boundaries. Tokens are the control provider's
reported prompt plus output usage: a response can overshoot a token ceiling,
but no subsequent operation starts after that usage is observed. Judge-service
usage is not included because it is not reported through the control provider.

### Local daemon (U2)

`nibb daemon` serves a versioned local session protocol over a private Unix
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


### Terminal client (P9 in progress)

`nibb tui --repo /path/to/project` starts the full-screen client locally;
`nibb tui --socket /path/to/sock` attaches to a daemon. Choose or resume a
session, follow the conversation, inspect scrollable approval diffs, and reply
to budget or masked credential prompts. Ctrl+C aborts busy work; Ctrl+Q quits.
Quitting a daemon client detaches without stopping its session; Ctrl+R reconnects
and recovers history without resending operations. Plain mode remains the default.
Use `:tools` to list model-facing tools, or `:tools NAME` for an input schema.
Use `:log`, `:bindings` (also `:allow`), `:replay` and `:friction` for read-only inspection;
Esc returns to the conversation and `r` refreshes a snapshot.
Enter sends the whole draft; Alt+Enter or Ctrl+J adds a newline. Bracketed paste
preserves line breaks without sending. Prompts keep their own single-line input.
See [the TUI guide](docs/tui.md) for controls, ownership and the remaining P9 work.
