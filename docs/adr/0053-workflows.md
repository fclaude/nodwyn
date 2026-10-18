# ADR 0053: Workflows: a trigger, a filter, an instruction, where runs go, and what happens with them

- Status: accepted 2026-10-17; the timetable (§3) built the same day as an
  amendment to ADR 49, §11 (slice 0), slice 2, slice 3 (pull requests and
  commands), slice 4 (the outbox), slice 5 (Gmail) and slice 6 (the TUI and the
  command line) the same day, amended below; the rest not built (see "Not
  built")
- Date: 2026-10-17
- Amends: 49 (a schedule becomes a workflow whose runs all go to one task;
  schedules stop being read from every task's log), 30 (a daemon may start at
  login, and then outlives the app's window; see the amendment)

## Context

The person asked for scheduled tasks to become "more of a workflow
builder": to control timing (every 30 minutes, Tuesdays and Thursdays at
15:00), to trigger on events (review pull requests when they are posted,
process emails as they arrive), and for the console, the TUI and the daemon
to converge. A review of the console in the demo and of the code
(2026-10-17, at 66e8d06) found:

- a schedule could only be one of five repeats from one minute, and only a
  clock could start a run: `Due()` was the scheduler's only source;
- the zone was shown as "Local", the same next time was written three ways,
  a schedule had no history and no place of its own, and a new daily
  schedule ran at once (all fixed with the timetable, ADR 49 amended
  2026-10-17);
- the scheduler read every stored task's log every 30 seconds under the
  lifecycle lock (`rpc/schedule.go`), a cost that grows with history once
  events open a task each;
- nobody is told when a run needs them: notifications come from an open
  console page, the daemon stops when the app that started it closes, no
  login item exists (ADR 49), and a locked keychain skips runs;
- the daemon listens only on a Unix socket and the bridge has no route that
  takes an unauthenticated request; no code polls a forge or a mailbox;
- the guard sits on the Scheme interpreter (SPEC invariants 4 and 5): a Go
  poller calling `net/http` would be a new, unguarded way out;
- a `user.msg` is the task the judge and the plan gate weigh each call
  against (ADR 35 §3, ADR 19), so an email or a pull request's body sent as
  one would become the person's request.

The person read the proposal (published the same day) and accepted it.

## Decision

### 1. A workflow is five answers
**When** (one or more triggers: a timetable, something happening, or Run
now), **Only if** (filters on the event, checked by the daemon before any
model is asked, so filtering costs nothing), **Do** (the instruction, written
once, with Home's choices: folder or worktree, model, judge, helpers,
limits, spend), **Runs go to** (one task, or a task per subject) and
**Then** (what happens with the result). A schedule as ADR 49 built it is a
workflow with a timetable, its message, and every run going to its one
task.

### 2. Every run is still a chat's turn
ADR 35 and ADR 49 §1 hold: no unattended mode, no Don't ask, normal
approvals, the guard, the judge, the spend limit and the progress guard, and
the person can reply in the task.

### 3. Timing is a rule said in words
Built 2026-10-17 as ADR 49's amendment of that date: set times on chosen
weekdays, an interval within hours of the day, monthly dates or a weekday of
the month, once, a first minute and a last day, said back by the daemon in
a sentence with labelled next times and the zone's name. Not cron.

### 4. Events come from watchers that poll through the guard
Each event source is a watcher the daemon runs on the source's interval (2
minutes by default) in a Scheme interpreter with no model and nobody to ask:
whatever would prompt is refused, never waited on, so a watcher reaches out
only through what the person already allowed, a credential bound to the
API's host for GET or a command with a standing approval for that workflow.
A watcher returns items with an id and a version; the workflow's log keeps
what it has seen, so each new item or new version (a pull request's new
head commit) fires once. The first poll records what exists without firing,
unless the person asks for what is open now too. The sources nodwyn ships
are Scheme procedures in its default library (Gitea and GitHub pull
requests, Gmail, IMAP, a branch's head, a command's JSON lines), so a
person's own source is a procedure too, and the builder fills in a source's
arguments. Polling comes first because it needs no listener, catches up
after sleep and goes through the guard; signed webhooks on the tailnet's
address can come later for instant events, with polling kept as the
fallback.

