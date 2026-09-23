# Terminal client (P9)

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
| Enter | Send the whole draft or submit the displayed prompt answer |
| Alt+Enter / Ctrl+J | Insert a newline in the message composer |
| Up / Down | Move within the draft; the editor scrolls to follow the cursor |
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
| `:plan` | Inspect the latest user message’s recorded planning outcome |
| `:budget` | Inspect configured limits and the latest submitted turn’s checkpoints |
| `:tools [name]` | List model-facing tools; an exact name shows the input schema |
| `:allow` | Open the bindings/current approvals inspector |
| `:help`, `:h` | Show available controls in a scrollable view |
| `:q`, `:quit`, `:exit` | Quit using the same lifecycle as Ctrl+Q |

The composer is a separate multiline editor, with up to four visible rows.
Longer drafts scroll within it; terminal resizing does not truncate the draft.
Enter sends the entire message. Alt+Enter or Ctrl+J inserts a newline without
sending; Ctrl+J is useful when a terminal intercepts Alt+Enter. Bracketed paste
preserves line breaks and never submits, so multiline `:eval` expressions keep
Scheme comments on their own lines. CRLF/CR line endings in bracketed paste
become LF; the editor expands tabs to four spaces and removes control characters.
The underlying editor supports up to 10,000 logical lines.

A busy session retains an attempted submission as a draft. Prompts use their
own single-line editor; closing a prompt returns to the same draft and cursor.
Unsent drafts also survive daemon reconnects, but are not saved across exits.
Submitted operations are never retried automatically. Existing CLI commands
and plain mode remain available.

## Tool-call groups

Each model tool call has one transcript group with its name, call ID, formatted
arguments, guard decisions and full result or error. The header reports the
recorded result status and elapsed milliseconds. While awaiting a result it
says so; if the interval ends without one, it says `no result recorded`.
PgUp/PgDn scroll through long arguments, results and standing-approval diffs.
All rendered fields pass through the existing redaction and terminal sanitizer.

Calls execute serially. The frontend joins a result only to the current open
call with the same ID and name, so reused provider IDs do not overwrite earlier
calls. A result without a matching call (including a skipped call) is displayed
separately with arguments marked unavailable. It never fabricates an execution.

Guard records have no call ID. The group therefore labels them **guard decisions
during this call**, using only model-origin records within the open call/result
interval. This includes nested Scheme operations; it is not an inferred overall
permission verdict. User-origin and out-of-interval decisions keep their existing
standalone presentation. Pending approvals still replace the transcript and
require an explicit answer.

History catch-up reconstructs the same groups as live delivery. Reconnect can
continue an open call; duplicate events are ignored by sequence. The durable log,
RPC schema, CLI replay and `:replay` output are unchanged.

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

`:tools` lists names and descriptions from the session's model-facing catalog.
`:tools NAME` shows one tool's description and JSON input schema; names must
match exactly. This includes tools registered during initialization and excludes
ordinary Scheme builtins and hidden primitives. Inspection never evaluates the
tool, contacts its external service or grants permission to call it. The catalog
is captured when the session is constructed, so later edits to initialization
files do not change this view. Local mode reads `Session.Tools`; daemon mode
reads the existing `session.info.tools` field. Normal inspector refresh,
scrolling, prompt priority and safe rendering apply.

`:bindings` (also `:allow`) shows the session's loaded class/destination/method/position rows
and its current effective command/write approvals. It reads a copied snapshot,
not the file on disk, and has no setter. Changes to `bindings.toml` reload
before the next egress check. Malformed policy denies egress; the inspector
continues to show the last validated snapshot. No stored credential values are accessed. An
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

## Plan and budget snapshots

`:plan` shows the latest user message and its most recent recorded gate outcome.
It distinguishes `plan` (recorded plan score and revision count), `proceed`
(probability that a plan was needed, not a plan-quality score), and `degraded`
(a fallback plan with no recorded score). The transcript uses the same labels.
A new message without a gate outcome does not inherit the previous message's
plan. No recorded outcome is shown as absence, never as success or an assertion
that the judge is disabled. Use `:log` for older outcomes and diagnostic notes.

`:budget` shows the configured initial soft/hard limits for the current session
lifetime, including the legacy tool-call override. It then shows checkpoints
from the latest submitted user-message or Scheme-evaluation turn: dimension,
usage, soft/hard limits at that point, all four usage counters, files/commands,
recorded choice or error, and actor when available. Elapsed time is rounded to
milliseconds. A recorded turn end is identified separately.

These are checkpoint snapshots, not live counters, remaining allowances or
final turn totals. A turn without a recorded checkpoint has unknown usage here.
Checkpoint increases are displayed as recorded requests, never applied by the
viewer or carried into the next turn. On resume, old checkpoint limits remain
historical evidence while configured limits describe the current lifetime.
Pending checkpoints still use the existing reply panel, which now shows all
four usage dimensions. Opening either inspector records nothing, changes no
budget or permission, and never calls the judge. `r` refreshes and Esc returns.

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
Reconnect refreshes from the full history. It never resends
a message, evaluation, approval or secret. If a reply was lost, inspect the
recovered history and current prompt before deciding what to do next.

