# ADR 0021: A real UI after v0 — the daemon, the protocol, the clients

- Status: accepted; implemented (U1, U2, P9 and U3 2026-09-22; the canvas design 2026-09-30; U4 2026-10-01, completed by ADR 18's unattended runs)
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
in flight). `nodwyn --resume <id>` and `nodwyn sessions` grow accordingly.

*Why first:* it is the smallest piece, it is already the top item in
`docs/friction.md`, and every later phase assumes it.

### U2 — `nodwynd` and the protocol
A daemon owning zero or more sessions, speaking **JSON-RPC 2.0 over a Unix
socket** (`$XDG_RUNTIME_DIR/nodwyn/sock`, mode 0600), with an optional
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
`nodwynd`** (`nodwyn ui` opens a browser at `127.0.0.1`): it reuses the
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

Amended 2026-09-30: a workspace's kind no longer selects the tool surface.
A `docs` workspace has `shell-exec`, `register-tool` and Scheme evaluation
like any other, and `session.eval` is exposed to every client (ADR 22).

## Consequences
- **Ordering.** U1 fits anywhere (a day). U2 is the real work (about a
  week) and should land *before* P9, because a TUI over the RPC protocol is
  the protocol's first client and proves it. U3 follows v0. U4 depends on
  ADR 18.
- **P9 changes shape:** the TUI becomes a client of `nodwynd` when a socket is
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
`nodwyn --resume ID` restores the conversation in the recorded repository, and
`nodwyn sessions` shows id, last event time, unfinished-turn state, repo and first
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

## U2 lifecycle implementation — 2026-09-22

The first U2 slice is implemented as `nodwyn daemon` in the existing binary.
It provides `hello`, `session.list`, `session.open`, `session.resume`,
`session.info` and `session.close` over a private Unix socket. The daemon owns
sessions independently of connections; lifecycle operations are serialized and
reuse the core's existing constructors and log locks. No core interface changed.
The wire contract, framing, macOS runtime-path fallback and current scope are
specified in [the protocol guide](../protocol.md). This is not completion of U2.

The next slice adds turns, event notifications and log catch-up. Approval and
secret replies, peer identity, generated event schemas and approval inspection
follow separately. No TCP or UI client is implemented here.

Two clarifications supersede the earlier method table: budget checkpoint replies
accept `y`, `n`, or a numeric/duration soft limit (ADR 18), rather than the tool
approval vocabulary. Ordinary `session.answer` must not accept `a` to create a
standing approval: that requires a separate explicit method and an attributed
decision. The approval slice must define its wire contract before exposing it.

## U2 turns and events implementation — 2026-09-22

The second slice adds send/abort, explicit per-connection subscriptions, live
notifications and paged `log.read`. A subscribe response fixes a durable
watermark and returns the first catch-up page; later durable notifications begin
after that watermark. Clients fill any remaining gap from disk using a fixed
`through` cursor. There is no replay buffer or automatic retry of tool calls.
`Session.PendingPrompts` supplies a redacted snapshot of current pending
prompts on reconnect. RPC owns no second prompt-state cache. A queued live
prompt may duplicate a snapshot entry; clients deduplicate by ID, and core
retains ownership of whether a prompt can still be answered.

Each connection has one writer and a bounded queue. A stalled client is
disconnected without blocking the session or other subscribers. Sessions keep
running when their clients disconnect. Closing a session still aborts its turn.
A deterministic regression exposed that polling `Busy` did not wait for final
idle delivery; `Close` now waits for the goroutine to finish and rejects new
turns while closing. See [the protocol guide](../protocol.md) for wire details.

Attributed approvals and secret replies, explicit standing-approval creation,
generated event schemas and approval inspection remain unimplemented. Remote
turns may wait at a confirmation; this slice exposes abort, never implicit yes.


## U2 local approval and evaluation implementation — 2026-09-22

The local Unix-socket protocol now implements the remaining U2 methods:
`session.answer`, `session.secret`, `session.eval`, and the explicit
`session.approval.create`. Kernel peer UID/GID/PID supplies the actor, not a
caller-provided label. Ordinary answers cannot create standing approvals;
scoped grants derive their target from a live eligible prompt. Security
confirmations remain mandatory even with turn-wide write approval. Fresh opaque
prompt IDs prevent replies from a previous session lifetime being reused.

