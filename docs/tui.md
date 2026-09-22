# Terminal client (P9, in progress)

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
| `:log [n] [kind prefix]` | Inspect the last matching durable records (default 20, maximum 1000) |
| `:bindings` | Inspect loaded bindings and current command/write approvals |
| `:replay` | Read a transcript of the current session's durable log |
| `:friction` | Read `docs/friction.md` through the existing guarded Scheme path |
| `:help` | Show available controls in a scrollable view |

The first composer is a single-line editor with horizontal scrolling and
bracketed paste; pasted newlines are normalized by the editor. Use single-line
Scheme for `:eval`, especially when comments are involved. A multiline composer remains planned. Existing CLI commands and plain mode remain available.

## Read-only inspectors

Inspectors replace the conversation viewport without stopping event delivery.
PgUp/PgDn scroll; `r` takes a fresh snapshot, and Esc returns to the conversation.
A pending approval, checkpoint or masked credential prompt always takes priority.
Late results cannot reopen a dismissed inspector or replace the prompt panel.

`:log 50 guard.` shows the latest 50 records whose kind starts with `guard.`.
Filters operate on event kind; payloads are shown as indented JSON with sequence
and timestamp. `:replay` uses the same transcript renderer as CLI replay, with
no execution. Both read a fixed complete prefix through `log.read`/`Store.ReadLog`;
later pages retain the first page's watermark. They show which sequence the
snapshot ends at and do not append events. A partial tail reports an error;
refresh after an active writer finishes, or resume for crash recovery.

`:bindings` shows the session's loaded class/destination/method/position rows
and its current effective command/write approvals. It reads a copied snapshot,
not the file on disk, and has no setter. Changing `bindings.toml` takes effect
when the session is reopened. No stored credential values are accessed. An
older daemon without binding inspection reports that limitation instead of
showing a misleading empty table.

`:friction` needs an idle session and submits the fixed user-origin evaluation
`(fs-read "docs/friction.md")`. It therefore uses the same Scheme environment,
repository pin, registered guard checkers, read-size limit, cancellation and
redaction as ordinary reads. User extensions to `fs-read` also apply. It adds
normal evaluation and guard audit records, but source/results do not enter the
model conversation. Missing files, denied reads and escaping symlinks appear
as errors; the frontend never falls back to reading the filesystem itself.
The result is captured only after the matching evaluation request is observed.

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
- `inspect.go` and `inspect_backend.go`: snapshot viewers and their read paths.
- `run.go` and `cmd/nibb/tui.go`: lifecycle and command startup.

Core now exposes a copied binding-metadata snapshot (ADR 10 amendment). Scheme,
guard, keychain and log ownership are unchanged. Bubble Tea v2
uses the `charm.land` module path; the frontend boundary lint covers it as well
as `github.com/charmbracelet` so the new dependency cannot enter core.

The initial client and read-only inspectors are implemented; P9 is not complete.
Remaining work includes fuller tool/plan/budget views and the guarded binding-edit
workflow, richer composing, and broader parity with line-mode commands. U3's
desktop client and U4's background supervisor remain separate roadmap work.
