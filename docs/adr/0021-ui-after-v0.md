# ADR 0021: A real UI after v0 — the daemon, the protocol, the clients

- Status: **proposed** (scope agreed; scheduled after v0, before or beside P10 work)
- Date: 2026-09-22

## Context
v0 has one front-end (`plain`) and one planned (`tui`, P9). ADR 10 already
makes the core UI-agnostic: `core.Session` exposes `Send`, `Events`,
`Answer`, `Abort`, `Eval`, `Close`, every user-visible thing is an `Event`
on the same stream the log writes, and a CI lint keeps front-ends out of the
core. So a second front-end is a new package, not a refactor.

What a *real* UI needs beyond that is not in the plan:

1. **A wire format.** Events are Go structs. A GUI needs them serialized,
   versioned, and delivered over something other than a channel.
2. **A process that outlives a terminal.** Today one process is one session,
   started by a terminal and dying with it. A UI wants to attach to work
   already running, and to reattach after a reload or a crash.
3. **More than one session.** A UI shows a list; the harness has no notion
   of a list, no resume, and no supervisor.
4. **An authority story for a socket.** The plain front-end's authority is
   "you are at this terminal". A socket needs its own answer, and confirms
   are the thing being authorized.

## Decision

A UI is built in four phases, each independently useful. Nothing here
changes the guard, the log, or the Scheme surface.

### U1 — Resume and the session store
`Session.Resume(id)` rebuilds a session from its log: the window is already
a fold over events (ADR 9), so resume is `Build` over an existing file plus
a writer that appends. A `core.Store` lists sessions from the sessions
directory (id, repo, first user message, last event time, whether a turn was
in flight). `nibb --resume <id>` and `nibb sessions` grow accordingly.

*Why first:* it is the smallest piece, it is already the top item in
`docs/friction.md`, and every later phase assumes it.

### U2 — `nibbd` and the protocol
A daemon owning zero or more sessions, speaking **JSON-RPC 2.0 over a Unix
socket** (`$XDG_RUNTIME_DIR/nibb/sock`, mode 0600), with an optional
`--listen` for a TCP port behind a token.

Methods (request/response):

| Method | Meaning |
|---|---|
| `session.list` / `session.open` / `session.resume` / `session.close` | the store from U1 |
| `session.send` | start a turn |
| `session.abort` | cancel the running turn |
| `session.answer` | answer a confirm or a checkpoint (`y`/`n`/`a`/`t`) |
| `session.secret` | answer a masked credential prompt |
| `session.eval` | evaluate Scheme with user origin |
| `session.info` | repo, tools, standing approvals, budgets |
| `log.read` | a page of events, for scrollback and replay |

Notifications (server → client): every `event.Event`, JSON-encoded, with its
log `seq` when it has one. A client that reconnects sends the last `seq` it
saw and is sent the gap from the log — the log is the truth, so catch-up is
a read, not a replay buffer.

The schema is generated from the Go types and versioned: a `hello` exchange
carries a protocol version, and a client that does not understand a field
ignores it. `internal/frontend/rpc` is the server side; the wire types live
in `internal/event` and `internal/log` as they already do.

*Authority:* connecting to the socket is authority, and the socket's
permissions are the boundary (same as an SSH agent). Over TCP, a token from
the keychain, and `--listen` prints a warning naming what a connection can
approve. **A confirm answered over RPC is logged with the client's identity**
(socket peer credentials, or the token's name) so `guard.decision` never
loses who said yes.

### U3 — The desktop client
One GUI over the protocol. The recommendation is a **local web UI served by
`nibbd`** (`nibb ui` opens a browser at `127.0.0.1`): it reuses the
protocol over a WebSocket, needs no toolchain beyond the Go binary plus
static assets embedded with `go:embed`, and runs on every platform the
binary does. A native shell (Tauri/Electron) is a later packaging decision,
not a different architecture.

Views, in the order they earn their place:

1. **Session list** — repo, first ask, last activity, running/idle.
2. **Transcript** — streamed text, thinking folded away by default, a card
   per tool call with the guard badge, the model's usage line.
3. **Approvals** — the confirm as a first-class panel: reason lines, the
   diff for a write, and the three answers (`once`, `always under …`,
   `this turn`). This is the view that justifies a GUI: a diff is the thing
   a terminal renders worst and a UI renders best.
4. **Plan and budgets** — the gate's plan (P5) with its score, and the
   turn's budget with its checkpoints (ADR 18).
5. **Inspectors** — the log with filters, bindings and standing approvals
   (editable, each edit an ordinary confirmed write), the friction log.

### U4 — Multi-session and background work
Several sessions at once (workshop serializes them; the UI shows the queue), a
run driver for long tasks (ADR 18 §4) with checkpoints surfaced as
notifications, and desktop notifications for "needs your approval".

## Amendment 2026-09-22: one product, two profiles

The cowork application (ADR 22) is not a second product: one core, one
binary, one protocol, two front-ends. A workspace `kind` selects the tool
surface, and a `docs` workspace has no `shell-exec`, no `register-tool` and
no Scheme evaluation — so `session.eval` is not exposed to a `docs` client,
and the desktop client of U3 is the same client either way, showing fewer
panels. Only a distribution constraint (a sandboxed bundle that cannot run
a daemon or a container) would force two binaries.

## Consequences
- **Ordering.** U1 fits anywhere (a day). U2 is the real work (about a
  week) and should land *before* P9, because a TUI over the RPC protocol is
  the protocol's first client and proves it. U3 follows v0. U4 depends on
  ADR 18.
- **P9 changes shape:** the TUI becomes a client of `nibbd` when a socket is
  given and an in-process front-end otherwise. Same package, same views.
- **The plain front-end stays** as the CI and `selfcheck.sh` path, and as
  the fallback when no daemon is running.
- **The core still does not change.** If the protocol needs something
  `core.Session` cannot express, that is a gap in ADR 10 and gets an
  amendment — the same rule P9 already has.
- **New risk: the socket is an approval surface.** Anything that can connect
  can answer a confirm. Mitigations: 0600 socket in the runtime dir, no TCP
  by default, client identity in every `guard.decision`, and no standing
  approval may be *created* over RPC without an explicit method (so a
  compromised client cannot silently widen policy).

## U1 implementation — 2026-09-22

U1 is implemented; U2–U4 remain proposed. The constructor is
`core.Resume(options, id)`, returning a new `Session`, rather than mutating an
existing session. `core.Store` derives metadata directly from JSONL logs.
`nibb --resume ID` restores the conversation in the recorded repository, and
`nibb sessions` shows id, last event time, unfinished-turn state, repo and first
ask (`--ids` retains an ID-only scripting form).

A resumed session loads current configuration, bindings, file-backed approvals
and initialization. Session-only approvals, pending prompts and interactive
Scheme state are reset. It never restarts a turn automatically. Model tool calls
without a durable result receive an explicit failed result whose outcome is
unknown; a crash after a side effect cannot prove the operation did not happen.
Completed calls stay in history and are never replayed as operations.

Each writer holds a nonblocking exclusive file lock until close or process exit.
A partial final line is repaired and noted; other corruption and mismatched
session IDs are rejected. Legacy payloads are redacted again before entering
in-memory history or frontend replay, without rewriting the original log.
`turn.end` and `session.resume` are durable lifecycle markers (ADR 2 amendment).
This adds no daemon, index, background work or RPC surface.