Evaluation is asynchronous so user-origin credential registration can receive
its masked reply over the same connection. It uses the core turn lifecycle,
while source/results have distinct redacted log records outside model context.
Session details expose effective command/write approvals. The generated JSON
Schema and drift test describe requests, results and all implemented events.
See [the protocol guide](../protocol.md) for the exact contract and limitations.

This completes U2's local transport. The earlier implementation notes above
record their respective slices, not current missing methods. Optional TCP/token
transport is still deferred; U3/U4 remain proposed. P9 is the next client step.


## P9 first protocol client — 2026-09-22

The opt-in `nodwyn tui` now implements the initial client slice: session selection,
conversation/guard output, approval and budget prompts, masked credentials,
user Scheme, abort and reconnect. With `--socket` it uses the local protocol;
without one it owns a core session through the same small view-facing adapter.
No core facade or policy state changed. Daemon detach leaves turns running;
local exit aborts and closes. Plain mode remains the default.

The client consumes a fixed catch-up prefix before live events and deduplicates
prompt IDs. Connection failure never retries an operation. Terminal control
sequences are stripped from rendered content. The boundary lint now also covers
Bubble Tea v2's `charm.land` imports. [The TUI guide](../tui.md) records current
controls and limits. This is partial P9, not U3 or U4 completion.


## P9 inspector slice — 2026-09-22

The TUI now provides `:log [n] [kind prefix]`, `:bindings`, `:replay` and
`:friction`. Log/replay views use a fixed paged log prefix; binding inspection
uses the additive `bindings` field in session details and the copied core
snapshot described in ADR 10. Friction uses the existing guarded Scheme read.
Prompts preempt inspector display, and stale asynchronous results cannot reopen
a closed view. These are read-only views, not the planned policy-edit workflow.
P9's richer composing, full command parity and expanded tool/plan/budget views
remain unfinished; U3/U4 are unchanged.


## P9 multiline composer — 2026-09-22

The TUI now uses a multiline message editor and a distinct single-line prompt
editor. Enter sends the whole draft; Alt+Enter/Ctrl+J adds a newline. Bracketed
paste preserves line breaks without sending, including Scheme comment lines.
Prompt presentation preserves the draft and cursor; reconnect preserves unsent
text while still clearing secret replies and never retrying a sent operation.
The existing Bubbles dependency supplies the editor. Core, guard, protocol and
plain-mode behavior are unchanged. The remaining P9 work is full command parity,
expanded tool/plan/budget views and the guarded binding-edit workflow.


## P9 tool discovery — 2026-09-22

The TUI exposes `:tools` and `:tools NAME` for the model-facing catalog and input
schemas. Both adapters read existing session metadata; inspection does not query
the interpreter registry or invoke a tool. The catalog remains the one captured
at session construction. No core facade or wire schema changes were needed.
Plain-mode aliases `:h`, `:q`, `:quit`, `:exit` and `:allow` are also recognized;
`:allow` opens the existing binding/current approval view. Commands do not
intercept a pending prompt's reply. Full flow parity and richer tool/plan/budget
presentation remain P9 work, along with guarded binding edits.


## P9 tool-call presentation — 2026-09-22

Tool calls now render as grouped arguments, recorded guard decisions, and full
results/errors with elapsed time. The frontend follows the existing serial
execution interval and requires matching result ID and name; reused IDs do not
merge across intervals. Skipped/unmatched results stand alone, and missing
results remain explicitly unknown. Guard records lack call IDs, so only
model-origin decisions in the interval appear under “during this call,” never
as an invented aggregate permission verdict. Reconnect rebuilds these groups
from the log. No core, guard, durable schema, RPC or plain-replay behavior changed.
P9's plan/budget presentation, guarded binding edits and full flow parity remain.


## P9 plan and budget inspection — 2026-09-22

