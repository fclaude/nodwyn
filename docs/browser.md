# Browser view

## Home and a task

The console is organized around tasks (ADR 21's design canvas): **Home** starts
a task in a folder and lists what needs an answer, what is running and what
finished; a **task** shows its conversation with the approval inline, and
beside it a compact monitoring summary above four tabs. Worktree tasks
also have a **Review** button which opens their changes in the centre panel.

The task's heading shows the one action for the state the task is in, **Stop**
while it works (it is not there when there is nothing to stop) or **Resume**
once it is saved, and one **⋯** menu for the rarer ones: **Search**,
**Schedule…**, **Close** (an open task), **Archive** or **Unarchive** and
**Delete…** (a saved one).

## The monitoring summary

The summary keeps total task spend, current turn
time and context fullness in three fixed positions across tabs; the tool
calls are in Statistics › This turn and the cache rate is Statistics →
Totals' **Token cache rate**: the rounded, token-weighted share of raw prompt
tokens read from cache across the session, not the percentage of requests
hitting cache (missing or unreported usage is excluded; no positive prompt
total shows **unknown**, reported zero cache reads give **0%**; with truncated
history it covers the log that remains). A task whose requests the log cannot
price shows **unpriced** for spend, not "at least $0.0000". Other unknown
figures stay as a dash. Context shows a percentage against the recorded high
water, or an estimate when no high water is known. A reserved warning line flags tokens, time, tool calls,
spend or context at 90% of their displayed limit, and says when that limit
has been reached. These are monitoring hints, not new enforcement rules.

## The bottom tabs

**Progress** holds the plan; **Statistics** holds this turn's budget and the
task's totals; **Changes** still holds recorded write approvals, each
file's kept versions and attachments; **Workspace** holds where it works,
the tools it was offered, Details inspectors and retained project notes.
**Review** replaces the centre conversation with a worktree task's actual
changes since it branched, to comment on; **Close review** restores the
conversation and its reply draft. It stays separate from Changes' approval
previews and recovery tools. The four tabs sit above what they switch, at 12.5
px on desktop and 44 px tall on a phone, on one row at both desktop panel
widths, with visible keyboard focus. On desktop only
the selected content scrolls, so expanding a plan step cannot move the
summary or the tabs; each tab keeps its scroll position through
redraws and tab switches. On narrow screens the summary, content and
navigation stack with ordinary page scrolling.

## The plan

The plan shows the
model's progress through
it: the steps it marked done (with what it says shows it), the ones it
skipped, and the next. A step folds to its title, the clause up to its
first dash, and the mark's note waits behind the fold too, so the plan
reads at a glance; clicking a step shows the whole of it, the note with it.

## Statistics

After each reply the conversation shows its tokens,
and when the provider reports them or the model has prices, how much of the
prompt came from the cache and what it cost (the manual, "Choosing models");
over several requests, the largest single prompt too, since each request
sends the whole prompt again. Statistics' Context meter shows how full
the context the model holds is against the window's high water, from the
start of a task: while the explorer, the planner or a sub-agent asks in a
context of its own, the meter follows it and names it (*Context ·
explorer*), and shows the conversation's again once it folds; what the
provider counted and how often it was compacted are in its tooltip, so the
panel does not shift on every reply. "Tokens used", shown from the turn's
start, is the token budget's
count: each request's prompt and reply, a prompt token the provider read
from its cache counted as a tenth. Beside them the turn's clock is split: "Model wait",
each request to a model from its sending to its reply, and "Tool time",
each tool call from its start to its result, the wait for your approval
left out. Only a figure with a limit behind it is a bar (Time, Tool calls,
Tokens used and Context, and the total spend when a spend limit is set);
the bars come first and the rest are plain rows under them. Statistics' **Totals**
includes a plain "Token cache rate" row (defined in [the manual](manual.md)).
The totals' aggregation is
kept in the daemon's derived log index (`session.totals`), together with spend,
judge usage, speed, clocks, approved previews and attachment metadata. Recorded
events refresh the snapshot; clock ticks extend only its open clocks while the
task runs. Statistics also shows turn counts and, for CLI driver runs, run
counts; their tooltips give the latest start time. Closed tasks need no resume
to read these figures. Older daemons without this method retain the browser's
whole-log calculation.

## History in a task

The conversation opens at its latest indexed run. Earlier runs fetches five
older last-reply previews at a time; a run's event pages load only when it is
expanded, within a fixed sequence range. Closing and reopening a loaded run
reuses its content. Failures can be retried without disabling the live task;
replies to a former task cannot replace the current conversation. A latest-run
subscription selects its history and live watermark together. Historical runs
have separate renderers, so old choices and turn records do not change the
current model, prompts or gauges. The Log and Plan record inspectors label
their latest-run scope. A task without run records retains its whole
conversation view; earlier conversation before its first run remains reachable.
Run markers include CLI driver starts and `schedule.run`.

## Schedules

Home’s **Options › Schedule…** opens a schedule form beside the ordinary ask
choices, without sending immediately: its message is a required field, started
from the composer's text, so it works with the box empty. When a task cannot
start yet (no folder, a locked keychain, a model that cannot be used) the
button is disabled and says why. **Schedule…** in a task's **⋯** menu, with or without a schedule, opens its
saved definition and controls; **Remove schedule** asks first. The dialog is
titled **Schedule** here and on Home: scheduling has that one name, and
**Repeat** is only the field for how often.
`schedule.next` supplies the preview, which reads as "Next: tomorrow 9:30 AM,
then every day" on the schedule's own wall clock (the browser's zone does not
move it); the full list, ISO times with the daemon’s local offset, is its
tooltip. The dialog shows the status, the message and that line, with **Close**
at its top right and the edit form folded behind **Edit**. Schedule creation and
changes are local-only; devices can pause and run now. A pause survives reload,
as do edits and removal. Runs discard Don’t ask and use normal approvals.
Home lists next times and paused schedules, each card with Pause or Resume and
Run now. The daemon coalesces missed times,
records skips and closes idle scheduled sessions after 15 minutes; see the
manual and ADR 49 for log-lock deferral and startup requirements.
Whole-task spend stays in Totals; an open scheduled task also shows This run
and uses its per-run amount for spend warnings. The `schedule` browser check
performs creation, reload, pause, edit, resume, run now with an actual write
confirmation, removal and the phone layout.

## Activity and compaction

A compaction is
a note behind **Activity**: what set it off, and the context's size before
and after. Routine activity (judge requests, focused contexts, instructions
to the model, groups of calls and the token line) shares one closed
**Activity** row per stretch between replies; click it to see it all. Its
summary follows the work without opening, and counts a failed call in red
("1 failed"), whose run or group is open inside. A call that was skipped
without running (an explorer out of rounds, the rest of a batch after a
failure) did not fail: it is a note in the row that says why, one note for
the calls skipped in a row for the same reason. Replies, warnings, errors,
a turn that stopped short and a worktree that left uncommitted files out
stay in view; a question is asked in its card at the end of the
conversation. This folds the display only: `/compact [focus]` changes the
model's context.

