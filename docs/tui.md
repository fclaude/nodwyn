# Terminal client (P9)

The TUI is opt-in. Line mode (`nodwyn`, or `nodwyn chat`) remains the default
and the path used by scripts and promotion selfchecks. The old `--plain` flag
is accepted and ignored.

```sh
nodwyn tui --repo /path/to/project
nodwyn tui --repo /path/to/project --worktree
nodwyn tui --resume SESSION_ID
nodwyn tui --socket /absolute/private/directory/sock --repo /path/to/project
```

The TUI is a client of the daemon, as the console is (ADR 30, amended
2026-10-17): the daemon owns the sessions, configuration, credentials,
schedules and every refusal, and the TUI shows them. It uses the daemon on
the default socket, or `--socket`'s. When none answers there it starts one
in its own process, as `nodwyn app` does, and says so before the full screen
opens (`tui: serving tasks in this process on …; quitting ends them`). The
console, another TUI or `nodwyn run` can then attach to it while it runs. A
daemon that answers but that this build cannot use (an older protocol) is
left running, and the TUI says so instead of starting a second one.

When the daemon's keychain is locked (one `nodwyn app` started, or one whose
passphrase was changed elsewhere), the TUI asks for its passphrase before
the full screen opens, masked, and unlocks it over the socket
(`keychain.unlock`). A keychain without a passphrase is opened by the daemon
without asking. An empty answer leaves it locked, and `--no-keychain` does
not ask: tasks then have no credentials until it is unlocked, here next time
or in the console.

Ctrl+Q detaches: the task and any turn go on in the daemon, for every client
and the scheduler, as when a console tab closes. Use Ctrl+C first to abort
the turn if that is what you want. When the TUI's own process serves the
daemon, quitting ends the daemon and with it the tasks it serves; their logs
remain, and a worktree with nothing unlanded is removed. The footer says
which of the two quitting does. `:close` closes the task in the daemon and
quits, as the console's Close does: its log is released, and a worktree with
nothing unlanded goes with its branch. It waits for a turn to end.

`--show-thinking` starts with the model's thinking shown, as `:thinking`
would. Flags belong after `tui`. Both standard input and output must be terminals;
use line mode (`nodwyn chat`) for pipes.

## Everyday use