`:plan` renders the latest user message's recorded gate outcome, distinguishing
plan scores, need-plan probabilities and unscored fallback plans. Absence of a
record never implies success. `:budget` combines configured initial per-turn
limits with the latest submitted turn's recorded checkpoints and decisions.
Usage is explicitly historical; the current facade/protocol exposes no live
counters or remaining allowance. Initial limits and prior checkpoint changes
remain separate, including across resume. The existing checkpoint reply panel
now includes all four usage dimensions. Both inspectors use existing metadata
and paged history, adding no core API, RPC schema, policy mutation or judge call.
Guarded binding edits and full flow parity remain P9 work.


## P9 credential scope in confirmations — 2026-09-22

Credential-bearing confirmations now carry optional structured call facts from
`guard` through the existing confirm event: class, destination host, method and
injection position. Plain and TUI clients show the same scope summary; absent
facts are explicitly unspecified rather than inferred or treated as wildcards.
This also covers a confirmation requested by another checker on a bound call.
The metadata is display-only, redacted at the existing frontend boundary, and
included in reconnect snapshots. Older events without it remain readable.

This is a prerequisite to ADR 6's binding workflow, not its completion. No new
permission operation, persistence or hot reload is introduced. In particular,
Jev currently declares its fixed host but not its fixed method/position in guard
metadata; those omissions are shown explicitly. A later binding proposal must
have complete validated facts and must not infer authorization from a displayed
or redacted prompt. Existing default-deny and one-time approval behavior remain.


## P9 binding workflow implemented — 2026-09-22

Plain mode and the TUI now offer `b` on eligible unbound credential confirmations:
append the displayed exact scope persistently and approve this call. `y` still
means once and `n` denies. The core derives and validates the row and owns the
mutation; frontend adapters submit only the prompt ID. Pending proposals survive
RPC reconnect and are rejected after cancellation or resolution. Applied/failed
outcomes are shown in live output and replay. Inspectors remain read-only.
Jev's fixed POST/Bearer scope is now complete (ADR 20 amendment). Binding edits
are implemented; the remaining P9 item is verification/completion of line-mode
flow parity. U3/U4 remain future work.


## P9 acceptance completed — 2026-09-22

The remaining parity audit added visible temporary approval-change records and
end-to-end coverage for standing directory approvals, turn-scope expiry and
protected-path restrictions through both adapters. Together with existing
message/eval, credential, binding, inspector, plan/budget, resize, cancellation,
reconnect and process-tree tests, this completes P9's line-mode flows. The TUI
guide records the acceptance matrix. Plain stays the default; live budget
counters are not claimed, and U3/U4 are still separate future phases.


## U3 read-only browser slice — 2026-09-22

`nodwyn ui` serves embedded assets on a random 127.0.0.1 port and bridges a
WebSocket to an existing Unix-socket daemon. The serving process is a frontend
attachment, so closing it does not take the daemon's sessions down. This refines
"served by nodwynd" above: the same binary/protocol serves the UI, while ownership
stays in the separately running daemon. No general TCP RPC listener is exposed.

Pairing uses a random code shown in the local terminal, a same-origin POST and
an HttpOnly SameSite cookie. Exact Host/Origin checks, fetch-metadata checks,
no-store responses and a restrictive CSP protect the browser boundary. Tokens
never travel in URLs or session logs. Each browser socket has its own existing
RPC client; kernel identity remains the bridge process's identity.

This slice exposes only hello, session listing/details, subscriptions and paged
log reads. All mutation methods are rejected by the bridge. The browser renders
literal text, catches up to a fixed watermark before live delivery, and never
retries an operation after disconnect. Interactive turns, approvals, credentials,
plan/budget views and inspectors are still U3 work, not implemented by this slice.


## U3 conversation controls — 2026-09-22

The paired browser bridge now forwards session open/resume/send/abort/close to the
existing daemon. `--read-only` preserves the original authority restriction.
Hello advertises only methods allowed by this bridge; server enforcement remains
independent of the visible controls. Scheme evaluation, prompt replies, secrets,
standing approvals and bindings are still unavailable from this slice.

The composer holds per-session drafts only in tab memory. No reconnect or failed
reply automatically retries a mutation. Transcript catch-up restores the current
busy snapshot after historical events, then applies queued live events. Browser
checks cover creation, sending, drafts, close/resume and aborting a pending prompt.
Approval/diff panels, masked replies and inspectors remain U3 work.