## Other pages

**Connections** (MCP tools and credentials),
**Workspaces** and **Settings** hold the rest; Settings also creates,
unlocks and sets the passphrase of the keychain. The
U3 inspectors described below sit under a task's **Details**, in the
Workspace tab. For the app
window and first-run setup see [app.md](app.md).

## Messages and failures

Messages sit at the top of the window, clear of the composer. A confirmation
("Allowed once.") is a toast that goes after four seconds, one at a time. An
error stays until it is dismissed, is announced as an alert and is not
replaced by the next message: it often says what not to do next ("check the
conversation before sending again"), and a person looking elsewhere must still
find it. An identical error is shown once, and one that can be tried again
has a **Retry** button. A read the desk makes on its own (the task list,
what waits for you, the settings, the workspaces, the models' check) that
fails is such an error, not a silent gap: only a daemon that does not offer
the method (`conn.has` is false) is treated as an older one. A page that
throws while it is drawn is replaced by its failure, with the message,
**Reload** and **Home**, instead of leaving the previous page under a sidebar
that already moved on; an error nobody caught is shown the same way as an
error toast. A lost connection says so on its own banner and is not reported
once for every read it cut short.

## Settings

Settings is what tasks start with when nobody says otherwise, kept in
`config.toml`; every other line of the file, comments included, stays as
you wrote it. It is five tabs, each at an address of its own
(`#/settings/judge`), so a link opens one and a reload keeps it;
`#/settings` is the first. Only the tab shown is drawn, and the bar of
unsaved changes keeps those made in every tab. The tabs:

- **General** — Approvals, and under a fold called Advanced, what most people
  never change:
  - **Approvals** — what a task does without asking you (ADR 35): whether it
  writes files in its folder without asking (the default) or asks first,
  and who answers a command no allow list covers: the judge, then you when
  it refuses or there is none (the default); you; or nobody, and it runs;
  whether tasks offer **Don't ask**; and, once a Google credential is stored,
  how uploads to Google Docs are handled (the manual, "Documents out"). A
  write to a protected file, content that looks like a credential and a
  command built from what a tool returned always ask. The transcript says
  which calls went ahead, which the judge allowed, and why it refused one.
  - **Advanced** — **Limits**: each turn's tool calls, tokens used, actions
  and time: where it stops, and, if you set it lower, where it asks first
  whether to go on (by default it doesn't); the spend limit on a whole
  task, in dollars, off until you set where it stops; and how long a model
  reply and a command may take. **Context window**: the water marks between
  which a task's context is summarized or trimmed (unset, they follow each
  model's context less its reply cap; set, they shrink only for a model that
  cannot hold them), the largest tool result shown whole, and the fallback
  reply cap of a model that has none of its own and none its provider
  publishes. **Containers**: podman, docker, or whichever is installed, for
  contained MCP servers. The fold is closed, and open while a change waits in
  it. The addresses `#/settings/context` and `#/settings/tools`, which were
  tabs of their own, open it and are replaced by `#/settings/general`.
- **Models** (ADR 41, ADR 47) — two things.
  *Default for new tasks* is a model from the list and its default effort, as
  a task chooses them; it waits in the bar like the settings (the judge new
  tasks ask is the Judge tab's, which this row links to).
  *Providers and their models* lists each model server as a card with the
  models on it under it, each with whether it can
  be used and, when not, the sentence that says what to fix (needs its key,
  keychain locked, key not allowed, not answering); **Test** sends it one short
  request and shows the reply or the provider's refusal; **Check again** asks
  every server now; **Add a provider** and **Edit** take a preset (which says
  the kind), an address, a dialect, a name (under a **More** fold, when
  adding) and an API key (kept in the keychain, bound for the provider's
  requests and its model list, never in the page again; the form takes a key
  for a provider that has none, and a stored key's value is changed under
  Credentials, which the form links to), with presets that only fill the form.
  The models under each provider are the list the pickers offer, each with the
  efforts it takes and
  a line saying its context length and prices; **Add a model** and **Edit**
  take a provider, a model id (suggested from what the provider lists, asked
  with its key as the form opens, and offered as buttons when the list is
  short) and, under a **More** fold that is closed when adding and open when
  changing, its efforts (*As published*, the provider's, once the id is
  given and `models.published` describes it), what it is for and the effort
  it starts at; and its own settings (ADR 41,
  amended 2026-10-05), each with what the provider publishes as its
  placeholder: its context length, its reply cap, which comes out of it, with
  a line under both saying what the conversation is left with as they are
  typed, its prices, which price a request when the provider reports no cost,
  and whether it takes images (what its provider publishes, else yes, until
  you choose); under *More settings*
  its temperature and seed, and for a chat-completions model which
  field caps its reply (not on the Responses API, which reads none and has
  no seed, as its hint says), how its requests name their session and whether they
  ask the provider to cache the prompt, which a Claude model needs (the
  manual, "Keeping the cache warm"), or for Ollama how long it stays loaded.
  The default model's settings are its entry's, so editing it changes the
  model wherever it is chosen. These forms are drawn from state kept for the
  visit, so the readiness checks that arrive while one is being typed into
  redraw the page without losing what was typed, and leaving Settings drops
  it, a key included.
- **Judge** (ADR 46, ADR 47) — what new tasks ask, and the judges set up.
  *Status* says which judge new tasks ask and whether it can be asked: its
  key stored, unlocked and allowed for its host, as a provider's key is
  checked (the judge's service is not asked, so ready is not yet its answer),
  or why not. *Default for new tasks* chooses among them, or none. *Judges*
  is a card for each judge set up, with its host, its model and its price
  (both known: its provider's judge model, and the price nodwyn keeps for its
  questions), and, for Jev, **Replace the key** (a link to Credentials) and
  **Remove**. A judge not
  set up yet is offered in place: paste Jev's key, or an OpenAI API key, and
  that is all; one added when there is none is chosen as the default, which
  waits in the bar like the rest. A ChatGPT
  plan's server is no judge, since the Decisions API takes an API key, and
  the OpenAI card says so. Under a fold called **Tuning**, closed unless
  something in it is set: *What it sees* (the task and plan, or also what
  calls would send; the judge is turned off by *No judge*, and a hand-written
  `judge.content` of `none` or `plan+summaries` is shown as such until one of
  the two is chosen), *Allows a command at*, *Waits for an answer* and the
  default judge's own prices, each empty for the published one. A `[judge]
  model` its provider does not answer to is said, with a button that clears
  it.
- **Keys & devices** — the keychain (its create and change forms say the
  8-character minimum, check it before sending, and keep what was typed when
  the daemon refuses, so a wrong current passphrase does not empty the new
  one; what is typed is forgotten once it worked, and an unlock forgets it as
  it sends); **Credentials over remote HTTP**; and the
  paired devices, each with **Revoke**, which asks first, and, when none is
  paired, how to pair one (`nodwyn ui --listen`, which prints the pairing code
  in the terminal it runs in).
- **This browser** — its appearance, whether it shows the model's thinking,
  its notifications, and this connection.

**Saving.** One rule, said at the top of the page. A choice made in Settings
waits in the bar at the bottom of the page and is saved with the others:
the settings of every tab, the default model and effort, and the answer for
uploads to Google Docs. Acting at once are keys and the passphrase (a secret is
never staged), a provider or a model, each its own form with its own Save, and
the choices of **This browser**, which change nothing on the computer.

A field left empty takes its default, which it shows; clearing one puts the
default back. The daemon checks the settings together as it would on loading
the
file, so a soft limit can be raised past the old hard one along with it.
They apply to tasks started afterwards; an open task keeps the limits and
model it started with, and its Budget details say which. A task's model and
effort are changed under its own message box (below). The judge's address,
its credentials, the keys of the providers (beyond saving one with its
provider) and the other `[policy]` entries are set where they are reviewed, or in
`config.toml`.

**Credential transport.** Keys & devices offers **Credentials over remote HTTP**,
set to **Require HTTPS** by default. **Allow HTTP** saves
`policy.allow_insecure_http = true` in user configuration. New/resumed tasks take
that choice; provider tests and model lists use the daemon's current choice.
Loopback HTTP remains available with the switch off. Credentials on remote HTTP
can be read or changed along the route. Bindings, redirect refusal and TLS
certificate checks still apply; the separate MCP, OAuth endpoint and Google
upload URL rules are unchanged.

**Project startup.** The console opens an untrusted project's tasks with the default
loop, skipping its init and approval files and showing a note. Review and accept
them using `nodwyn trust --repo PATH` from this computer's terminal. A changed
revision needs review again before reload/resume. There is no console grant-trust
button or remote grant-trust API. Existing tasks must be restarted after revocation
to discard their loaded code and grants.

**The model picker.** Under the message box of the New task window, and under
a conversation's, a **Model** and an **Effort** select. Both list the models
of Settings grouped by provider, each labelled with what is wrong when it
cannot be used, and the efforts that model takes (*Not configurable* when it
takes none). Choosing another model keeps the effort when the new model takes
it, else takes that model's own default, and says so. Beside them, where a
judge is set up and on this computer, a **Judge** select (ADR 47): *No judge*,
then each judge of Settings › Judge, labelled with what is wrong when it
cannot be asked. A judge that cannot be asked does not stop a task, since you
answer instead; the lines under the picker say why. A conversation's summary
says which judge it asks ("judged by Jev", or "no judge").

- *New task.* It starts on the default of Settings. A choice is kept while
  another page is shown and in this browser across a reload (an unsent choice,
  like the message), dropped when the task starts or the default is picked
  again, and never changes the default. A choice left alone is not sent. A
  model that cannot be used is said so and a task is not started on it; one
  removed from the list is said to be gone, and the page does not pick another.
  A judge chosen here is kept and dropped the same way, and sent as
  `session.open`'s `judge` only when it is not the default.
- *A conversation.* The selects show what the next message is answered with
  and change it (`session.model.set`). After a choice, the lines under them
  say that the change takes effect from the next message; an unchanged running
  response shows no change notice. When a choice is queued, they say it keeps the
  model and effort it started with and the choice waits for it, naming both,
  and, if the choice could not be applied when it ended, why. A conversation
  whose model is no longer offered shows *Choose a model…* and its reply box
  stays shut until one is chosen. A closed task shows the model and effort it
  was left on in its heading. The selection is the daemon's and the log's, so
  it is the same after a reload, a reconnection or reopening the task, from
  any window. An open task whose model's context nobody knows (its model
  state's `context_unknown`) says under its heading that it works in the
  default window, which compacts early, with an **Open Settings** button
  (ADR 41, amended 2026-10-05) to Settings › Models. Its Judge select moves
  the task to another judge, or none, from its next question
  (`session.judge.set`); while a response runs it waits, since a judge is
  chosen between turns, and the transcript says each choice ("Judge: Jev ·
  jev-latest at api.typesafe.ai").
- **Helpers** (ADR 27, amended 2026-10-09). Beside the picker, New task has
  a **Helpers: none** fold listing the other models with the labels each
  carries; ticked models are sent as `delegates` in `session.open` /
  `worktree.open`, so the task's model may hand work to them with its
  `delegate` tool. None is the default, nothing is remembered for the next
  task (not even in this tab's storage), the model chosen and the default
  model are not offered, and the fold is on this computer only, like the
  Judge select. A task's page says "May hand work to: ..." beside its
  picker. Settings › Models edits a model's labels (*What it is for*, with
  hint buttons); the change reaches open tasks.
- A paired device sees the selects, disabled, and a line saying the model is
  chosen on the computer nodwyn runs on; Home shows only the default model's
  name. It has no Judge select: the judge is chosen on that computer too
  (ADR 47).

## Tasks, folders and files

**Starting.** Home's composer starts a task in the folder picked last,
which this browser remembers like the theme; until a folder has been
picked, it starts in the first one on the list. The **Work in** chips are
the one folder control. The bar holds what every task needs: **Attach**,
**Options**, the model (its Effort select is left out for a model that takes
none) and Send. **Options** holds the rare choices, **In a worktree**,
**Limits**, the helpers and **Schedule…**, and says how many differ from
their defaults; Escape closes it. **Enter** starts the task and
**Shift+Enter** breaks the line, as in a task's reply box (Cmd or Ctrl+Enter
starts it too), and a line under the box says so. **Schedule…** opens the
schedule with the message typed so far; the schedule is saved with the
message the form holds, so it works with an empty box and is disabled, with
the reason as its title, only where a task cannot start (keys locked, no
folder, the model blocked). A start the daemon refuses before any task opened
says "The task did not start: …" and leaves the message in the box; only a
lost connection or a timeout, which may have started one, sends the person
to the task list. On a phone only **Needs you** comes above the composer;
Running, Scheduled and Recently finished follow it. **Add a folder** names one
and, in the app window, opens the system's folder chooser. Workspaces lists
named folders and **Folders tasks used**. **Add a workspace** names another;
**Edit** changes its name and **mounted folders** (other folders a task may
read, or read and write, by absolute path). **Forget**, on both kinds
of row and only on the computer nodwyn runs on, takes a folder out of the
lists after a confirm; what it keeps and what brings the folder back is in
[the manual](manual.md#forgetting-a-folder-in-the-console).

**Finding a task.** The sidebar's magnifier opens one search box. It filters
every task's title, folder and branch, the archive included, and, above them,
offers **Search conversations for "…"**, which looks for what was said in the
tasks (**Enter** takes it too); **Escape** clears and closes the box. The
conversation search is anchored on the task on screen, else the newest task in
the folder Home would start in, else the newest, since the daemon takes the
project from the task it is given.
Home offers the
first six folders; the rest are a click away on Workspaces. The name,
mounted folders and forgotten markers are kept in `workspaces.toml` in the
config directory, never in the folder, so a folder someone else commits
cannot mount anything (ADR 22). Mount changes apply to tasks started
afterwards; already-open tasks keep what they loaded.

**Retrieval** ([ADR 50](adr/0050-retrieval.md)). A folder's editor has
**Let the model search the earlier tasks here**, off until ticked; it counts
from each task's next call, open ones included, and a folder's row says when
it is on. A folder whose repository index exists has a line saying what it
holds (files, size, when it changed, how many working trees) with **Forget
the index**, which deletes it until the next search builds it again; a
paired device sees the line but not the button. A task's **Workspace** tab
shows the same two things for its folder (**Search index**, **Earlier
tasks**), and reads the index again each time the tab is opened. In the
conversation the model's searches, recalls and index updates fold with its
other looks and say what they did in words; a build of the index is a line
under the call that made it, and the build's own reads of files never mark
that call.

**In a worktree.** Under Home's **Options**, **In a worktree** is checked
by default for a git workspace, is disabled with the reason for a named
folder that is not a git repository (a folder no workspace names is left to
the daemon, which refuses), and starts the task in a git worktree of its own
([ADR 34](adr/0034-sessions-in-git-worktrees.md)): a checkout of the
repository on a branch of its own, `nodwyn/<id>`, in
`.nodwyn/worktrees/<id>`, so several tasks can work on one repository at
once without touching each other's files or the ones you are editing.
Uncheck it to start in the folder itself; the choice is kept for that
git workspace in this tab. Only a git repository's top
folder with a commit can have one; any other is refused, in a sentence,
when the task starts. Uncommitted changes in the folder are not in the
worktree, and the conversation says how many. The task is listed under
its repository; its heading shows the branch, and its Workspace panel the
branch and where it started. Its changes stay on that branch until it is
merged. The task's **Review** button, or **Review** beside a worktree task
under Home's **Recently finished**, opens its changes in the centre panel.
Each file has its status and a diff, rendered as literal text. **Unified** is
the default, with old and current-file line numbers; **Side-by-side** aligns
the old and new files, leaving a blank where only one has a line. Long lines
wrap within the diff; on a phone the split diff scrolls sideways without
widening the page. A line starting with `++` or `--` is content, not a header.
A closed task's review says to resume it, with **Resume and review** beside
the message: one step resumes the task and opens the review (Home's **Review**
link lands there for a saved task). **Close review** restores the
conversation and reply form without discarding either draft. The review is
built again only when something it shows changed (its diff, the comments, the
layout, whether the task works), not for every recorded event, so a selection
in a long diff and the reading place stay while the task works. Comments and an
unsaved notes draft survive a reload of the same task, which a failed send or
answer causes; only another task starts without them.

**+** on a current-file line (or **Comment on file**) writes a comment, which
**Remove** takes back. Old-only deletions have no line-comment control; use a
file comment instead. **Send review** sends all comments as one message ordered
by file and line, with only the current-file line number and the comment,
and clears them only after acceptance. Send waits while the task works;
a refusal keeps the comments and overall note. Once opened, the diff is read
again when a turn ends; **Refresh** reads it now. A failed refresh keeps the
previous diff, and late replies cannot reopen a closed review. A comment whose
file or line the diff no longer shows is listed under its own heading, still
counted, and one whose line now says something else is marked. Comments, an
unfinished comment and the overall note survive redraws, mode changes and
closing/reopening in this tab, with the reading position and textarea caret.
Reloading the page loses these drafts. Home's approval **Review** links still
open the conversation's pending request, not this worktree review.
Closing the task removes the worktree and its branch only when
nothing in them would be lost: no change, no untracked or ignored file (an
attachment is one), no commit its base lacks. Otherwise both stay, and
resuming the task works in them again; a worktree that was removed is made
again on resume.

**Limits.** Under **Options**, beside **In a worktree**, **Limits** is ticked for every new
task: its turns stop at the limits in Settings › General (tool calls, tokens,
actions and time). Untick it and that task's turns have none of the four, so
a turn may work, or wait for your answer, as long as it needs
([ADR 49](adr/0049-scheduled-tasks.md)); the label turns amber while it is
unticked. The spend limit, the timeouts and the progress guard still apply.
Starting the task ticks it again for the next one. The task's heading then says
**No turn limits**, Statistics' **This turn** says "no turn limits" and shows
counts without bars, and Details › Budget says "no limit"; the conversation
says when the limits went off or on, and who chose. **Limits** beside
the task's model turns them on or off again, from its next turn; it is not
offered during a run, which keeps the limits it started with. Only this
computer has either box: a paired device's tasks start with their limits on.

**The task list.** The sidebar puts what needs your answer first, across
folders. Every other task is under its folder, newest activity first, and
the folder whose task worked last comes first; the order follows as turns
end and tasks close. A folder shows five tasks and **Show N more**, and
its heading folds it (this browser remembers which). The sidebar’s filter matches
titles and folder paths in every folder and in the archive
([ADR 39](adr/0039-named-archived-deleted-tasks.md)). Only the task list
scrolls: the search, New task, the places (Connections, Workspaces,
Settings) and the model's state stay in view however long it is, and a
redraw keeps the list where it was.

**Switching.** A task picked from the list puts the focus in its message
box, so the next word goes to its reply, and a word not sent is kept for
it. An open task's page is kept while another is shown: it stays
subscribed, draws what happens meanwhile, and comes back at once as it
was, without reading the conversation again. The eight most recently
shown are kept; a saved task's page, or one past those, is read from its
log when it is opened. The page says "Loading the conversation…" until its
first event is drawn; a load that fails says so where the conversation would
be, with the message and **Retry**, and the reply box says why it is off. A
saved task's reply box is not dead: **Send** resumes the task and then sends
the message (nothing is sent if resuming fails, and the text stays in the
box), and **Resume** in the heading stays too. A key typed into a request card is cleared when you
switch away.

Streamed text and thinking are joined for the next display frame, without
redrawing the task's heading and detail panels for each fragment. A task
kept off screen buffers those fragments until it is shown or another kind
of event arrives. Replies and questions flush preceding fragments at once,
so their order does not depend on a background tab receiving frames. The
conversation follows new text only while you are already near its end.

A task is read by its first message until you name it: the pencil beside
its heading (or a double click on it) turns the heading into a field;
Enter or clicking away saves, Escape keeps the name, and an empty name
gives back the first message. A closed task has **Archive**, which moves
it under **Archived** at the end of the list (**Unarchive**, or resuming
it, brings it back), and **Delete…**, which asks, then removes its
conversation and log for good and leaves the files it changed as they
are. An open task is closed first. A paired phone renames and archives;
only this computer deletes.

**Attaching.** The paperclip in either composer attaches files to the next
message (at most 20 MB each): text formats, `.docx`, `.xlsx`, `.pptx`, PDF
when `pdftotext` is installed, and images (PNG, JPEG, GIF, WebP; at most
10 MB) when the control model takes them; the TUI and line mode attach
nothing. Each file is copied into the folder's
`.nodwyn/attachments/`, its text redacted and capped, and shown to the
model as data. The conversation shows a chip per file with how many
secret-like strings were hidden.

An image cannot be redacted, so before it is attached the task asks, as a
security request card: it names the file, the model and its host, and
whether that host is this computer. Sending waits for your answer; from
Home the task opens first, so the card is in front of you. **Deny** (or
**Stop**) leaves the image out and keeps your message unsent, in the task's
reply box: send it there, with or without attaching again. Home starts
afresh, so the same image is not asked about twice. An attached image shows as
"image · 1200×800 · ~1,105 tokens": what it costs the context. It goes only
to the host you allowed, never to the judge.

**Undo.** Unless git already has a file committed and unchanged, nodwyn keeps
its previous content before changing it, so a write that went ahead without
asking can be taken back. A task's **Changes** list shows the latest recorded
approval per file, newest first (up to twelve). Click the filename, or focus
it and press Enter or Space, to expand its recorded preview with line numbers
and addition/deletion counts; long lines wrap, since the side pane is too
narrow to scroll them. **Approved preview—not proof of a successful
write** means the guard approved before execution: check the tool result for
success. Older records without a preview say it is unavailable; no diff is
invented from today's file. Empty and oversized diffs say so explicitly.

**History** lists kept versions. **Compare with current file** reads the chosen
kept version and today's file on demand: deletions are lines from the kept
version, additions are lines in the current file. A missing current file is
shown as a deletion. This read-only comparison does not restore anything or
refresh automatically. Open previews and comparisons survive redraws; replies
for closed History lists, old requests or another task are discarded.
**Restore** writes a kept version back as a write like any other (it
asks only when Settings › General › Approvals has writes ask), and keeps the current
content too. From a terminal: `nodwyn versions FILE` and `nodwyn undo FILE`.

**Tools from MCP servers.** Connections › Tools adds a remote server (HTTPS, or
plain HTTP to this computer only; a key goes to the keychain, bound to that host only)
and the folders it is offered in, lists its tools for
review, and approves that exact tool list. For each tool you say what it does
— reads, changes things, or leave it undeclared — and whether it is offered;
nothing is offered until you choose. An undeclared tool asks every call and
offers no standing answer. If the server later changes its tool list, new
tasks withhold its tools until you review again.

A server can also be **contained, on this computer** (ADR 23 W3b): its image
pinned by digest, the hosts it may reach (none, or a list its proxy lets
through), the workspace folders it sees, and, if it needs a key, the
variable it reads it from; the key goes to the keychain, bound for every
listed host. Its page says what it can reach, whether podman or docker is
there, and whether its image is; Pull downloads the image by its digest,
which nothing else in nodwyn does and a task never does. Reviewing its tools
starts it in a container and removes it after. A confirm for one of its
tools says it sends to the contained server and what that can reach; a
connection its proxy refused shows in the task.

## Credentials

**Connections › Credentials** (`#/connections/credentials`, or Settings ›
Keys & devices › Manage credentials) lists what the keychain holds: each
credential's class, kind (an API key, or an OAuth2 account with its provider
and granted scopes, and when its access token runs out), what is configured
to send it (the main model, another model, a model server, an MCP server,
Google Docs, the judge) and the binding rows that allow it somewhere. A credential allowed
nowhere is marked: nodwyn refuses every request that would carry it. A class
a model or an MCP server sends that the keychain does not hold is named at
the top, with a button to add it; so is a keychain without a passphrase.

- **Add a key**: a class, the value (a password field; nothing shows it
  again), and optionally the host it may go to, its methods and where it goes
  in the request (`Authorization: Bearer`, another header, a query parameter,
  the body, or a contained MCP server's environment variable). The host is
  saved as one exact binding row per method.
- **Allow it for a host** adds binding rows for a credential already stored;
  an OAuth2 credential's rows carry the scopes it was granted, so a broader
  token is refused.
- **Replace the key** gives a static key a new value after it was rotated
  where it was issued; its id and class stay, so its bindings and what uses
  it carry on.
- **Connect an account (OAuth2)**: a provider (google, microsoft, github,
  notion, or custom with its endpoints), your OAuth client's id (and secret
  if the provider insists), scopes (the provider's narrowest useful set by
  default) and a class. nodwyn opens the provider's consent page; when you
  agree, the provider sends the browser back to a listener on this
  computer's loopback, and the tokens go into the keychain. The page waits
  up to five minutes and shows the outcome. **Authorize again** does the same
  for a credential the provider stopped refreshing. It works from a browser
  on the computer nodwyn runs on, since that is where the provider sends you
  back.
- **Remove…** asks first, naming what uses it; an OAuth2 credential can be
  revoked at its provider and removed, or removed here only. Its class's
  rows leave `bindings.toml` with it, so the next credential given the class
  does not inherit them, unless another credential still has the class.
- **Remove** on a row of *Where it may go* takes that host back after a
  confirm (`credentials.unbind`): the row as listed, whatever its methods,
  goes from `bindings.toml`, and every other byte of the file, comments
  included, stays. A row the text cannot be cut at (written as an inline
  array) is refused with the file unchanged: edit it in the file, or in a
  task's Details › Policy.

No reply carries a value or a token. A paired device sees the list but
changes nothing.

## Other devices (ADR 30)

```sh
nodwyn ui --listen 100.101.102.103:7777          # a tailnet address
nodwyn ui --listen 100.101.102.103:7777 --name desk.tail1234.ts.net:7777
nodwyn ui --listen 0.0.0.0:7777 --name desk.local:7777 --tls-cert cert.pem --tls-key key.pem
# behind a proxy that ends TLS (tailscale serve): the console stays on loopback
nodwyn ui --listen 127.0.0.1:7777 --name https://desk.tail1234.ts.net
tailscale serve --bg 127.0.0.1:7777              # see `tailscale serve --help` for your version's syntax
nodwyn devices                                   # list paired devices
nodwyn devices revoke phone
```

A browser on another machine pairs with the code **and a device name**. It
gets a device cookie (HttpOnly, SameSite=Strict, Secure over TLS, 180 days);
`devices.toml` in the config directory keeps only the token's SHA-256, so the
file cannot pair anything. Revoking takes effect without a restart: new
requests are refused at once, and a console the device already has open is
closed before its next request, or within five seconds if it is only
watching. The console answers only to the names it listens on, so a wildcard
`--listen` (`0.0.0.0`, `::`) needs `--name` with the name browsers use. A name
is `host:port` as the browser's address bar has it; a proxy that ends TLS in
front of the console (`tailscale serve` serves https by default) sends the
bare name and an `https://` origin, which `--name https://NAME` says: the
console then expects that origin for that name, marks the device cookie
Secure, and still speaks http to the proxy. A refused origin is answered with
the one expected. Every answer from a device is recorded as the bridge's
kernel identity followed by `via web:NAME`.

The console pings an open page every 30 seconds and closes a connection that
has not answered within a minute: a proxy such as `tailscale serve`, or a
network on the way, keeps a connection that carries pings open, and a phone
that slept or lost its network gives back its connection to the daemon. The
page connects again by itself when it can (the conversation, above): after
a sleep, a change of network or a restart of `nodwyn ui` or the daemon, a
paired device comes back without pairing again, and a revoked one goes to
pairing and stops trying.

A device can read tasks, start and steer them, and answer approvals — one-time,
for this turn, or for the session, as the core offers; it can also restore a
kept file version, save project notes, read the policy files, see the
credentials, and stop a run. Its tasks ask what Settings › General › Approvals, chosen
on the computer nodwyn runs on, says they ask. It cannot start or resume a
run that answers its own requests (unattended, judged, or saying yes), turn
on "don't ask", turn a task's limits off or on (its tasks start with them on),
save a credential binding,
write a policy file, unlock or create the keychain or change its passphrase,
add, replace, allow, authorize or remove a credential (a key the model asks
for included: the device shows the request, and the key is typed on the
computer), change Settings, the
model or how
uploads to Google Docs are handled (it can answer the first upload's
question with "allow by hand" or "deny", never "always allow"), edit
workspaces, add, review, approve, pull or remove MCP servers, evaluate Scheme, or
list and revoke devices: those stay with the computer nodwyn runs on. A request counts as remote when it comes from another address or names
this console by anything but its loopback address (a proxy such as
`tailscale serve` connects from loopback on a phone's behalf). Launch tickets
pair only local browsers. Without `--tls-cert`, traffic is unencrypted unless
the network encrypts it; a tailnet does.

# Local browser view

The U3 session desk supports conversations, prompt replies and session lifecycle
controls. Run a daemon, then attach the browser bridge in another terminal:

```sh
nodwyn daemon
nodwyn ui                         # opens the local browser page
nodwyn ui --read-only             # view only; no replies or lifecycle changes
nodwyn ui --socket /absolute/private/sock --no-open
```

`nodwyn ui` connects to an existing daemon and opens the page with a one-time
launch ticket in the address's fragment, so that browser pairs itself. The
ticket is on the browser's command line while it starts, which another user of
the same computer can read in the process list; it is good once and for a
minute, but on a computer other people log in to, start with `--no-open` and
paste the pairing code instead (the code is never on a command line). It
also prints a random pairing code for another browser on this computer;
paste that code into its page. The code is never put in a URL or session log.
Keep it in the terminal rather than sharing it. The browser receives an
HttpOnly, SameSite=Strict session cookie holding a token made for this run,
never the code; no credential is written to browser local/session storage.
Browsers send a loopback cookie to every port on that address, so a server on
another port of 127.0.0.1 that this browser visits receives the token and
could use this console until it stops; it cannot pair another device.
Without `--listen`, restarting the bridge creates a new code and session and
takes the port it had the last time when that port is free (kept in the
data directory as `console-port`), so what the console keeps in this browser,
the theme, thinking shown, folders folded, is still there; with `--listen`,
the port is yours and a device's cookie survives restarts.

The desk shows saved and open sessions, their repository and last activity,
recorded conversations, streamed text, tool arguments and results, guard
decisions, and model usage. Reads and searches in a row fold into one group.
Writes in a row fold the same way, into one Edited group whose header counts
distinct files, each write keeping its row with its outcome; a failed write
opens its row and the group. A line of narration the model writes between two
writes goes into the group, in order, behind its fold, since the group says
what the line was about; a longer reply, or the words before a command, stay
where they are and end the group. Commands in a row fold the same way, into
one run whose header says how many ran and for how long together, counts up
each second while one runs, says when one waits for your answer, and counts
those that failed; a failed or refused command opens its row and the run. A
read, a command, a mark or a reply of more than a line ends the other groups,
so the transcript keeps its order. The model's marks on its plan join one
line in a row ("Steps 1, 2 of 5 marked done"), their notes behind its fold.
All of these sit inside their stretch's **Activity** row. A read-style wrapper
that runs a shell command keeps the guard's recorded, redacted command in its
call row without changing the read group into a run of commands.
The model's thinking is hidden, as in the TUI without `:thinking`, unless
Settings › This browser › The model's thinking is **Shown** (kept in this
browser); shown, a reply that only thinks and calls tools puts its thinking
in the group or run its calls join, between the calls, under the thought's
first line. It reads a fixed log prefix in pages
before applying live events, deduplicating durable sequence numbers. Reloading
the page reads that prefix again. Statistics includes usage from the recorded
replies in that history, just as it does for replies delivered live; nested
shell decisions inside read-style wrappers do not interrupt replay. Browser
reload retains pairing through the session cookie, and the address keeps the
task, so the same task opens again.

A connection that drops comes back by itself. The banner over the page says
it is disconnected and counts down to the next try: a second, then two, four
and so on up to thirty seconds, and no sooner than every five while the
daemon is not running, which the banner says. **Reconnect now** does not
wait, and neither does the page when it is shown again or the network comes
back; a connection that looks live then is asked to answer within ten
seconds, since after a sleep a socket can look open with nobody at the other
end. A page that was never live shows the same on its connection card.
Coming back reads the desk and the open task again from the log, as a first
connection does. Nothing that was on its way when the connection dropped is
sent again: a request waiting then fails and says so, and the reply box
keeps its draft. Project notes being edited and a worktree review's comments
not yet sent are lost, as on a reload. A browser the console no longer takes
goes to pairing and stops trying: the console closes a revoked device's
socket saying so, and a socket refused before it opened is followed by `GET
/paired`, which says whether the console takes this browser; a stopped
console, or the 5xx a proxy in front of one answers, is tried again.
A page that comes back to a daemon of another version than the one it first
connected to (nodwyn upgraded, as `make promote` does on an always-on
computer) still runs the console it loaded: a banner says nodwyn was
restarted as that version and offers **Reload**. The page never reloads by
itself, since the reply drafts and the New task message are only in this
tab's memory and a reload loses them; they are not handed over through the
browser's storage (ADR 30, amended 2026-10-17). A reconnect to the same
version shows no banner.
Untrusted text is rendered as literal text, never HTML or executable Markdown.
Quotes and lists nest at most eight deep; deeper lines show as text. A record
the console cannot draw shows a line saying so in its place, and the rest of
the conversation draws, so no single reply can close a task to the console.

Create a session with an existing absolute repository directory, resume a saved
session, and send messages from the composer. Abort cancels the current turn or
pending prompt. Close ends the session and preserves its transcript; it is disabled
while known busy, but another client can start work concurrently, so closing can
still interrupt work. Drafts remain separately in memory for each session in this
tab and disappear on page reload. A failed reply may follow successful delivery:
inspect the transcript before sending again. No operation is automatically retried.

`--read-only` retains the original view-only mode. The server filters advertised
methods and enforces this restriction independently of the browser controls.
The interactive mode includes a pending-prompt panel with reasons, literal
arguments, egress scope, and the existing write diff (additions/deletions colored
without changing the diff algorithm). Allow once and Deny answer only that live
prompt. Eligible ordinary calls also offer the displayed command/write prefix for
this session, or ordinary writes for this turn. Security and protected-path
confirmations remain one-time only; no grant overrides a denial. Under the
composer, **Don't ask: approve writes and commands** answers every ordinary
write and command in that task without asking, and the question waiting when
it is checked, until it is unchecked or the task closes; security and
protected-path questions still ask, the conversation says when it was switched
and each call it approved says so. It is the daemon's, not Scheme's
(`session.approve_all`, [protocol](protocol.md)), it is never saved, so a
resumed task starts asking again, and only this computer turns it on: a
paired device sees the switch only while it is on, and can turn it off (the
daemon refuses to turn it on for one), so a task skipping its questions can be
stopped from a phone without aborting it. The switch is shown only while Settings › General ›
Approvals offers it (**Don't ask in tasks**, `approval.dont_ask`, off by
default), or while it is still on; offering it shows a red warning in place
of the hint, and taking the offer away turns the switch off in every open
task (ADR 17, amended 2026-10-05). The daemon sends
derived scope labels in optional prompt metadata and validates the actual pending
prompt again when an answer arrives. An older daemon without this metadata gets
only one-time browser buttons.

When an unbound call has a core-approved exact binding proposal, the panel shows
its credential class, host, method and injection position. Saving that row uses
the separate binding method, persists beyond the session, and permits the pending
call. That prompt-bound operation cannot substitute row fields. The separate policy-file
editor below proposes explicit file changes for another review. Resume never reinstates session/turn grants from their audit history.
Scheme evaluation remains unavailable from the browser.

Credential prompts use a password field and the existing shared keychain path.
Input is cleared on submit, session switch, refresh, or disconnect; it is never
saved as a draft or included in the transcript. JavaScript strings cannot be
reliably wiped from browser memory; the UI does not claim a secure memory arena.
Budget checkpoints show usage, what the turn has cost so far when that is
known, and unchanged hard ceilings, with continue, stop, and explicit absolute
soft-limit choices. More budget grants no tool authority.

Read-only mode displays the pending prompt but exposes no reply controls and
rejects replies at the server. Pending state is restored from the daemon on
reconnect; stale/replayed answers are rejected by core. Resolution in another
client clears the panel. Browser replies retain the Unix peer identity of the
bridge in audit receipts; individual tabs are not separate security principals.

The bridge gives each relayed call 30 seconds. The daemon answers one request
at a time on a connection, so the calls that may take longer, a model's
**Test** (a minute) and an MCP image's pull (thirty minutes), go over a
connection of their own with a little more time than the daemon gives them,
and the console's other requests, an abort or an answer, are not held up
behind them.

Closing a browser tab or stopping `nodwyn ui` does not close sessions in the separately running daemon. Stopping the daemon still closes its
sessions normally. The session list's titles and history metadata are snapshots, read
again when this console starts, resumes or closes a task, or another client opens one;
reload the page to update them at other times. Open/running/needs-answer state updates live across the daemon's
sessions, including sessions opened by another client. These updates do not
reload the selected conversation, replace an inspector or discard a draft.

The session view buttons open fixed snapshots:

- **Plan record** shows the latest user message's recorded gate outcome, score and plan,
  and the steps the model marked since (`plan.step`), in order.
  A newer message without a recorded outcome never inherits an older plan.
- **Budget** separates current configured soft/hard limits from the latest turn's
  recorded checkpoints. Checkpoint usage is not live remaining allowance.
- **Log** filters durable records by kind prefix and shows the last 1–1000 matches.
- **Policy** shows the last validated bindings, current command/write prefixes and
  turn approval. It does not restore authority from recorded receipts. Separate
  buttons open the guarded policy-file editor.
- **Tools** shows the model's current tool catalog and input schemas.
- There is no Friction entry: `docs/friction.md` is nodwyn's own development log
  and only its repository has the file, so the console does not offer a read that
  fails everywhere else. `session.friction` and the TUI's `:friction` remain as
  developer commands.

Every inspector names its log watermark. Live transcript events keep arriving but
an open inspector stays fixed until Refresh view reloads history and metadata.
Current session metadata is sampled separately from the log prefix. Saved sessions
retain plan/log views; current limits, policy and tools require an open session.
Pending prompts take priority over inspector navigation. Read-only mode has the
same snapshot inspectors. It retains its server-side mutation restrictions.

Without `--listen` the bridge binds only `127.0.0.1`, on the port it had the last time when free, else a new one; with it,
the address you give, for paired devices (above). Every request checks its
exact Host; pairing and WebSocket upgrades also require the exact browser
origin. Cross-site fetches and framing are refused. There is no general TCP
RPC transport: the daemon's Unix socket remains private, and kernel peer
identity still identifies the bridge process.

Implementation is deliberately small: embedded HTML/CSS/plain JavaScript,
`internal/frontend/web` for the HTTP/WebSocket boundary, and the existing RPC
client for session data. There is no JavaScript build system, second history store or policy evaluator.
`app.js` is the entry point (pairing, router, shell); `connection.js` is the one
connection to the bridge; `desk.js` holds what every page reads (tasks, what
needs you); `sidebar.js`, `home.js`, `task.js`, `transcript.js` and `pages.js`
draw the views; `prompts.js` renders review choices; `setup.js` the keychain
forms; `modelpicker.js` the model and effort picker of the New task window, a conversation and Settings; `models.js` Settings › Models, whose provider and model forms the first run draws too; `credentials.js` Connections › Credentials; `attach.js` attachments; `markdown.js` model replies;
`inspectors.js` fixed snapshots from the same redacted records; `policy.js`
policy drafts and review controls; `dom.js` the helpers that keep every string
text. A pinned WebSocket library avoids a
homegrown frame parser. `nodwyn licenses` prints the notices that travel with
the binary: the console's typefaces and the licence of every Go library linked
into it (`licenses/go-modules.txt`, written by `python3 scripts/licenses.py`
from `go list -deps` on each platform nodwyn is built for, and checked against
`go.mod` by a test).

## Verification and remaining U3 work

Tests cover pairing, missing/wrong credentials, origin/Host rejection, read-only
method enforcement, filtered capability discovery, browser lifecycle/message
operations, durable history after resume, live daemon events and session survival
after browser detach.
Browser checks use isolated synthetic sessions and local fake services, covering
literal HTML-like text, paging, refresh, collapsed thinking/tool details, reload,
disconnect notices and narrow layouts. These are acceptance fixtures, not claims
of production usage.

Browser acceptance also covers creation, message errors, independent drafts,
close/resume, and aborting a pending confirmation without executing its operation.
Prompt tests also cover attributed one-time approval/denial, stale and forged
answers, canary credential isolation, and checkpoint hard ceilings. Browser checks
exercise pending snapshots, cross-client resolution, masked input clearing,
read-only prompt display, and valid/invalid checkpoint choices.
Scope tests additionally cover temporary grant lifetime, protected/security
confirmations, exact persistent binding and host injection, forged destinations,
and clean authority after resume.
Inspector browser checks cover paged log filters, older-plan exclusion, fixed
snapshot refresh, current/saved session distinctions and prompt priority.
The friction read is covered through core, RPC and the browser WebSocket, with
allow/deny/custom-confirm/cancellation, concurrent-operation rejection and canary
redaction.
The policy editor completes the current U3 scope. Its two targets are shared
`bindings.toml` and repository `.nodwyn/allow.toml`. Reads and writes use fixed host
primitives through the interpreter and guard, with no arbitrary path or browser
Scheme evaluation. The editor is unavailable through read-only bridges. Its
buttons are the console's own (`.btn`), and **Reload file and discard edits**
asks (**Discard edits** / **Keep editing**) when the draft differs from the
file, so one slip does not lose it.

Each save validates TOML and requires a one-time diff confirmation. Session/turn
approvals cannot waive it. A revision comparison under the shared policy lock
rejects stale edits; a new read and review are required. Publication is atomic
and mode 0600. Cooperative writers use the sibling `.lock`; an unrelated editor
can still ignore that advisory lock. Files are capped at 64 KiB, final symlinks
and non-regular files are refused, and repository approval paths cannot escape
through parent symlinks. Recognized secrets are refused instead of round-tripping
redacted text as editable policy.

Bindings apply before the next egress operation. Repository approval-file changes
need a fresh `nodwyn trust` review before new or resumed sessions load them;
existing sessions retain their current grants.
Global `config.toml` settings and transient grants are not edited by this view.
Drafts stay only in the selected tab/session and clear on switching, refresh or
disconnect. After answering a prompt, reopen Policy to see the result. Successful
saves require reloading before another edit; failures retain the draft. A failed
connection never retries a save automatically.

Tests cover per-save confirmation, denial/cancellation, stale revisions,
caller-supplied paths/actors, environment-name hijacking, model-origin rejection,
known-secret containment, bounded reads and filesystem escape checks. Browser
acceptance covers creation, persistence, validation errors, stale saves and draft
retention after a failed write.
Cross-session notifications and runs are described below.
Optional general TCP transport remains separate roadmap work.


## Cross-session attention (U4)

The sidebar marks every open session needing a confirmation, credential reply or
budget answer. Select that session to review its normal pending-prompt panel.
The lightweight `session.watch` feed contains only IDs, state flags and revisions;
it does not subscribe the browser to other sessions' transcripts or prompt text.
Late snapshots cannot replace a newer state. Closed/resolved sessions clear their
attention indicator, and disconnect clears the live-state claim. Home's Needs you
list offers **Allow once** beside **Review** only for a plain command or write;
a security confirmation or a send to another host offers **Review** alone, since
its reasons, destination and diff are on the task's card. On a phone, where
the sidebar is a drawer, the topbar's "N needs you" pill is a link: to the
task when one waits, to Home when several do.

Desktop notifications are optional. In Settings › This browser › Desktop
notifications, **Turn on** asks this browser for permission; the choice is
kept in this browser (`nodwyn-notify` in local storage), so it holds across
reloads, and counts only while the browser still allows them: a permission
taken back turns them off, and nodwyn never asks for it again unprompted. In
the app window the system's notifications are used instead, with no browser
permission to grant, and are on until turned off. When the page
is hidden, a new needs-answer transition can produce a generic notification.
It never includes task text, prompt arguments, diffs, credential classes or
values. Clicking it opens the corresponding session for review. Resolution
closes the notification where supported; reconnect snapshots do not re-notify old
prompts. Blocked or unsupported browser notifications leave sidebar attention
available. Browser/OS notification delivery still depends on their permission
and focus settings.

RPC/WebSocket tests cover content-free snapshots and live transitions. Browser
acceptance verifies another session's prompt and resolution without disturbing a
draft, and notification mocks verify opt-in, generic text, revision ordering,
deduplication and reconnect behavior. Native OS notification delivery was not
exercised by those mocks.

## Runs (U4)

Every task Home starts is a chat (ADR 35). A request that takes long is a
message like any other: the turn goes on until the model answers or a hard
limit stops it, and asks you only what Settings › General › Approvals leaves to you.

`nodwyn run` carries a task to its end with no conversation, for scripts
and benchmarks ([runs](runs.md)). The console shows such a run like any
task: its status says "Run" or "Unattended run" (and "judged" when the judge
answers its confirms) with the turn count, the sidebar shows the turns and
the time left, and the plan panel shows each turn's progress. A supervised
run's checkpoint arrives as an ordinary prompt. **Stop** aborts the session
and with it the run; a message afterwards goes on with the task as a chat.
The transcript says when an unattended run went on or stopped by itself,
and which confirms the judge allowed or refused.

Workspace paths and task roots resolve symlinks for identity, including macOS's
`/tmp` and `/var` aliases. An existing alias does not become a second project;
mounts and MCP workspace choices follow the same identity. See
[the security reference](security.md) for notes and conflicting legacy entries.

## Conversation search (ADR 51)

The sidebar's search box (**Search conversations for "…"**, or Enter) and a
task's **Search** (in its menu) open `#/search/ID?q=WORDS&scope=project`; the
scope is `project` (this task and the project's earlier ones, the default),
`conversation` (this task only) or `earlier` (the others only). `project` is
two calls of the read-only `conversation.search`, this task's results first;
the others are one. It sends the anchor task ID, not a project path. The daemon derives the project from that
log and uses the existing `.mem` word index and turn ranking. Human search
is independent of the model’s earlier-tasks switch, as the memory CLI is:
paired devices and read-only bridges may read these same logs. Nothing resumes
a task or changes its permissions. Results include archived tasks and worktrees
of the recorded project, excluding the anchor in earlier scope.

Twenty best matching events per call are grouped by task and turn. A match
says what it is in words (**Your message**, **A reply**, **File read:
evidence.txt**, **Command: …**, **The plan**, an attachment's name) with its
time, built from the event's kind and heading, which stays as the tooltip; a
turn is headed by the time of its first match and how it ended, not its
sequence numbers; the query's words are marked in the snippets. The times and
up to three snippets are text nodes (the marks too). A long line retains the matched words,
and redaction precedes clipping. Missing source reads mark the result incomplete.
The form survives desk updates, supports empty/error states and Retry, and
resubmitting a query refreshes it. Query and scope survive reload and Back in
the URL.

A match links to `#/history/ID/SEQ`, retaining its search URL for Back.
`log.context` selects the containing indexed turn and run and returns at most
81 nearby records. The separate historical Transcript opens the match’s folds
and highlights its rendered node, including a result nested in an earlier call.
Unsupported presentation falls back to the redacted record. Earlier/later loads
use `log.read`, at most 500 records, within the fixed turn/run boundaries.
No latest-run catch-up, model change, approval application or subscription occurs.
Open task returns to the ordinary current conversation; an open task parked by
search keeps its live subscription and draft. Leaving either search or context
invalidates pending replies. Failed reads can be retried without replacing a
newer view. The `conversation-search` browser check exercises these actions,
project/worktree/archived isolation, literal untrusted snippets, reload, failure
and stale-response paths, and desktop/phone layouts.

This slice adds keyword search and focused history. A History panel listing
all cards, and search settings with semantic ranking, remain unbuilt: Settings
has no search section.