The RPC client has one socket reader, serialized writes, correlated responses
and a bounded notification queue. Overflow fails the connection and requires
catch-up; it never silently drops an approval or durable event. Server/repository
text is redacted and stripped of terminal control sequences before rendering.

## Maintainer map

- `internal/frontend/rpc/client.go`: connection, handshake, request correlation,
  bounded event delivery and failure; it shares the server's wire types.
- `internal/frontend/tui/backend.go` and `remote.go`: the small operations used
  by these views, implemented over core or RPC. They own cleanup for their mode.
- `model.go`: UI state, commands, selection, resize and navigation.
- `events.go`: durable/ephemeral display reconciliation and safe terminal text.
- `prompts.go`: prompt presentation and explicit answer routing.
- `composer.go`: multiline editor setup, input routing and editor/viewport sizing.
- `inspect.go` and `inspect_backend.go`: snapshot viewers and their read paths.
- `tools.go`: model-facing tool list and input-schema presentation.
- `tool_calls.go`: serial call/result grouping and guard/result presentation.
- `plan_budget.go`: recorded gate outcomes, checkpoint history and budget display.
- `run.go` and `cmd/nibb/tui.go`: lifecycle and command startup.

Core now exposes a copied binding-metadata snapshot (ADR 10 amendment). Scheme,
guard, keychain and log ownership are unchanged. Bubble Tea v2
uses the `charm.land` module path; the frontend boundary lint covers it as well
as `github.com/charmbracelet` so the new dependency cannot enter core.

Credential-bearing confirmations show the class, destination host, method and
injection position reported by the guard. Unavailable facts are labeled
“not specified by primitive.” Eligible unbound calls offer `b`: persist the
displayed class/host/method/position and approve this call. `y` approves once;
`n` denies. Missing or redacted scope and security prompts cannot create a
binding. Applied/failed outcomes appear in the transcript and replay. Plain
mode offers the same choices. Older daemon prompts omit the binding action.

The initial client, multiline composer, tool discovery, read-only inspectors,
tool/plan/budget presentation and guarded binding edits are implemented.
P9 line-mode flow parity is complete. Live budget counters are not exposed by
the current API; the budget view explicitly reports recorded usage. U3's desktop client and U4's
background supervisor remain separate roadmap work.


## P9 acceptance coverage

The TUI covers the line frontend's user flows over both direct core ownership
and the Unix daemon protocol. Plain mode remains the default; this milestone
adds no desktop client or supervisor.

| Flow | Regression evidence in `internal/frontend/tui` |
|---|---|
| Send messages, evaluate Scheme, masked credential input, cancel | `TestBothBackendsApproveRegisterAndCancel`, `TestMultilineSubmissionThroughBothBackends` |
| One-time, standing-directory and turn approvals | `TestBothBackendsStandingAndTurnApprovalFlows`, `TestSecurityPromptNeverOffersWiderScope` |
| Persistent credential binding, failure, cancellation and reconnect | `TestBothBackendsPersistExactBindingAndReloadPolicy` |
| Approval audit history without replaying authority | `TestApprovalAuditIsVisibleAndDeduplicated` |
| Tools, help, quit and approval-list commands | `TestToolInspectionThroughBothBackends`, `TestPlainCommandAliasesRespectPrompts` |
| Log filtering, replay, bindings and guarded friction inspection | `TestInspectorsWorkLocallyAndOverRPC`, `TestLogFilterAndFixedPagination`, `TestFrictionInspectorHonorsGuardAndRepositoryBoundary` |
| Thinking visibility and tool results | `TestThinkingToggleRevealsAndHidesRecordedReasoning`, `TestToolCardsFromLocalAndRPCEvents` |
| Plan and budget inspection, checkpoint replies | `TestBudgetInspectionAndCheckpointThroughBothBackends`, `TestPlanViewSeparatesGateOutcomesAndMessages` |
| Resize, multiline paste, draft preservation and scrollable prompts | `TestComposerNewlinesPasteResizeAndBusyDraft`, `TestContentCannotControlTerminalAndDiffScrolls` |
| Abort kills shell children; quit releases local ownership | `TestAbortThroughTUIBackendKillsShellChildren`, `TestLocalCloseDrainsBackpressureAndReleasesLog` |
| Reconnect restores history and pending prompts without resending | `TestRemoteReconnectRecoversPendingPromptAndHistory` |

Real-terminal smoke checks supplement these tests for composer input, local and
remote inspectors, tool cards, checkpoint choices, masked credentials and the
persistent binding choice. These are operator checks, not human usability research.
