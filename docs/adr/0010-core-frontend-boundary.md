# ADR 0010: Core/front-end boundary

- Status: accepted
- Date: 2026-09-20

## Context
The TUI is one front-end. A real GUI, an editor integration or an RPC client
must be able to drive the same core without the core changing.

## Decision
The core (`internal/core` and everything it imports) knows nothing about
terminals. It exposes one façade, `core.Session`: `Send(userText)`,
`Events() <-chan Event`, `Answer(promptID, reply)` for confirms and masked
input, `Abort()`, `Close()`. Every user-visible thing — streamed text,
thinking, tool calls, guard decisions, plan scores, confirm prompts — is an
`Event`, and the event stream is the same stream the log writes. A front-end
is a log subscriber with a reply channel.

Front-ends live under `internal/frontend/`: `plain` (bootstrap), `tui` (P9),
`rpc` (post-v0). `scripts/lint-boundaries.sh` fails CI if any package outside
`internal/frontend/` and `cmd/` depends, directly or transitively, on
`internal/frontend/...` or on any `github.com/charmbracelet/...` package.

## Consequences
- The guard's `Confirmer` is implemented by `core` as an event plus a wait on
  `Answer`; the guard never knows which front-end is attached.
- If P9 needs a core change, that is a gap in this ADR and gets an amendment,
  not a shortcut.

## Amendment 2026-09-22: durable frontend catch-up

The RPC frontend needs read-only history without reopening a session for append.
`core.Store.ReadLog(id, after, through, limit)` provides validated pages with
legacy-payload redaction and a fixed upper cursor. This adds no transport types
to core and no additional history append point. Session execution and prompt
ownership stay behind the existing facade. `Session.PendingPrompts` adds a
read-only redacted snapshot of pending descriptors for reconnect; it excludes
secret values and reply channels, and does not grant authority to answer.
`Session.Close` now waits for final
turn event delivery rather than polling `Busy`; frontends keep draining until
the event channel closes.

## U2 attributed replies and asynchronous evaluation — 2026-09-22

The facade adds `AnswerFrom`, `SecretReplyFrom`, `CreateApproval` and
`StartEval`. RPC supplies an actor obtained from its authenticated transport;
core knows only the string, not sockets or peer-credential APIs. Existing
in-process `Answer`, `SecretReply`, `Eval` and the boolean guard `Confirmer`
remain usable. The optional `AttributedConfirmer` returns an approval receipt
for the guard to log. Secret replies stay separately typed as byte slices.

Pending records carry their waiting context. Replies check cancellation before
delivery, and the execution goroutine checks again before applying a scope.
Only that goroutine installs standing approval, after recording the actor and
derived target. Opaque random prompt IDs are not reused when a session resumes.
Security confirmations cannot be waived by turn-wide write approval.

`StartEval` shares the existing serialized turn lifecycle, cancellation,
budgets and close wait with `Send`. It runs user-origin Scheme asynchronously,
so an RPC client can answer prompts on the same connection. Its redacted source
and result have separate durable event kinds and do not enter model context.
The synchronous `Eval` entrypoint remains for the sequential plain frontend.

## P9 read-only binding inspection — 2026-09-22

`Session.Bindings()` closes a facade gap for policy inspection. It returns a
deep copy of the loaded binding metadata, including each row's positions and
methods. It reads no keychain values and offers no mutation. The guard keeps
its existing table; configuration edits still require reopening the session.
The RPC frontend includes this snapshot in `session.info` and other session
details. Local and daemon TUI views use the same session-owned information.

Log/replay inspectors use the existing redacted, paged history boundary.
Friction inspection uses the existing fixed user-origin `(fs-read
"docs/friction.md")` evaluation; it does not add a direct frontend file-I/O path.
Its guard/evaluation audit remains durable, with source/results outside model
context. No new policy, transport concept or terminal dependency enters core.


## P9 explicit persistent binding approval — 2026-09-22

`Session.CreateBinding(promptID, actor)` closes the mutation gap identified in
ADR 6. The frontend chooses only a pending prompt; it cannot submit a row.
E3 supplies an exact unbound class/host/method/position proposal. Core rejects
security prompts, canceled/resolved IDs, missing facts, mismatched proposals
and proposals whose scope would be redacted. The waiting execution goroutine
records the actor and requested row, persists it, refreshes policy, and records
an applied/failed outcome before the call can proceed. A failed write denies
the call. This is distinct from temporary command/write approval and is not
available to Scheme or a model tool.

`binding.Live` owns the session's validated snapshot; all binding-aware checkers
read that same authority. The first guard checker refreshes it before egress.
Malformed files deny even under confirm policy. A configured path is authoritative,
including absence; the daemon cannot resurrect its startup table after deletion.
`Options.Bindings` remains available for explicitly pathless, in-memory hosts.
Inspectors report the last validated snapshot and cannot mutate it.

Persistence appends TOML without reformatting existing content, uses a sibling
advisory lock for nodwyn writers, and syncs a private temporary file before atomic
rename and directory sync. External editors should honor the same lock; a
content comparison detects edits during preparation but is not a compare-and-swap
against an uncooperative writer. Symlink/non-regular write targets are refused.
Other sessions observe updates at their next egress check. Binding scope remains
hostname-based (not URL path, port or scheme), as in the existing table format.
Audit replay does not restore permission. The protocol exposes an explicit
`session.binding.create` operation, with identity derived from its local peer.

## Amendment 2026-10-16: a record is one step

`Session.Record` wrote the log, kept the event for the window and sent it to
the front-end as separate steps, and the turn and the requests that change a
setting during it (Don't ask, the model, the limits, a rename) recorded at the
same time. In a three-second test with six writers, hundreds of delivered
events had a lower `Seq` than one delivered before them; the stream and the
window disagreed on order, and a client that subscribed then took its
watermark from the last event it was given and lost the one before it. The
steps now run under one lock (`recMu`), so the log, the window's events and
the stream have one order. The lock is held while the event waits for a
front-end that is not draining, which blocks other recorders as the unbuffered
send already did; nothing that reads the events takes it. The regression is
`TestConcurrentRecordsKeepOneOrder`.