The initial list offers a new task in `--repo`, followed by stored tasks (a
task's log is its session; `--resume` and `nodwyn sessions` name it by that id).
Use arrow keys and Enter to open one; PgUp/PgDn move a page, Home and End
the first and last row. Typing narrows the stored sessions to those whose
first message, folder or worktree branch contains the text (Backspace edits
it, Esc clears it); the new-session rows stay. A selected stored session uses
its recorded repository and the core's resume rules. `--resume` skips the
chooser. Another process's writer lock remains authoritative; the TUI reports
an attach error rather than opening a second writer, and when `--resume` or
`--worktree` fails it shows the chooser, with the stored sessions, under that
error.

When `--repo` is a git repository's top folder (it has its own `.git`), the
first row, selected at start, is "New task in a new worktree of" it, as
the console starts a task in a git workspace by default (ADR 34). The session
then works in `<repo>/.nodwyn/worktrees/<id>` on a branch `nodwyn/<id>` from
the repository's `HEAD`; the conversation says so, with the number of
uncommitted files the worktree does not have, and the title shows the
branch. The next row opens in the folder itself. The `.git` check only
decides what is offered: core refuses a folder that cannot have a worktree,
with a sentence saying why. `--worktree` opens a new session in a worktree
at once, without the chooser; it does not combine with `--resume`. A stored
session in a worktree is listed with its repository and branch. Closing it
removes the worktree and its branch when nothing in them is unlanded, as in
every client. The TUI has no diff or review view, by decision (ADR 34,
amended 2026-10-08): `git diff` and an editor read the worktree at the path
the conversation gives, and a comment on the changes is a message.

The conversation shows user and model text, tool calls/results, guard outcomes,
plan scores, the steps the model marks done or skipped (`plan-step`),
attachments, document uploads, a build or refresh of the repository's index
(`[index updated: read 712 files · … · 1.3 s]`, under the call that made it)
and evaluation output. After each reply a
usage line gives its tokens and, when known, the share of the prompt read
from the provider's cache, its cost, the model's time and the request's
speed in tokens a second (`12,400→800 tok ·
78% cached · $0.012 · 200.0 tok/s`; [the manual](manual.md), "Choosing models"). The title
says how full the conversation's context is (`context ~45k of ~61k`: nodwyn's
estimate of the latest request against the window's high water), a
compaction is one line with the size before and after, and `:budget` adds
what the provider counted for that request and the compactions so far. Thinking is hidden by
default; `:thinking` toggles recorded reasoning and live thinking. Durable
response text replaces its streamed preview rather than appearing twice. The
log remains the source of truth; scrollback is a display derived from it.

A message that starts with `/compact` or `/optimize` runs that command
instead of an ordinary turn, as in every client ([the manual](manual.md)).

`:model` shows the model the conversation talks to, its effort and the
efforts it takes, a choice waiting for the turn under way, and why a choice
could not be applied or the model is unavailable. It lists the models the
conversation may be moved to, the same list as the console's picker
(`[control]` as `control`, then each `[models.NAME]`). `:model NAME [EFFORT]`
moves the conversation from its next turn (ADR 40, ADR 41). Naming the
current model changes only the effort, and a choice made while a turn runs
waits for that turn to end. The conversation shows each switch, or the
choice waiting, as a `model →` line. A refusal is the status line: a name
not configured, an effort the model does not take, or a conversation too
long for the target's context. A model given by a server and its id is not
offered, as the console's picker does not offer it. The TUI is always on
this computer, so the choice is never refused as a paired device's is.

`:judge` shows the judge the task asks (ADR 47): its model, host and price,
or that it has none, and why it cannot be asked when it cannot, with the
judges set up, the same list as the console's Judge select. `:judge NAME`
moves the task to that judge from its next question, and `:judge none` to
none; it is chosen while the task is idle, so a busy session says to wait.
Each choice is a `judge →` line in the conversation. Judges are set up in
the console's Settings › Judge, or in `config.toml`.

`:attach PATH` adds a file to the conversation (ADR 22). The rest of the
line is the path. `~/` is the home directory, and a relative path is from
the folder nodwyn was started in, as a shell would read it. Text is
extracted, redacted and capped as in every client, and the conversation
shows the attach line. An image asks its own security confirm, which naming
the file does not answer. It takes the screen like any other prompt, and the
attachment waits for its answer. Ctrl+C meanwhile cancels the attachment
instead of quitting. A message waits until the attachment is done, and a
busy session refuses one. The attachment is asked on a
connection of its own, because the daemon runs one connection's calls in
order and the confirm's answer could not pass the waiting call. The file is
the person's choice, as the console's file picker is. The daemon reads it
as the same user, and the model never names it.
The TUI also follows a run (`nodwyn run`): its
checkpoint arrives as an ordinary prompt ([runs](runs.md)). A question with
named answers, such as the first upload's to Google Docs, lists them; type
one's key (`ask`, `allow` or `deny`) and Enter.

| Control | Meaning |
|---|---|
| Up / Down, PgUp / PgDn, Home / End, Enter | In the chooser: select and open a row |
| Typing, Backspace, Esc | In the chooser: filter the stored sessions, edit and clear the filter |
| Enter | Send the whole draft or submit the displayed prompt answer |
| Alt+Enter / Ctrl+J | Insert a newline in the message composer |
| Ctrl+O | While a turn runs: send the draft to run when the turn ends, after any message queued before it; the message shows as `queued>` under the conversation until it starts. With no turn running it sends, as Enter does |
| Ctrl+S | While a turn runs: stop the turn, as Ctrl+C does, and send the draft as the next message, ahead of those queued. With no turn running it sends, as Enter does |
| Up / Down | Move within the draft; the editor scrolls to follow the cursor |
| PgUp / PgDn | Scroll conversation or the current approval diff |
| Ctrl+End | Jump to the bottom |
| Ctrl+C | Abort busy work or an attachment, and drop the messages queued behind the turn; with an unsent draft, clear it; otherwise quit (also after a disconnect) |
| Ctrl+Q | Quit immediately: detach, or end the tasks when this process serves the daemon |
| Ctrl+R | Reconnect to the daemon and refresh history/prompts |
| `:eval EXPR` | Evaluate user-origin Scheme through the usual guard |
| `:thinking` | Show or hide thinking |
| `:log [n] [kind prefix]` | Inspect the last matching durable records (default 20, maximum 1000) |
| `:bindings` | Inspect loaded bindings and current command/write approvals |
| `:replay` | Read a transcript of the current session's durable log |
| `:friction` | Read `docs/friction.md` through the existing guarded Scheme path |
| `:plan` | Inspect the latest user message’s recorded planning outcome |
| `:budget` | Inspect configured limits, the latest submitted turn’s spend and its checkpoints |
| `:details` | Expand or collapse tool-call groups (see below) |
| `:tools [name]` | List model-facing tools; an exact name shows the input schema |
| `:model` | Show the conversation's model and the models it may be moved to |
| `:model NAME [EFFORT]` | Move the conversation to that model from its next turn |
| `:judge` | Show the judge this task asks and the judges set up |
| `:judge NAME`, `:judge none` | Ask that judge, or none, from the task's next question |
| `:index`, `:earlier` | Show the folder's repository index (files, size, when it changed) and whether the model may search its earlier tasks (ADR 50) |
| `:index forget` | Delete the folder's repository index; the next search builds it again |
| `:workflows` | The daemon's workflows as the console's Workflows page lists them: timetables (scheduled tasks) and those that watch pull requests, email or a command, each with a reference in brackets, the daemon's sentence, when it runs next or what it watches, what today's runs cost and the latest events (ADR 53) |
| `:workflows pause\|resume\|check\|run REF` | Act on the workflow or timetable whose reference is REF: pause or resume either, check polls a source now, run starts a timetable's run now |
| `:outbox` | The answers workflows hold for you: a review for a pull request or a draft reply to an email, each with its reference and the start of the answer |
| `:outbox post\|discard REF` | Decide one. The daemon posts it (a review comment, or a draft in the thread, never sent), with a key bound for exactly that call; a terminal does not change the words, which the console's Edit does |
| `:earlier on`, `:earlier off` | Let the model search the folder's other tasks, or stop it, from its next call; kept with the folder's name in `workspaces.toml`, as the console's Workspaces page keeps it |
| `:attach PATH` | Add a file to the conversation; an image asks first |
| `:allow` | Open the bindings/current approvals inspector |
| `:help`, `:h` | Show available controls in a scrollable view |
| `:q`, `:quit`, `:exit` | Quit using the same lifecycle as Ctrl+Q |
| `:close` | Close the task in the daemon and quit; a worktree with nothing unlanded is removed |

The composer is a separate multiline editor, with up to four visible rows.
Longer drafts scroll within it; terminal resizing does not truncate the draft.
Enter sends the entire message. Alt+Enter or Ctrl+J inserts a newline without
sending; Ctrl+J is useful when a terminal intercepts Alt+Enter. Bracketed paste
preserves line breaks and never submits, so multiline `:eval` expressions keep
Scheme comments on their own lines. CRLF/CR line endings in bracketed paste
become LF; the editor expands tabs to four spaces and removes control characters.
The underlying editor supports up to 10,000 logical lines.

A busy session retains an attempted submission as a draft: Enter while a turn
runs sends nothing, and the footer names Ctrl+O and Ctrl+S, which queue the
draft or steer with it. A message the daemon will not hold (a model switch is
under way, say) comes back to the composer with the reason in the status line.
A `:eval` is never queued. Prompts use their
own single-line editor; closing a prompt returns to the same draft and cursor.
Unsent drafts also survive daemon reconnects, but are not saved across exits;
a first Ctrl+C clears one rather than quitting. The footer says what stops a
running turn (Ctrl+C) and, after a disconnect, which keys leave.
Submitted operations are never retried automatically. Existing CLI commands
and line mode remain available.

## Tool-call groups

Each model tool call has one transcript group. By default it is one line: the
tool's name, a summary of its arguments, the guard's verdict and its state.
The retrieval tools' arguments are said in words, as the console says them:
`index-search  “credential binding” in the repository`, `recall  seq 812 of
task …`, `index-update  the whole repository` (ADR 50).
`:details` expands every group to the call ID, formatted arguments, guard
decisions and the full result or error, and collapses them again. The header reports the
recorded result status and elapsed milliseconds. While awaiting a result it
says so; if the interval ends without one, it says `no result recorded`.
PgUp/PgDn scroll through long arguments, results and standing-approval diffs.
All rendered fields pass through the existing redaction and terminal sanitizer.

A focused context the loop names (the planner's, a sub-agent's) is folded
the same way: its system prompt is behind `── context narrowed: planner ──`,
and what it replies, addressed to nodwyn rather than to you, is one line
(`⎿ planner replied in Scheme · 9 lines`, or `⎿ planner is replying…` while
it streams). `:details` shows both, indented under their lines.

Calls execute serially. The frontend joins a result only to the current open
call with the same ID and name, so reused provider IDs do not overwrite earlier
calls. A result without a matching call (including a skipped call) is displayed
separately with arguments marked unavailable. It never fabricates an execution.

Guard records have no call ID. The group therefore labels them **guard decisions
during this call**, using only model-origin records within the open call/result
interval. This includes nested Scheme operations; it is not an inferred overall
permission verdict. Allows in a row of one tool by one checker are one line
(`fs-read · allow ×712`): an index build reads every file through the guard.
Those reads are the build's own work, so they do not give an `index-search`
or `index-update` its verdict; a file one left out is named on the build's
line instead. User-origin and out-of-interval decisions keep their existing
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
files do not change this view. It reads `session.info`'s `tools` field. Normal inspector refresh,
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

`:plan` shows the latest user message and its most recent recorded gate outcome,
and once the model has marked steps (`plan.step`), the plan's steps as
`[x]` done, `[-]` skipped or `[ ]` open, each with the model's note.
It distinguishes `plan` (recorded plan score and revision count), `proceed`
(probability that a plan was needed, not a plan-quality score), and `degraded`
(a fallback plan with no recorded score). The transcript uses the same labels.
A new message without a gate outcome does not inherit the previous message's
plan. No recorded outcome is shown as absence, never as success or an assertion
that the judge is disabled. Use `:log` for older outcomes and diagnostic notes.

`:budget` shows the task's own limits, those its next turn starts from:
"no limit" for each not set, which is every one unless the task was given
some (ADR 49, amended 2026-10-27), and its spend limit when it has one, the
same locally and over `--socket`. For the latest submitted
user-message or Scheme-evaluation turn it then shows what its model requests
cost, as far as the provider reported or the model's prices estimate (and the
share of prompt tokens read from cache), or that the spend is unknown and why,
and what the judge used (calls, tokens and, with its prices, cost) in that
turn and in the session. Each judge request is also one line in the
conversation. Then that turn's checkpoints: dimension,
usage, soft/hard limits at that point, all four usage counters, the spend so
far when known, files/commands, recorded choice or error, and actor when
available. Elapsed time is rounded to milliseconds. A recorded turn end is
identified separately.

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

