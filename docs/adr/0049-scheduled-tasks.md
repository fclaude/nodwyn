# ADR 0049: Scheduled tasks, one task each, and turn limits a task can do without

- Status: accepted; turn limits (§2), recorded worktree bases (§3), the log
  index (§10), console history and totals (§11), and scheduler (§1, §4–§9, §12)
  implemented 2026-10-15; login startup not built
- Date: 2026-10-15
- Amends: 18 (a task's turn limits can be off), 34 (a worktree made again
  records its new base), 9 (windows are built from a suffix of the log)

## Context

The person asked for tasks that run on a schedule, and for a design worked
through end to end, the console included. Three things in nodwyn shaped it.

- **There is one way to work** (ADR 35): every task is a chat, and the
  console offers no mode for who answers a run's requests. A schedule must
  not bring that back.
- **A turn's hard limits count time spent waiting for a person** (ADR 18:
  "The hard wall deadline includes model waits, tools, planning and
  confirmations"). A task that runs at 03:00 and asks at 03:10 is stopped
  at 07:10, before anyone is up to answer it.
- **Nothing reads a log from its end.** Asked to keep every run of a
  schedule in one task, so that its transcript grows for as long as the
  schedule lasts, we looked at what reads a log today (2026-10-15, at
  a8eec10):
  - the model's window is built from every event on every request
    (`loop/model.go:94`), and `context-tokens` builds it again each tool
    round (`loop/prims.go:1326`);
  - resuming a task reads its log twice and keeps every event in memory
    (`core/resume.go:26, 41, 109`);
  - every `log.read` page parses the whole file (`core/log_read.go:29`), and
    the console opens a task from seq 1 in pages of 500
    (`web/assets/task.js:143-157`), which is about n²/500;
  - listing tasks parses a log again whenever it has grown
    (`core/store.go:248-293`), once per turn end per console;
  - with a spend limit, each turn decodes every `llm.resp` (`core/spend.go`).

  The person's 78 logs come to 93 MB; the largest is 7.0 MB and 6,849
  events from a single message. A daily run of that kind adds 0.4 to 2.5 GB
  a year to one file.

The person's answers to the first draft (a task per run, a schedules file,
a wall clock that stops while waiting): limits become a checkbox on every
task, on by default, so that a scheduled task can do without them; a
schedule picks a worktree or its folder as Home does; and one task holds
every run, its transcript growing, with loading worked out by
pre-computation and reading from the end.

## Decision

### 1. A schedule belongs to one task, and every run goes into it
At each time a schedule is due, nodwyn resumes its task if the task is
closed and sends the schedule's message, as if the person had typed it. A
run is that turn and whatever the person says in reply; between runs the
task is a chat like any other. A schedule is made from Home's ask bar (a
clock beside the send button turns the ask into a schedule, the task
opening at once with every choice in the bar) or with **Repeat…** on any
task. Deleting the task deletes its schedule; archiving it pauses the
schedule.

A run is a chat's turn in every respect: Settings › Approvals, the guard,
the spend limit and the progress guard apply as they would to the same
message typed then. A schedule cannot hold Don't ask, `--yolo` or an
unattended setting.

### 2. Turn limits are a checkbox on every task, ticked by default
A task's turns stop at Settings' turn limits (tool calls, tokens, actions,
time) unless this computer turns them off for that task: on Home as it
starts (**Limits**, beside **In a worktree**) or in the task between turns
(**Limits** beside the model). Off, a turn has none of the four, and may
wait for an answer overnight. What stays:

- the spend limit, which is the task's and not the turn's;
- the request and command timeouts, which catch a call that hangs, not
  long work;
- the progress guard, which stops a turn that repeats itself or fails over
  and over.

