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
