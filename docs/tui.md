# Terminal client: first P9 slice

The TUI is opt-in. Plain chat and `--plain` remain the default and the path
used by scripts and promotion selfchecks.

```sh
nibb tui --repo /path/to/project
nibb tui --resume SESSION_ID
nibb tui --socket /absolute/private/directory/sock --repo /path/to/project
```

Without `--socket`, the TUI uses core directly and owns the selected session.
It loads the same configuration, bindings, initialization and shared keychain
as plain chat. Keychain unlock happens before entering the full-screen view;
`--no-keychain` skips it. Quitting aborts local work, drains events and closes
the session log.

With `--socket`, the TUI attaches to an already running `nibb daemon`. The
daemon owns configuration and credentials, so `--no-keychain` is rejected in
this mode. Quitting detaches; the session and any turn continue in the daemon.
Use Ctrl+C to abort before detaching if that is what you want. The UI shows
this difference in its footer. A connection never silently falls back to a
local session.

Flags belong after `tui`. Both standard input and output must be terminals;
use plain mode for pipes. No daemon is started automatically.

## Everyday use

The initial list offers a new session in `--repo`, followed by stored sessions.
Use arrow keys and Enter to open one. A selected stored session uses its
recorded repository and the core's resume rules. `--resume` skips the chooser.
Another process's writer lock remains authoritative; the TUI reports an attach
error rather than opening a second writer.

The conversation shows user and model text, tool calls/results, guard outcomes,
plan scores and evaluation output. Thinking is hidden by default; `:thinking`
toggles recorded reasoning and live thinking. Durable response text replaces
its streamed preview rather than appearing twice. The log remains the source
of truth; scrollback is a display derived from it.

| Control | Meaning |
|---|---|
| Enter | Submit the current message or displayed prompt answer |
| PgUp / PgDn | Scroll conversation or the current approval diff |
| Ctrl+End | Jump to the bottom |
| Ctrl+C | Abort busy work; quit when idle |
| Ctrl+Q | Quit immediately (detach in daemon mode) |
| Ctrl+R | Reconnect to the daemon and refresh history/prompts |
| `:eval EXPR` | Evaluate user-origin Scheme through the usual guard |
| `:thinking` | Show or hide thinking |
| `:help` | Show available controls |

The first composer is a single-line editor with horizontal scrolling and
bracketed paste; pasted newlines are normalized by the editor. Use single-line
Scheme for `:eval`, especially when comments are involved. This slice does not
yet provide a multiline composer or the planned `:log`, `:bindings`, `:replay`
and `:friction` inspectors. Existing CLI commands and plain mode remain available.

## Prompts and credentials

An approval temporarily replaces the transcript with the full, scrollable
operation description, reasons and diff. Type a displayed choice and press
Enter: `y` approves once; `n` declines. Eligible prompts also offer `a` for the
derived session scope and `t` for ordinary writes this turn. These use the
explicit scope-creation operation, never a generic yes answer. Security and
protected-path prompts do not offer wider scope. A budget checkpoint displays
its soft/hard limits and accepts only budget choices.

Credential prompts use a masked editor. For example, `:eval
(credential-register "service-key")` asks for the value separately, then gives
Scheme an opaque handle. The secret is never added to transcript or command
history. Submitting, canceling or disconnecting clears the editor; the reply's
transient byte copy is cleared after delivery. JSON and editor string storage
remain garbage-collected, as documented in the protocol. Empty input declines.
An unfinished ordinary message is kept separately while a prompt is displayed.

## Reconnect and failure

A daemon connection failure clears the secret editor and disables submissions.
Ctrl+R reconnects, resumes the selected session, loads a fixed log prefix through
paged catch-up, then applies live events beyond that prefix. Prompt snapshots
and live duplicates share IDs; an already resolved prompt is not shown again.
Reconnect refreshes from the full history in this first slice. It never resends
a message, evaluation, approval or secret. If a reply was lost, inspect the
recovered history and current prompt before deciding what to do next.

The RPC client has one socket reader, serialized writes, correlated responses
and a bounded notification queue. Overflow fails the connection and requires
catch-up; it never silently drops an approval or durable event. Server/repository
text is redacted and stripped of terminal control sequences before rendering.

## Maintainer map and remaining P9 work

- `internal/frontend/rpc/client.go`: connection, handshake, request correlation,
  bounded event delivery and failure; it shares the server's wire types.
- `internal/frontend/tui/backend.go` and `remote.go`: the small operations used
  by these views, implemented over core or RPC. They own cleanup for their mode.
- `model.go`: UI state, commands, selection, resize and navigation.
- `events.go`: durable/ephemeral display reconciliation and safe terminal text.
- `prompts.go`: prompt presentation and explicit answer routing.
- `run.go` and `cmd/nibb/tui.go`: lifecycle and command startup.

Core, Scheme, guard, keychain and log ownership are unchanged. Bubble Tea v2
uses the `charm.land` module path; the frontend boundary lint covers it as well
as `github.com/charmbracelet` so the new dependency cannot enter core.

This is the first usable P9 slice, not completion of the phase. Remaining work
includes fuller tool/plan/budget views, the inspectors and guarded binding-edit
workflow, richer composing, and broader parity with line-mode commands. U3's
desktop client and U4's background supervisor remain separate roadmap work.