## U3 prompt panel — 2026-09-22

The browser now displays pending confirmations with reasons, arguments, egress
scope and the existing write diff. One-time allow/deny, masked keychain input,
and budget checkpoint answers use the existing explicit RPC methods. Read-only
mode renders these prompts without reply controls and rejects replies server-side.

A reconnect restores pending snapshots; durable receipts, idle, or another client
answering clear the panel. Secret input is never a message draft and clears on
submit, refresh, session switch and disconnect. Browser strings cannot promise
memory wiping. Replies use the bridge's kernel-authenticated Unix peer identity;
tabs do not establish distinct identities. Core still rejects stale IDs, forged
actors, and attempts to exceed a checkpoint's hard ceiling. Standing approval and
binding controls, plan/budget overviews and inspectors remain unfinished U3 work.


## U3 scoped approval choices — 2026-09-22

Eligible browser prompts now offer a session prefix grant, a turn-wide ordinary
write grant, or the core's exact persistent binding proposal. The shared guard
helper derives scope eligibility and labels for core, the TUI and browser prompt
metadata. It does not authorize a call: core still checks the live prompt and
uses the separate attributed approval/binding methods. Older daemons without
scope metadata get one-time browser choices only.

The review panel names temporary grant lifetime and persistent binding fields.
Protected paths and security confirmations cannot acquire wider write/command
scope, denials still win, and resume does not restore transient authority.
Tests exercise all of these boundaries and local fake credential injection.
Plan/budget overviews, inspectors and confirmed policy editing remain U3 work.


## U3 read-only inspectors — 2026-09-22

The browser adds fixed plan, budget, filtered log, current policy and tool-catalog
views. They use the same redacted durable records and existing `session.info`
metadata. Plan lookup starts at the latest user message; absent planning outcomes
never inherit an earlier plan. Budget limits and checkpoint records are labeled
separately, without inventing live remaining allowance.

Views identify their log watermark and refresh explicitly. Current metadata is
sampled separately; saved logs cannot claim current tools or authority. Pending
prompts take priority over inspector navigation. Browser checks cover paged log
filters, stale-plan exclusion, snapshot refresh and prompt priority. The guarded
friction-file view and confirmed policy editing remain U3 work.


## U3 guarded friction inspection — 2026-09-22

The browser's Friction view starts a fixed `docs/friction.md` read through the
session interpreter and guard. The additive `session.friction` method returns an
operation ID immediately, leaving the same connection free to answer custom
checker prompts. An ephemeral `inspection.result` carries the redacted text or
error; the existing evaluation and guard records provide durable audit history.
No arbitrary path or general browser Scheme evaluation is exposed.

The operation uses normal turn serialization, cancellation and prompt handling.
Read-only bridges reject it. The browser caches only the completed selected
session's result; explicit refresh reads again, and denied/canceled reads replace
old content with an error. Confirmed policy editing remains the final U3 slice.


## U3 confirmed policy editing completed — 2026-09-22

The policy view now edits the two named user-owned policy files: shared bindings
and repository standing approvals. Fixed user-origin host primitives perform
bounded reads and validated, atomic replacements through the existing guard.
They are hidden from the model catalog; frontend calls use registered primitive
identity, so Scheme environment rebinding cannot redirect them. No arbitrary
path or general browser evaluation method is introduced.

Every save has a separate one-time diff confirmation. The persistence lock and
revision comparison reject stale content; denial/cancellation do not publish.
Bindings reload before egress as before. Approval files still load at session
construction: existing sessions retain their grants, and the UI states this.
Known-secret text is refused by the editor instead of editing a redacted copy.
Read-only bridges reject both policy methods. This completes the U3 views and
review flows; U4 background driving and notifications remain separate work.


## U4 cross-session attention — 2026-09-22

The daemon offers a content-free state watch independent of transcript
subscriptions. Monotonic revisions let clients merge its initial snapshot with
live open/running/needs-answer transitions. Its lock is separate from lifecycle
ownership so close can keep draining; existing bounded peer queues apply.