A daemon connection failure clears the secret editor and disables submissions;
Ctrl+C and Ctrl+Q quit even if a turn was running. Ctrl+R reconnects, resumes the selected session, loads a fixed log prefix through
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
- `models.go`: `:model`'s view and the model choice over core or RPC.
- `judges.go`: `:judge`'s view and the judge choice over core or RPC.
- `tool_calls.go`: serial call/result grouping and guard/result presentation.
- `plan_budget.go`: recorded gate outcomes, checkpoint history and budget display.
- `run.go` and `cmd/nodwyn/tui.go`: lifecycle and command startup.

Core now exposes a copied binding-metadata snapshot (ADR 10 amendment). Scheme,
guard, keychain and log ownership are unchanged. Bubble Tea v2
uses the `charm.land` module path; the frontend boundary lint covers it as well
as `github.com/charmbracelet` so the new dependency cannot enter core.

Credential-bearing confirmations show the class, destination host, method and
injection position reported by the guard. Unavailable facts are labeled
“not specified by primitive.” Eligible unbound calls offer `b`: persist the
displayed class/host/method/position and approve this call. `y` approves once;
`n` denies. Missing or redacted scope and security prompts cannot create a
binding. Applied/failed outcomes appear in the transcript and replay. Line
mode offers the same choices. Older daemon prompts omit the binding action.