The choice is a `session.limits` record (`on`, `actor`); the latest one
counts, and a task without one has its limits on. Unlike Don't ask, it is
kept when the task is resumed, since a scheduled task is resumed for every
run. A change applies from the next turn: a turn's wall deadline is its
context's from its start. A paired device's tasks start with their limits
on, and a device cannot change a task's (ADR 30): the limits are what stops
a turn nobody is watching, as Settings' limits are this computer's.

In the budget a hard limit of 0 is none, in every dimension, as it already
was for spend (`budget.Start` sets no deadline without a wall limit).
`config.toml` still requires positive turn limits: only a task's choice
makes them 0.

### 3. Worktree or folder is the task's choice
As on Home. In a worktree, a run that left nothing to keep has its worktree
removed when the task closes, and the next run makes it again from the base
branch's current head (`core/worktree.go:290-297`), so each run starts from
today's `main`; a run that left unlanded changes keeps its branch, and the
next run goes on with it. Making a worktree again records its new base
commit, which Review and the close check then use: today they compare with
the commit the worktree was first made from.

### 4. Five repeats, in this computer's local time
Once, every hour, every day, weekdays, every week. The daemon computes the
next times (`schedule.next`) and the console shows them. A minute a change
to summer time skips runs at the next valid minute; an hour it repeats runs
once.

### 5. A missed time runs once, late, and says so
The scheduler is in the daemon and ticks every 30 seconds against the wall
clock: a long timer does not count time while Linux is suspended. A time
that passed while nothing ran (the computer asleep, the app closed) runs
once at the next chance, standing for every time missed, and the run says
how late it started and how many times it stands for. A lock in the data
directory keeps a second daemon from firing the same time.

### 6. A task busy when its time comes skips that time
When the task's turn is running, an answer is pending, or another nodwyn
has it open, the time is skipped and recorded (`schedule.skip`, with why),
as a line in the transcript. A person chatting in an idle task sees the run's
message arrive.

### 7. A scheduled task closes after 15 idle minutes
Closing frees its interpreter, programs and MCP containers and removes a
worktree with nothing to keep (§3); the 15 minutes leave time to read a run
and reply. It is resumed for the next run.

### 8. Each run starts from a handover written while the cache is warm
When a turn in a scheduled task ends idle and its conversation since the
last handover is over a quarter of the window, nodwyn asks for a note at
once, with `compact-note` focused on what the next run needs: the request
reuses the prefix the provider has just cached. The note is recorded
(`context.handover`) and not applied. When the next scheduled message
arrives, nodwyn narrows to it (a lasting `context.narrow` and the handover
message) and sends the message. A person who replies to a run has the whole
conversation, as in any chat; the next run has the note, not a day-old
prefix to read again at full price. A turn that ended with something
pending leaves no note, and the run starts as any message does.

### 9. The log holds the schedule and every run
`session.schedule` holds the definition (message, repeat, time, paused,
spend limit per run, actor; the latest counts), `schedule.run` marks a run
(due, started, how many times missed, whether by hand) and `schedule.skip`
a time skipped or a run that could not start. The next due time is
computed from them. There is no schedules file: the records are written by
the nodwyn process holding the log's lock, in nodwyn's data directory and
never in a repository, as ADR 39 argues for names and archiving. A
scheduled task's spend limit counts per run.

