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