The browser updates sidebar attention without reloading the selected view or
its draft. Optional desktop notifications require an explicit browser permission
request and use generic text while the page is hidden; no prompt content enters
OS notification text. Reconnect establishes a new baseline without repeating old
notifications. The ADR 18 bounded run driver remains unfinished U4 work.

## General-purpose UI design — 2026-09-30

A redesign of the browser console for nodwyn as a general helper (coding first,
with MCP tools and document workspaces from ADR 22) is sketched on a design
canvas: [link removed]. It is private to
the maintainer's claude.ai account: a session signed in to that account can
open and edit it; nothing else can until it is shared. The canvas is the source
of truth for the design; its files are not in this repository.

It is a proposal, not an implementation. The console is organized around tasks
instead of inspector tabs: a home page to start any task and see what needs an
answer across sessions; a task view with the approval (diff and the four
answers) inline in the conversation, and the plan, budget, changes and project
notes beside it; a task using MCP tools, where an undeclared tool's send-out
names its destination and credential and offers no standing answer; a
Connections page for tool sources, per-tool capability profiles and
credentials; and a phone approval view, which assumes the optional
token-authenticated transport above. Home, the coding task and the phone view
also have dark versions.

## The canvas design implemented — 2026-09-30

The console now follows the design canvas above: Home, the task view with the
approval inline and its aside, Connections (MCP servers), Workspaces,
Settings, a phone layout and the dark palette, with the canvas's tokens and
embedded typefaces. How it is reached changed with [ADR 30](./0030-one-app-three-ways-to-reach-it.md):
`nodwyn app` (one program, no daemon to manage, a native window on macOS),
`nodwyn ui` for a running daemon, and other devices paired by name with
`nodwyn ui --listen`, whose answers record the device. The "optional TCP"
transport of U2 is realized as that bridge, not as a raw RPC listener.
The cowork pieces the canvas assumes (workspaces, attachments, remote MCP
servers) are ADR 22 W1, W2 (text) and ADR 23 W3a. Not yet in the design's
views: commit hashes under finished tasks. Per-step plan progress is there
since 2026-09-30: the model marks steps with `plan-step`, the log records
each mark (`plan.step`), and the plan panel shows which steps are done,
skipped and next, as the model's word.

## Amendment 2026-10-05: Routine activity folds into one row

The transcript still stacked a row for each judge request, focused context,
instruction to the model, group of calls and token line, and an
instruction to the model started a second "nodwyn" heading before any
reply. Routine activity now shares one closed `details` row, **Activity**,
per stretch between replies. The rows and groups it holds are unchanged
inside it, and its summary follows what they say without opening or
closing it. An instruction to the model belongs to the turn it is part of.

Replies and streamed text stay outside. A short line of narration between
writes still joins its Edited group, and so does the thinking before each
write. A warning, an error, a turn that stopped before it was done (at a
limit, or stopped) and a worktree that left the repository's uncommitted
files out stay in view: the transcript is the only place that says them.
A failed call is counted on the row in red ("1 failed") and opens its run
or group inside, but not the row: a command that exits 1 is routine (a
grep that finds nothing, a test run before its fix), and opening the row
for it showed the whole stretch. A question leaves the row closed, since
its card at the end of the conversation asks it.

Only the display changes: no log record, model window, RPC method or
`/compact`. The transcript browser check builds the stack from the
screenshot that asked for this and checks the fold, what it keeps, live
updates, failures, questions, stops, thinking between writes and turn
boundaries; the screens, models and worktrees checks open the row before
looking inside it.

## Amendment 2026-10-05: A skipped call is a note that says why

The explorer that prepares a plan has twelve tool rounds. A model that asks
for more calls in its last round has them answered "Not executed: this
agent is out of rounds" (`skip-tools`), so that every call has a result and
the agent can still write its report. The console drew each of those
results, which have no call in view, as a failure: a red "fs-read failed"
outside the Activity row, once per call, on a task that was going well.