### 5. The event is data, the instruction is the task
The instruction is the run's `user.msg`. The event goes in as an attachment
does (ADR 22 §3): framed as data, never instructions, and marked external
like an MCP result. The transcript shows it as a card ("Pull request #142
got new commits"). The judge is told the run was started by an outside
event.

### 6. Runs go to one task, or one per subject
A check every 30 minutes keeps one task (ADR 49's handover keeps it cheap).
A pull request or an email thread is a subject: its first event opens a
task named after it, and later events about it arrive there as its next
run. A run waiting on the person stops only its own subject. At most a set
number of runs go at once (2 by default); events wait their turn, while a
timetable's time that finds its task busy is skipped, as ADR 49 §6 has it.

### 7. Outward actions wait in an outbox
Posting a review or saving a reply is the host's step after the run, done
with a binding for exactly that call once the person says yes on Needs you
(Post, Edit, Discard, Open task). A run that read a hostile email has
nothing to send with. "Without asking" is a choice per workflow, made on
this computer. Notifications are chosen per workflow: every run, when it
needs the person, or when the run says so (a tool offered only in workflow
runs).

### 8. A workflow has a log of its own
`workflows/<id>.jsonl` in the data directory, written by the daemon holding
`scheduler.lock`, with an index derived from it as for tasks:
`workflow.define` (the latest counts), `workflow.seen` (each source's
cursor), `workflow.fire` (the event's summary, its subject, its task),
`workflow.skip`, and `workflow.outbox` with its fate. A task a workflow opens
records `session.origin` (workflow and subject). The scheduler's tick reads
the workflows, not every task. A task scheduled under ADR 49 keeps working
and is shown as a workflow of one task; it becomes a `workflow.define` when
first edited in the builder, and `schedule.*` stays as an alias for a
release. Nothing in a repository writes or enables a workflow (ADR 45, ADR
49 §9).

### 9. Only this computer makes or changes a workflow
A paired device may pause one, run it now, answer a run and decide on the
outbox. No primitive makes or changes a workflow: persistence is what an
injected instruction wants most (ADR 49 §12).

### 10. Spend and floods are bounded per workflow
A spend cap per run (ADR 49) and per day: a workflow that reaches its daily
cap pauses until the next day and says so. At most a set number of events
per poll; above it the workflow pauses and asks ("43 new emails since the
last check: handle them all?").

### 11. The daemon keeps running and says when it needs the person
A login item (a systemd user unit, a LaunchAgent), notifications from the
daemon rather than from an open page, with attention for paired devices,
and "Workflows paused: keychain locked" shown on Home.

### 12. The console has a Workflows page; the TUI and the CLI follow
A Workflows page beside Connections and Workspaces lists each workflow with
when it next runs or what it watches, what happened lately and what it cost
today; the builder shows the five answers as cards beside a live preview,
whose **Test the trigger** polls once and lists what would fire, without a
model and without recording anything. Home's Scheduled card becomes a short
list of workflows, and the outbox joins Needs you. The TUI (`:workflows`)
and the CLI (`nodwyn workflows`) list, pause, run now and decide on the
outbox, in the daemon's words.

### 13. The order to build it (all built 2026-10-17; see the amendments)
0. the daemon at login, its notifications, the locked-keychain state;
1. the timetable (built);
2. the workflow log, the Workflows page, the daily cap, today's schedules
   shown as workflows;
3. watchers, the pull-request and command sources, a task per subject, the
   event card, Test the trigger;
4. the outbox and notification choices;
5. email sources and draft replies;
6. the TUI and the CLI.

Each slice ends with its browser check, its row in `docs/clients.md` and an
amendment here.

## Consequences

- A workflow's events cost no model request until a filter passes.
- Credentials for writing back (posting, sending) are used by the host in
  the outbox step, never by a run's model, unless the person chose
  "Without asking".
- The Google profile needs `gmail.readonly` and the GitHub profile a
  repository scope before their sources work; the shipped profiles ask for
  `drive.file` and `read:user` (ADR 24).
- Fakes stand in for a forge and a mail server in tests (`test/fakes`), as
  for every other service.

## Not built

§13's seven slices are built (the amendments below say how, and where each
departs from the text above): the timetable, the daemon at login, the Workflows
page, pull requests and commands, the outbox, Gmail, and the terminal clients.
What this record still leaves open, each said in the amendment that built the
part it belongs to: a real Gitea, GitHub or Gmail (everything ran against
`test/fakes`); IMAP; "when the run says so" (§7); the judge told a run came from
an outside event (§5); watchers as Scheme procedures (§4: they are Go, with a
command source for a person's own); a workflow log for a timetable (§8: it
stays in its task's log). Not proposed: one workflow starting another, a zone
chosen per workflow, a cron line turned into a rule.

## Amendment 2026-10-17: slice 0, the daemon keeps running and says when it needs the person

Built, as §11 and §13 slice 0 have it:

1. **A login item, written only when asked.** `internal/startup` writes a
   systemd user unit (`nodwyn.service`, wanted by `default.target` through
   the link `systemctl --user enable` would make) on Linux, or a launchd
   agent (`nodwyn.daemon.plist`, run at load and again on a failure) on
   macOS, running this nodwyn's `daemon` with the `PATH` and XDG homes it
   has: a daemon started at login has the system's short `PATH`, and the
   tools tasks run are often elsewhere. `nodwyn startup [on|off]` and
   Settings › General › While you're away (`startup.read`, `startup.write`)
   turn it on and off; both are this computer's, refused to a paired device
   by the daemon and the bridge (ADR 30). It never starts or stops a daemon:
   the one it writes starts at the next login, so it does not race the
   app's. A file at its place nodwyn did not write is left alone, both ways,
   and a `go run` build is refused, since it is gone when it exits. Who
   writes the file: nodwyn, on the person's request, in the person's own
   home; nothing in a repository and no model reaches it, and the daemon
   offers it only when its command gives it a place (`WithStartup`), so
   tests and the browser demo never write a real one.
2. **ADR 30's lifecycle, amended.** A daemon the app starts still closes
   with its window. A daemon started at login is the one the app finds and
   uses, and closing the window leaves it running with its schedules.
   A login item restarts a daemon that failed, which finds the socket its
   crash left: `rpc.Listen` removes one nobody listens on, for every caller,
   under a lock (built the same day beside this, ADR 21 amended; this slice
   had its own copy of the check, dropped when the two met). The daemons
   `nodwyn tui` and line mode start when none answers (ADR 30, amended the
   same day) get the same login item and notices as the app's.
3. **The daemon's own desktop notices**, off unless a person turns on
   `[notify] desktop` (Settings › General), as a browser's are. With fixed
   words, never a task's, as the app's notification has (a lock screen shows
   them): "nodwyn needs your answer" when an open task's state turns to
   needing one while no client of this computer (one without `via`) is in
   `session.watch` (a console watching notifies by itself; a paired phone's
   page is not this computer's), and "nodwyn can't run a scheduled task"
   once when a locked keychain makes a scheduled run skip, until the
   keychain is unlocked; a person's Run now says nothing, since they are
   there. `notify-send` on Linux, `osascript` with the words as its
   arguments on macOS. Only a daemon its command gives a notifier
   (`WithNotify`) shows any, so tests and the demo never do.
4. **Locked keys holding back a run are said where the schedules are.** A
   daemon started at login has no terminal to ask for the passphrase, so its
   keychain starts locked. `schedule.read` and the listing add `held` when
   the next run would be skipped for it (the task is closed, the keychain
   locked, the model needs a key); Home's Scheduled section and the card say
   so, and the unlock form at Home's top unlocks it and reads the list again.

Not built: notices pushed to a paired phone (its page shows them while open,
as before); Windows; starting the login daemon at once; the TUI saying a run
is held. Checks: `internal/startup` (both kinds, quoting of specifiers and
variables, a file nodwyn did not write, relative and temporary programs),
`internal/frontend/rpc` (`away_test.go`: a notice when no local client
watches, none while a console does, a phone not counting; off by default and
saved by `settings.write`; the locked-keychain notice once per locked
period and `held`; `startup.*` refused to a device, on and off, none offered
without a place or for a temporary build), `cmd/nodwyn` (the stale socket),
and the `away` browser check (Home's held schedule and its unlock, start at
login turned on and off in the demo's own home, the notices saved and read
back).

## Amendment 2026-10-17: slice 2, the Workflows page and a daily spend limit, with a timetable kept in its task

Built: a **Workflows** page (the sidebar, and **All workflows** on Home's
Scheduled section) lists every schedule as a workflow whose runs go to one
task: its task, the rule in the daemon's words, when it runs next or why
not (paused, archived, held by locked keys), what today's runs came to and
how the latest ended, with Pause or Resume, Run now and Schedule…. **New
workflow** opens Home's Schedule dialog, so a workflow starts as any task
does, with its folder, worktree, model, judge, helpers and limits: there
stays one way to start a task. A schedule has a **daily spend limit**
(`day_cost`, 0 for none) beside the one per run: once what the day's runs
cost (their follow-ups included) reaches it, the day's remaining automatic
times are skipped in one `schedule.skip` standing for all of them, and runs
go on the next day; Run now, a person's choice, is not held to it. The log
index folds the day's runs and cost (`today` in `schedule.read`; version 8),
so a run every five minutes needs no walk through the log to check it.

Changed from §8: a timetable whose runs all go to one task keeps its
definition in that task's log, as ADR 49 has it, and has no workflow log.
The task's log already holds what such a workflow needs (its runs and
skips, the per-run spend base, the handover, its history and today's
sums), and a second record of the same definition would be one to keep in
step for nothing; the house rule is to add nothing a change does not use.
A workflow log comes with slice 3, for what a task's log cannot hold: an
event source's cursor, the tasks of each subject, and the outbox. The
scheduler's tick still reads every task's schedule from the index, which
answers from memory while a log is unchanged; the tasks of a workflow with
a task per subject will carry no schedule of their own.

Not built in this slice: the builder's cards as one page (the Schedule
dialog is the When card until event triggers need the rest), a cost
estimate per day. Checks: `internal/core` (today's runs and cost in the
history test), `internal/frontend/rpc`
(`TestADailySpendLimitSkipsTheRestOfTheDay`), and the `schedule` browser
check (the daily limit sent, the Workflows page reached from Home and the
sidebar, its row before and after Run now, New workflow opening Home's
Schedule dialog).

## Amendment 2026-10-17: slice 3, a workflow that watches pull requests or a command

Built: **Workflows › Watch pull requests** makes a workflow whose events come
from a Gitea or GitHub repository's open pull requests, or from the JSON
lines a command prints (§1, §4, §5, §6, §8, §10, §12). It is checked every 1
minute to 24 hours (a tick every 30 seconds at the soonest); what is open at
its first check is recorded and not acted on, unless the person asks
(`existing`); an item new to the source, or whose version (a pull request's
head commit) changed, is an event; filters on author, base, head, draft,
labels and title (a command's own fields too) leave events out before any
model is asked; each event that passes runs a task, in a task per subject
(its first event opens a task named `#142 title`, with `session.origin`, and
a later one is its next run) or in one task. At most 2 runs of a workflow go
at once (settable), the rest wait for the next check unmarked; more than 20
events at one check pause the workflow and say so; a daily spend limit counts
what all its tasks' runs cost today; a source that cannot be checked says so
once. **Test the trigger** asks the source once and lists what is there and
whether each passes, with no model and nothing recorded. Only this computer
(or a passkey-signed person) makes, changes, removes or tests one; an invited
device sees, pauses and checks one.

How the sections are met, and where this departs from them:

1. **§5, the event is data.** A run first attaches the event as a file
   (`pull-request-142.md`, actor `workflow NAME`), which the window frames as
   data, never instructions, and tags external as any attachment is, then
   sends the workflow's message as the run's request. A pull request whose
   description says to print the keys reaches the model as the quoted text of a
   file the workflow attached; the browser check and `TestAWorkflowRunsATask…`
   use exactly such a body. The conversation says "Attached by workflow
   “NAME”", never "You attached", and the run's marker says "Workflow run ·
   Pull request #142 opened". **Not built:** telling the judge the run was
   started by an outside event.
2. **§4, the watchers are Go, not Scheme.** §4 had them as Scheme procedures
   in a library, run in an interpreter that refuses what would prompt. They
   are `internal/watch`, in Go, with the guard's own checks applied where
   they apply to the poll: a forge is asked only with a key from the keychain,
   only where the binding table (read afresh for each poll) allows that class
   at that host for GET at the position the forge takes (`header:Authorization:token`
   for Gitea, `...:Bearer` for GitHub), by the same `binding` lookup the
   guard makes for `http-request`; the request is a GET of the pull-request
   listing and nothing else; no redirect is followed, so the key goes to the
   bound host and no other (`TestAPollDoesNotFollowARedirectWithItsKey`); the
   body is bounded; a locked keychain, a missing key or an unbound host is an
   error said on the workflow. This keeps SPEC invariant 5 by the mechanism
   that fits a poll with no model in it, but it is a different one from the
   interpreter's, and the choice is recorded here rather than hidden: a source
   of the person's own is a command source, not a procedure.
3. **A command source** runs the program the person named, with no shell, in
   the workflow's folder (which must pass the task-root refusal), with the
   daemon's environment, for at most a minute, its output bounded. A program
   is an absolute path or a name on `PATH`: one with a slash that is not
   absolute is refused, since a program in the folder would be whatever its
   repository holds, written by whoever can commit to it (AGENTS.md). Who
   writes the definition: a person at this computer, in nodwyn's data
   directory; no repository, no model and no device. The command is the
   person's own act, as an MCP server entry is, and is not asked about again.
4. **§8, a workflow log, now built for what a task's log cannot hold.**
   `workflows/<id>.jsonl` holds `workflow.define` (the latest counts),
   `workflow.seen` (each item's id and version at the last check),
   `workflow.fire` (item, version, kind, subject, task, summary) and
   `workflow.skip`, redacted as it is written, by the daemon that holds
   `scheduler.lock`. The tick reads these logs (a handful), not every task's.
   The outbox's records come with slice 4.
5. **§6, events wait their turn** is built as: an event that cannot start (its
   task busy or waiting for an answer, all the workflow's runs going, the
   keychain locked, the day's limit reached) is not marked seen and is tried
   at the next check.

Not built, and not verified: the sources were run only against fakes
(`test/fakes/forge` answers both forges' listings with the headers each takes),
not a real Gitea or GitHub, so a real server's differences are untried; a
listing is the 50 most recently updated open pull requests, with no paging; a
GitHub source needs a token with a repository scope, which the shipped GitHub
OAuth profile does not ask for, so a personal token is stored as a credential
instead; the judge is not told a run came from an event; opening a source of
the person's own as a Scheme procedure; filters on which paths changed; the
outbox (slice 4); email (slice 5). Checks: `internal/watch` (the listings
through the binding table in both forges' ways, every refusal, no redirect,
JSON lines, the changes and filters), `internal/frontend/rpc`
(`TestAWorkflowRunsATaskForEachPullRequest`, with a mutation check that
removing the attachment fails it; `TestEventsWaitTheirTurnAndAFloodPauses`;
`TestAWorkflowsDailySpendLimitHoldsItsEvents`; `TestWorkflowsAreMadeOnThisComputer`),
the `workflows` browser check, and the bridge's classification of every method.

## Amendment 2026-10-17: slice 4, the outbox

Built (§7): a workflow on pull requests has a **Then** card. The run's answer
stays in its task, or is posted as a review comment on the pull request. A
posted answer is held in an outbox, shown on Home under Needs you as a card
("Ready to post · Review for #142") with **Post review**, **Edit**,
**Discard** and **Open task**, and the host posts it only when the person
says so; "Without asking" has the host post it as the run ends, a choice made
on this computer when the workflow is made. Who sends: the run's model never
does. When a run's turn ends, the host reads its answer from the log (the last
reply, if the turn completed and said something), writes it as a
`workflow.outbox` record, and, on a decision, posts it itself: a `POST` to the
forge's review endpoint of kind `COMMENT`, with the workflow's key, only where
the binding table allows that key at that host for **POST** at the position the
forge takes it (the same lookup the guard makes), no redirect followed. A key
bound only for GET, which lists pull requests, cannot post: the two are
separate grants, said in the builder and in the refusal. A review that
approves or asks for changes is never posted by a workflow: those are the
person's.

- **Who decides:** `outbox.decide` post or discard is a paired device's to
  make, as answering a question is; **the words** of an answer are changed on
  this computer only (the daemon refuses another body from a device), so a
  phone can post what the run wrote and not something else with this
  computer's key.
- **Records:** `workflow.outbox` (id, kind, item, subject, task, summary,
  address, body, time) and `workflow.delivered` (id, fate: posted or
  discarded, by whom, "edited before it was posted") in the workflow's log;
  a post that fails stays in the outbox with the reason, and a run that did
  not finish, or said nothing, queues nothing and says so in the log.
- **Notices:** a held answer is a `needs your answer` notice (ADR 53 §11,
  fixed words) when no window of this computer watches; a workflow may ask
  for "nodwyn ran a workflow" after each run, or for no notice. The topbar
  counts held answers among what needs you; clients refresh on a pushed
  `outbox.changed`.
- **Not built:** "when the run says so" (§7), a tool offered only in workflow
  runs by which a run asks to be told: the choice is when it needs you, after
  every run, or never; posting a reply to an email (slice 5); a held answer
  that a daemon restart cut off from its run (the pending run is in memory:
  the run's answer stays in its task, and nothing is queued); the sidebar's
  Needs you list does not list held answers, only Home and the count do; a
  real Gitea or GitHub has not been posted to, only the fake.

Checks: `internal/watch` (`TestAReviewIsPostedOnlyWhereThePostIsBound`: both
forges' ways, refused with only GET bound, no number but a pull request's,
a locked keychain), `internal/frontend/rpc` (`outbox_test.go`: held and not
posted; refused without the POST binding and kept; a device posts but cannot
edit; posted once; discarded; edited then posted with the record saying so;
without asking posts as the run ends, by "workflow"; without asking with
nothing allowed waits, and says why; the notice after a run; what a workflow
may do with its answer is validated; a mutation that posts without asking
fails the first of them), and the `workflows` browser check (the Then card,
the held card on Home counted in the topbar, edit and post with the forge
seeing the person's words, discard posting nothing).

## Amendment 2026-10-17: slice 5, email through Gmail; and a correction to slices 3 and 4

Built: **Email in Gmail** is a source (§4). Its answer can be saved as a
**draft reply** (§7).

- **Items.** A thread is the subject. The newest messages of the search are
  listed (25), each thread is an item whose id is the thread's and whose version
  is its newest message's id: a new message makes an event, and reading,
  starring or archiving a message does not, which a thread's history id, that
  changes with every label, would. A new email opens a task titled by its
  subject; a reply arrives in it as its next run. A message is read in full
  only for a thread that is new or changed, and not at all by the check that
  only records what is there. The event is attached as `email-….md` (sender,
  recipients, date, and the plain text, or an HTML part with its tags taken
  out, at most 20 KiB), as data, never instructions; filters read from, to,
  subject, labels and date, so `from does not contain "me@…"` leaves out the
  person's own replies.
- **Draft replies, never sent.** The held answer is an outbox item of kind
  `draft` with the reply's address, subject, message id and thread; the daemon
  saves it as a Gmail draft in the thread, to the sender, with `In-Reply-To`
  and `References`. Sending is the person's, from Gmail: no part of nodwyn
  sends mail. The sender's name, address and subject are a stranger's text:
  the address is parsed (a sender that is not an address gets no draft) and
  every value has its line breaks removed, so no header can be added
  (`TestADraftReplyJoinsTheThreadAndAddsNoHeader` builds one from a hostile
  subject and message id).
- **Credentials and scopes.** Reading needs a Google credential authorized for
  `gmail.readonly`, saving a draft one for `gmail.compose`, which is a separate
  authorization because it writes, each allowed at `gmail.googleapis.com`
  (GET, POST) at `header:Authorization:Bearer`. The shipped Google profile
  (drive.file only) is **not** changed: the person authorizes with
  `--scopes gmail.readonly` and, if they want drafts, `gmail.compose`, since a
  default scope granted to every Google credential would be a widening nobody
  asked for. A 401 and a 403 from Gmail are said with what is most likely
  wrong (expired or revoked; a scope or the API not turned on).

**Correction.** Slices 3 and 4, as pushed, made the binding-table lookup
themselves and sent the key: they did not apply the rule against plain HTTP to
a remote host, did not look at the credential's granted scopes against the
row, and did not refresh an OAuth2 token about to expire. Every poll and post
now goes through `core.Keyed.Authorize`, the check a task's own model requests
pass (`internal/watch`'s `Poller` takes it as `Authorize`), so those three
apply, and the daemon builds it per poll from the table as it is then.
`TestAKeyDoesNotGoOverPlainHTTPToARemoteHost` fails on the code before this
(a key went to `http://git.example.com`). A Gitea reached over plain HTTP on
a tailnet is therefore refused unless Settings allows credentials over remote
HTTP, as for a model's key.

Not built, and not verified: **IMAP**. It needs a mail-protocol client, which
is a dependency this repository does not have, and a person's mailbox password
in the keychain instead of a scoped OAuth2 token; adding either is a decision
of its own. The Gmail path was run only against `test/fakes/gmail`, not a real
mailbox: a real response's shapes (nested multipart messages, attachments, odd
encodings) are untried, and attachments are not read. Not built: marking a
message read or labelling it, sending a reply, a thread's earlier messages in
the event (only the newest is attached; the run can read the rest by asking).
Checks: `internal/watch` (`mail_test.go`: items and versions, what is fetched
and what is not, the refusals explained, the draft's headers), `internal/frontend/rpc`
(`TestAnEmailRunsATaskAndItsAnswerWaitsAsADraft`), and the `mail` browser
check.

## Amendment 2026-10-17: slice 6, the terminal clients

Built (§12): `nodwyn workflows` and `nodwyn outbox`, and the TUI's
`:workflows` and `:outbox`, list what the console's Workflows page and Needs you
hold and act on it. One package, `internal/frontend/workflows`, reads the
daemon's `session.list` (the timetables: scheduled tasks with a schedule that
are not archived), `workflow.list` and `outbox.list` into a `Listing`, lays it
out, and acts on it, so the command line and the TUI cannot say anything the
other does not. It decodes the daemon's views as `rpc.WorkflowView` and
`rpc.OutboxView`, which are now exported from `rpc` for that (as
`rpc.SessionInfo` is for the TUI): the structs are the daemon's own, so they
cannot drift from it. A workflow or held answer is named by a reference, the
last eight characters of its id, shown in brackets in the list; a reference
that matches none, or several, is refused. The actions are the console's
requests: `workflow.pause`, `workflow.check`, `schedule.pause` and
`schedule.run`, and `outbox.decide` post or discard, so what the daemon allows
is what it allows to any client of the socket (this computer's).

A deliberate gap, recorded in `docs/clients.md`: **a terminal makes and changes
no workflow and edits no answer.** The builder's cards and its live Test the
trigger, and the editor for an answer, are the console's; a terminal that
could send other words than a run wrote would hold the one door a stranger's
text might otherwise use. The TUI also still has no Needs you across tasks, so
a held answer is seen with `:outbox`, not in the title (`clients.md`, not
built, in order: item 1).

Checks: `internal/frontend/workflows` (the layout, references that mean one
thing, the requests sent and no body of its own, timetables listed and acted
on), `cmd/nodwyn` (`TestWorkflowsAndTheOutboxFromTheCommandLine`, a real
daemon: a timetable and a watching workflow listed, paused and resumed, a held
answer discarded and recorded, a reference that means nothing refused, no
daemon said), and `internal/frontend/tui`
(`TestWorkflowsAndTheOutboxThroughTheDaemon`).


## Amendment 2026-10-17: the settings methods are `.write`

`startup.set` and `workflow.set` are `startup.write` and `workflow.write`
(and `workflowSetParams` is `workflowWriteParams`). They had followed
`schedule.set`, the nearest neighbour; the rule in `docs/protocol.md` (Naming)
is that a change to a setting or to a definition is `.write`, and a method
that came first and breaks the rule is not a reason to break it again. They
were renamed in the commit that ends the programme, before any released client
could call them: the console, the line mode, the TUI and the checks are
all this repository's, and `grep` for the old names finds none. The acts that
are neither a read nor a write keep their verbs (`workflow.pause`,
`workflow.check`, `workflow.test`, `outbox.decide`), as `schedule.pause`,
`models.test` and `session.answer` do.