The initial client, multiline composer, tool discovery, read-only inspectors,
tool/plan/budget presentation, guarded binding edits, worktree sessions, the
model choice and attachments are implemented.
P9 line-mode flow parity is complete; parity with the console is not, and
[clients.md](clients.md) records what each client does and what is still
to build. Live budget counters are not exposed by
the current API; the budget view explicitly reports recorded usage. The
browser console and app window ([browser](browser.md), [app](app.md)) are
other clients of the same daemon, which also drives runs ([runs](runs.md)).


## P9 acceptance coverage

The TUI covers the line frontend's user flows over the Unix daemon protocol;
until 2026-10-17 it also ran them over a session in its own process (ADR 30,
amended 2026-10-17). Line mode remains the default.

| Flow | Regression evidence in `internal/frontend/tui` |
|---|---|
| Send messages, evaluate Scheme, masked credential input, cancel | `TestApproveRegisterAndCancelThroughTheDaemon`, `TestMultilineSubmissionThroughTheDaemon` |
| One-time, standing-directory and turn approvals | `TestStandingAndTurnApprovalFlows`, `TestSecurityPromptNeverOffersWiderScope` |
| Persistent credential binding, failure, cancellation and reconnect | `TestBindingPersistsExactlyAndPolicyReloads` |
| Approval audit history without replaying authority | `TestApprovalAuditIsVisibleAndDeduplicated` |
| Tools, help, quit and approval-list commands | `TestToolInspectionThroughTheDaemon`, `TestPlainCommandAliasesRespectPrompts` |
| Log filtering, replay, bindings and guarded friction inspection | `TestInspectorsWorkOverTheDaemon`, `TestLogFilterAndFixedPagination`, `TestFrictionInspectorHonorsGuardAndRepositoryBoundary` |
| Thinking visibility and tool results | `TestThinkingToggleRevealsAndHidesRecordedReasoning`, `TestToolCardsFromDaemonEvents` |
| Plan and budget inspection, checkpoint replies | `TestBudgetInspectionAndCheckpointThroughTheDaemon`, `TestPlanViewSeparatesGateOutcomesAndMessages` |
| Resize, multiline paste, draft preservation and scrollable prompts | `TestComposerNewlinesPasteResizeAndBusyDraft`, `TestContentCannotControlTerminalAndDiffScrolls` |
| Abort kills shell children | `TestAbortThroughTUIBackendKillsShellChildren` |
| Ctrl+Q leaves the task open; `:close` closes it and removes its clean worktree | `TestCloseEndsTheTaskAndItsCleanWorktree`, `TestTheFooterSaysWhatQuittingDoes` |
| A task open in the TUI still runs on its schedule | `TestATaskOpenInTheTUIStillRunsOnSchedule` |
| A resumed task shows its whole conversation, past the first page | `TestResumeShowsTheWholeConversation` |
| The daemon's locked keychain unlocked over the socket | `TestTUIUnlocksTheDaemonsKeychain` (in `cmd/nodwyn`) |
| Reconnect restores history and pending prompts without resending | `TestRemoteReconnectRecoversPendingPromptAndHistory` |
| Worktree row, `--worktree`, branch in the title and list | `TestChooserOpensAWorktreeThroughTheDaemon`, `TestChooserStillOpensInTheFolder`, `TestWorktreeFlagSkipsTheChooser` |
| `:model`, a switch the next turn asks, a choice queued behind a turn | `TestModelChoiceThroughTheDaemon`, `TestModelChoiceWhileBusyIsQueued` |
| Ctrl+O queues the draft and Ctrl+S steers with it while a turn runs; Enter keeps it; a refused message comes back | `TestQueueAndSteerKeysWhileBusy`, `TestRefusedQueueGivesTheDraftBack` |
| `:attach` of text and of an image whose confirm is answered while it waits; Ctrl+C cancels | `TestAttachThroughTheDaemon`, `TestCtrlCCancelsAnAttachment`, `TestAttachPathAndRefusals` |
| `:judge`, a switch, none, and a judge not configured | `TestJudgeChoiceThroughTheDaemon` |

Real-terminal smoke checks supplement these tests for composer input,
inspectors, tool cards, checkpoint choices, masked credentials and the
persistent binding choice. These are operator checks, not human usability research.