A result whose error starts "Not executed: " is now a skipped call, not a
failure. It is a note behind the Activity row, its line saying which tools
were skipped and why ("6 calls skipped: this agent is out of rounds"), the
tools listed behind it; calls skipped in a row for one reason are one note.
It is not counted as "failed" on the row. A result without its call that did
fail is still drawn in red outside the row. The prefix is the one
`skip-tools` writes for the model to read (`docs/scheme.md`), so no log
record, RPC method or field changes. The transcript browser check skips
calls for two reasons and fails a stray result.

## Amendment 2026-10-07: Monitoring stays independent of the plan

The Live tab stacked turn statistics, a variable-length plan and task
totals in one scrolling column. A longer plan, or opening a step, pushed
statistics out of view. Monitoring now has five reserved slots above the
selected content: task-wide spend, current turn time, context fullness,
turn tool calls and Cache hit. Cache hit exposes the cached, token-weighted
session cache rate already in Statistics' Totals: raw cache-read prompt
tokens divided by raw prompt tokens from replies reporting both, not a
percentage of requests or discounted budget tokens. Missing or unreported
usage is excluded; no positive denominator is unknown, and reported zero
cache reads remain 0%. Truncated history covers the log that remains, as
its tooltip explains. Other unknown values are dashes, not invented zeroes;
unpriced spend remains a lower bound and truncated history is identified
in its tooltip. A fixed-height warning line flags displayed limits at
90% and distinguishes a reached limit; this does not change enforcement.

Bottom navigation separates Progress (the plan), Statistics (the existing
turn budgets and task totals), Changes and Workspace. Retained notes
belong inside Workspace alongside the folder, access, tools and Details.
Smaller labels and tighter spacing keep all five worktree tabs, including
Review with its comment count, on one row at the 344px and 300px desktop
panel widths. Keyboard navigation and visible focus are retained.
On desktop only the content scrolls, preserving its per-tab reading
position across events and switches. Narrow screens stack normally
without a nested scrolling content area. The summary stays available
across all tabs and its time updates while the task works.

Changes and worktree Review remain separate, with their current approval
previews, actual worktree diffs, comments and recovery behaviour.
Consolidating them, and relocating attachments from Changes, is explicitly
not built in this iteration: the user wants to iterate on Changes next.
No log, RPC or accounting semantics change. Browser checks cover a long
plan, step expansion, event redraws, tab returns, empty values, limit
warnings, live summary updates, cache rates and cached aggregation reuse,
five-tab geometry at both desktop widths, keyboard focus, mobile layout
and editing notes in Workspace.

## Amendment 2026-10-08: Worktree review opens in the centre

The sidebar was too narrow for reading a task's full changes, and Review had
no way to close without choosing another tab. A worktree task now has a Review
button alongside the fixed monitoring summary; Home's Recently finished list
also has a direct Review link. Review uses the existing centre inspector,
replacing the conversation and reply form until Close review restores them.
The four detail tabs and monitoring remain available; Changes still means
recorded approval previews, kept versions and attachments, not the branch diff.

Unified is the default, with old and current-file line numbers. Side-by-side
aligns contiguous replacements in order, leaves unmatched additions/deletions
opposite blanks and keeps hunk boundaries and explanatory notes. Comments
target current-file lines only; old-only deletions can receive file comments.
Content and paths remain literal text. Long lines wrap within the columns and
phone split diffs scroll inside their bounded body, never the whole page.
The close control stays visible while the centre review scrolls.

Saved comments, unfinished comment and overall note, display mode, reading
position and textarea selection stay in each task's tab-memory state through
redraws and closing/reopening. Late replies update that state but cannot reopen
a closed review; stale replies cannot replace newer diffs, and a failed refresh
retains the preceding diff. Existing inline approval and Details inspector
behaviour stays separate, including Home's pending-approval entry points.
No RPC, log or schema changes. The worktrees and screens checks fail on the
previous sidebar implementation and pass on this one; controlled-response
guards and transcript, approve and firstrun checks pass on both. Branch approval,
Changes consolidation and persistent review drafts are not built.

## Amendment 2026-10-16: The day-to-day pages carry one hierarchy

A review of the console's daily pages (Home, a task, the sidebar, search,
the schedule dialog) found the same faults in each: every control at once,
errors as passing toasts, a phone screen spent on chrome. What was decided,
and the pages follow it:

- **Errors stay.** A confirmation is a four-second toast at the top of the
  window; an error is a separate alert that stays until it is dismissed, is
  not replaced by a later message, is shown once when identical and may offer
  Retry. A read the desk makes that fails is such an error, never "an older
  daemon" (only a method the daemon does not offer is); a page that throws
  while it is drawn is replaced by its failure with Reload and Home.
- **A control for the state, a menu for the rest.** A task's heading shows
  Stop while it works or Resume once it is saved, and one menu for Search,
  Schedule, Close, Archive and Delete. Home's composer shows Attach, Options
  (worktree, Limits, helpers, Schedule, with a count of what is off its
  default), the model and Send. Scheduling has one name, Schedule, and Repeat
  is only the field for how often.
- **The composer answers.** Enter sends in both composers and Shift+Enter
  breaks the line, with a hint under the box; a saved task's reply box works
  and resumes the task first; Schedule on Home asks for its message in its
  own dialog.
- **The phone's screen is the request's.** While a request waits the reply
  box is hidden; only Needs you comes above Home's composer; targets are 44
  px; the topbar's pill is a link.
- **Only the task list scrolls** in the sidebar; the side panel's tabs sit
  above their content and its summary is spend, turn time and context (tool
  calls and the cache rate are in Statistics).
- **One search box**: it filters tasks and offers the search of what was
  said, over this task and the project's earlier ones by default, with
  matches said in words and the query marked.
- **Nothing is thrown away by a slip**: removing a schedule and discarding
  a policy draft ask first; review comments and the notes draft survive a
  reload of the same task.

Not built: moving the judge select into Options (it stays in the model
picker, where it appears only when a judge is configured); the setup pages
(Settings, Models, Credentials, Setup, Connections, Workspaces) still call
the old toast for their errors, and five of the ten inline column styles
remain there; reading a saved worktree task's diff from its retained
worktree, so that review would need no resume.

## Amendment 2026-10-17: a daemon starts again over the socket a crash left

The U2 daemon refused any existing socket, stale ones included, so that it
could never unlink another daemon's endpoint. A daemon killed outright
(SIGKILL, the out-of-memory killer, a power loss) leaves its socket, so the
next one refused to start until someone removed the file by hand, and a
service manager's restart (systemd's `Restart=on-failure`, for a daemon kept
running on a server) failed every time. `nodwyn app` already removed a socket
nothing answered on (ADR 30), with its own copy of the check.

`rpc.Listen`, which `nodwyn daemon`, `nodwyn app` and the test daemons bind
with, now makes that check itself, and the app's copy is gone. It removes the
socket only when a connection to it is refused, which is how the kernel
answers for a socket no process listens on. A connection, a timeout or any
other error, and any file that is not a socket, still refuse: a running
daemon's endpoint is still never unlinked.

The check, the removal and the bind are made holding an exclusive `flock` on
the socket's path with `.lock` appended. Without it, two daemons starting at
once could both find the old socket stale, and the second unlink the socket
the first had just bound, leaving the first serving its tasks and schedules
where no client can reach them. The lock file is in the socket's directory,
which `Listen` already requires to be this user's, mode 0700 and not a link,
so only this user can create or replace it. It is never removed, since
removing it would let a third daemon lock a new file while a second waits on
the old one. The lock is held only while starting: once the socket is bound,
a connection to it is the proof that a daemon runs.

`scheduler.lock` in the data directory was not used for this. It guards
dispatching schedules, per data directory, and is taken once serving starts;
two daemons on different sockets may share a data directory (the second
serves clients and dispatches nothing), and it is the socket that two
starting daemons contend for.

The app now binds before it reads the configuration, so a daemon it cannot
use is still refused before anything else can fail; when the bind finds a
daemon that started after the app's first dial, the app attaches to it.
`TestCLIDaemonRestartsAfterAKill` kills a daemon with SIGKILL and starts
another on its socket; `TestListenBindsUnderTheSocketLock` holds the lock and
checks that `Listen` waits for it. Not built: a daemon that removes a stale
socket does not say so in its output.