### 10. A derived index makes a long log cheap to read
Each log gets `<id>.idx` beside it: listing info, running totals, the byte
offset where each turn and run starts (and one every 256 events), the
latest choices, and where the model's window starts. Whichever nodwyn
process reads the log folds in what the log gained beyond the index and
writes the index back, as a temporary file renamed into place. (Changed
while building it, 2026-10-15: the draft had the process holding the log's
lock keep it as it appends, and a mark every 1,000 events. Readers keeping
it leave the log's writer as it was, and two that race each wrote an index
of the same log; a mark every 256 events keeps a page's read short.)
A reader checks the line at the index's end, and at each offset it seeks
to, against the log and rebuilds the index from the log on any mismatch.
Nothing is recorded only in the index: it is a revocable view (SPEC,
invariant 1). Listing, `log.read`, resume, the window and the spend limit
read the index and the suffix they need; resume keeps in memory only the
events from where the window starts.

### 11. The console opens a task at its latest run
It pages back by runs, folds older runs to their last reply and builds
their content when opened, and takes totals, spend, cache rate and Changes
from the daemon (`session.totals`) rather than walking every event.

### 12. Schedules are made on this computer
Setting or changing a schedule and turning a task's limits off are refused
to a paired device; pausing a schedule and running it now are not, since
they use only choices this computer made. The model has no primitive that
schedules: persistence is what an injected instruction wants most.

## Consequences

- A task can run, and wait, without a turn limit. The spend limit is then
  the fence on cost; nothing sets one by default.
- `session.info`'s `budgets` can report a hard limit of 0 for the turn
  dimensions, meaning none, as `cost` already could; clients read it so
  (the console's Details › Budget and Statistics, the TUI's `:budget`).
- The index and reading from the end (§10, §11) serve every long task, not
  only scheduled ones: the 7 MB single-message task opens today in 14
  pages that each read 7 MB.
- A log can still grow without end; splitting one into several files is
  left until a single log passes a few GB, since every reader of logs
  would have to learn it.

## Not built

Built on 2026-10-15: §3's recorded base (ADR 34, amended the same day);
§2, a task's turn limits, with the budget's hard limit
of 0 as none; `session.open {limits}`, `session.limits.set` and `limits` in
`session.info`; the `session.limits` record, kept on resume; Home's
**Limits**, the task's **Limits** and its **No turn limits** badge; a line
in the conversation for each choice, in the console and the TUI; the TUI's
`:budget` for such a task. Tests: `internal/budget`, `internal/core`
(`limits_test.go`), `internal/frontend/rpc` (`limits_test.go`),
`internal/frontend/tui`, `internal/frontend/web`, and the `tasks` and
`remote` browser checks.

Also built on 2026-10-15, the first part of §10: `<id>.idx`, with the
listing (`core.Store.Info`, so `session.list`) and `log.read` pages read
through it, folded in from the end and checked against the log; it does not
yet hold totals or turn marks, which nothing reads yet. Tests:
`internal/core` (`log_index_test.go`, against a whole read for synthetic and
recorded logs and stale indexes; `log_end_test.go`, a 50,000-event log
whose first half is unreadable).

Also built on 2026-10-15, the rest of §10 but the console's totals:
resuming a task opens its log from the index's end (`log.OpenAt`, which
reads only what follows it and cuts a torn last line there) and holds the
events from the earliest point any reader needs, rounded down to a mark:
the window's start (`window.StartTracker`, ADR 9 amended), the latest three
requests and the latest that is not a `/compact` (the planner, the judge,
`request-context`, `/compact`), the end of the latest turn that finished
(an unfinished plan), and a run's start; the whole log when it has a
summary. The task's model, judge, helpers and limits come from the index's
latest records of their kinds, and the spend limit starts from the
index's running sum at the mark. Front ends replaying the conversation
(the TUI's own process, `nodwyn --resume`) read it from the log
(`Session.History`). A session left open keeps every event it records, as
before: memory falls back to the end of its log when it next resumes,
which a scheduled task does for every run (§7). Tests: `internal/window`
(`start_test.go`), `internal/core` (`resume_suffix_test.go`, a 2,000-event
log held from its last compaction), `internal/log` (`OpenAt`), and the TUI
and line mode replaying a resumed task whole.

Not built, in the order proposed: §1, §4–§9 and §12, the schedules
themselves; then a login item that keeps the daemon running when no window
is open. Not proposed: the model proposing a schedule, cron expressions,
splitting a log into several files.


## Amendment 2026-10-15: Index totals and boundaries reach the console

The index now folds model and judge spend, unknown costs, cache-read token
sums, request speed, running and working clocks, answered model waits,
merged tool intervals minus human waits, the latest allowed preview per
path and attachment metadata (never extracted text). Unfinished pairs stay
in the fold so extending a saved index gives the same totals as rebuilding
it. End and resume records drop abandoned pairs and exclude crash downtime;
open nested tools count their completed portion once, including when a
result arrives before its outer call ends.

Each submitted turn and each existing CLI driver's run start has its seq,
time and byte offset indexed. The driver's `run.turn` and following
`user.msg` are one turn. `log.read` uses these boundaries when closer than
its sparse mark and checks the sought line's seq as before. `session.totals`
reads the derived sums, counts and latest boundaries without resuming the
task or replaying its old prefix; the bridge offers it as a read method.
Legacy previews and attachment metadata are redacted for display.

The console consumes it for whole-task totals, judge usage, summary spend
and cache rate, approved previews and attachment chips; recorded events
refresh it, live clocks extend only while the task is busy, and a reply to
a former task cannot overwrite the one now shown. Statistics shows turn
and CLI run counts and latest-start tooltips. Older daemons without the
method retain the full-log calculation. The index version changed, so old
indexes rebuild automatically.

This completes §10 for existing record kinds and the totals part of §11.
Not built in this slice: scheduled run records (§9, with the scheduler),
paging back by runs, folding and lazily building older conversations (§11),
and all scheduling behaviour in §1, §4–§9, §12. The console still loads the
whole conversation. The model's window still follows the suffix rules of
the earlier §10 implementation.

Checks: core totals with fixed timestamps, growth, rebuild, torn tails,
legacy redaction, nested tool waits and crash downtime; totals and boundary
pages with the old log prefix unreadable; RPC totals for open and closed
tasks and invalid parameters; tasks and transcript browser checks for
indexed totals after a reply and reopening, summaries and Changes without
their old conversation, and stale responses after switching tasks.


## Amendment 2026-10-15: Latest-run conversation and paging earlier runs

The console now selects the latest run when opening a task with run records,
using `latest_run` in `session.subscribe` or `log.read`. The selection uses
the subscription's fixed watermark while delivery is paused; continuation
pages use that same inclusive end. Inherited setup records feed the displayed
model and context high water without replaying old choices as new messages.

`log.runs` pages backwards over the same fixed prefix. Its bounded, redacted
previews hold the last non-empty model reply, capped at 2,000 Unicode
characters and an ellipsis, with redaction before clipping so a secret
straddling the cap cannot expose its prefix. The index keeps those previews and the setup each
run inherited; neither the preview list nor opening the latest run replays the
old prefix. It remains a revocable view in nodwyn's data directory, written by
the same readers as before, with no new repository-controlled authority. The
index version changed so older views rebuild. The first run includes startup
records; submitted conversation before it is an earlier group. Without any
run records the task remains one whole conversation.

Earlier runs fetches five previews at a time, inserted in chronological order
without moving the reader's existing anchor. They remain collapsed to their
last reply. Expanding one fetches only its fixed range, in bounded event pages,
then builds its own transcript; reopening reuses it. Historical choices,
receipts and turn records cannot change the current model, prompts, pending
operations or gauges. Failed preview reads leave the button available; a failed
run read offers Retry. Replies arriving after a task switch are discarded.
Whole-task totals, spend, cache rate and Changes still come from
`session.totals`. Log and Plan record inspectors label their loaded latest-run
scope. Older daemons without `log.runs` retain the full-history display.

This completes §11 for existing run records: latest-run opening, backwards
paging, collapsed last replies, lazy older contents and whole-task totals.
Current runs are CLI driver starts, with all their turns and human follow-ups
until the next start. Scheduled run boundaries will join the index with §9;
§1, §4–§9 and §12, then the daemon's login item, remain unbuilt. This slice
adds no scheduling behaviour.

Checks: core fixed prefixes, redacted bounded previews, index rebuilds,
conversation preceding runs and suffix reads with an unreadable old prefix;
RPC latest-run subscriptions with a held fake model, fixed catch-up and live
sequence delivery; tasks browser checks for closed and resumed history,
multiple preview pages, expansion spanning event pages, literal untrusted
text and live follow-ups; transcript checks for failed reads, retry and stale
responses after switching tasks.


## Amendment 2026-10-15: Scheduler and console management

This implements §1, §4–§9 and §12. Home’s clock creates the task with all
ordinary choices before recording its schedule, without sending immediately.
Repeat… adds a schedule to an existing task; Schedule… reads and edits it,
pauses, resumes, runs now or removes it. Home lists schedules. Archive pauses;
Unarchive leaves it paused; deletion removes the log and schedule together.
Run now works while paused and consumes no automatic time. Unpausing begins
after now, skipping the paused interval. Same-time edits preserve progress;
retiming starts a new series. Paired devices may pause and run now; unpausing,
like other changes, is local-only. The bridge and daemon both enforce this.

Civil recurrences use the daemon’s local timezone. The preview and saved
view supply five ISO times with offsets; the console retains those offsets
rather than converting them to a paired browser’s timezone. A skipped minute
advances to the next valid one, and repeated minutes choose their first
occurrence. Startup and 30-second wall-clock ticks coalesce missed times,
claim their interval with schedule.run before model work, and record busy,
pending-answer and failed-dispatch skips. One scheduler.lock flock in the
data directory excludes other daemon dispatchers. A second daemon may serve
clients and acquires scheduling if the first releases its lock.

Clarification to §6: a log held by another process cannot also receive a
skip. The daemon defers that skip in memory, extending its interval while
ownership remains elsewhere, then appends it after release. If this daemon
stops before release, that unrecorded skip is lost and restart treats the
interval as missed. No separate authority file is introduced to bypass the
log lock. The schedule, progress, spend base and handover remain log records
written through normal redaction by the process owning the log, in nodwyn’s
data directory. Nothing in a repository enables a timer.

A run clears Don’t ask and uses normal chat approvals, refusing unattended or
yolo options. Its spend base and limit come from schedule.run and survive
resume; human follow-ups share that run’s cost. The next run resets it.
Whole-task totals remain whole-task; open tasks also show This run and use
its amount for spend warnings. Unknown prices remain uncounted as before.
After 15 idle minutes with no pending answer the ordinary close path frees
resources and clean worktrees. Successful turns over a quarter-window since
the last handover call compact-note under the same budget, with tools skipped.
context.handover records the note without narrowing the human conversation;
the next scheduled message narrows to it. The index retains the latest
schedule, run and handover and treats schedule.run as a history boundary.

Checks: calendar coalescing, all five repeats and summer-time gaps/folds
including Lord Howe; core per-run spend across resume, handover versus human
context and clearing Don’t ask with an actual confirmation; daemon startup,
coalescing, busy/external-log skips, idle closing and scheduler lease transfer;
RPC creation/removal, pause/resume authority, manual runs and archive; browser
creation without send, daemon preview, persistence, pause/edit/resume, run now
with an approved write, removal and phone layout.

No login item is built. The model proposing schedules, cron expressions and
log splitting remain outside the proposal.

## Amendment 2026-10-16: the handover's exchange stays out of the conversation

The handover note was written by asking the model in the live conversation:
the ask (compaction's summary prompt) and the note were recorded as a
harness message and an ordinary reply, so a person's follow-up in the
scheduled task's chat sent both to the model, and the note became the
agent's last answer (`request-context`, `FinalReply`), which the plan gate
then told its explorer and planner. `context.handover` now records `from`,
the first event of that exchange, beside `through`, and
`window.WithoutHandovers` leaves the events between them out of later
windows, `request-context` and the last answer. The next run still starts
from the note. The console still shows the note as it is written, after the
run's answer; that is not changed here. The regression is in
`TestScheduledHandoverWaitsForNextRun`.
