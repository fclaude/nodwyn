# ADR 0052: Workflows: a trigger, a filter, an instruction, where runs go, and what happens with them

- Status: accepted 2026-10-17; the timetable (§3) built the same day as an
  amendment to ADR 49, and §11 (slice 0) the same day, amended below; the
  rest not built (see "Not built")
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

### 13. The order to build it
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

Everything but §3 and §11, in §13's order. Not proposed: one workflow
starting another, a zone chosen per workflow, a cron line turned into a
rule.

## Amendment 2026-10-17: slice 0, the daemon keeps running and says when it needs the person

Built, as §11 and §13 slice 0 have it:

1. **A login item, written only when asked.** `internal/startup` writes a
   systemd user unit (`nodwyn.service`, wanted by `default.target` through
   the link `systemctl --user enable` would make) on Linux, or a launchd
   agent (`nodwyn.daemon.plist`, run at load and again on a failure) on
   macOS, running this nodwyn's `daemon` with the `PATH` and XDG homes it
   has: a daemon started at login has the system's short `PATH`, and the
   tools tasks run are often elsewhere. `nodwyn startup [on|off]` and
   Settings › General › While you're away (`startup.read`, `startup.set`)
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
