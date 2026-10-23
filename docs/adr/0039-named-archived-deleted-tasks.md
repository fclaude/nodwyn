# ADR 0039: Tasks are named, archived and deleted, and listed by folder

- Status: accepted; implemented 2026-10-03
- Date: 2026-10-03

## Context
The console's sidebar listed what needs an answer, what runs, and the eight
most recent other tasks, each by its title: the first sentence of its first
message (ADR 30). A person who starts tasks with the same request, as the
author did when comparing runs of one prompt, saw rows that could not be
told apart and had no way to name them. Older tasks fell off the list and
could be reached only by searching, every row repeated the same folder
name, and nothing ever left the list.

## Decision
- **A task has the name a person gives it.** A `session.title` record in
  the session's own log holds it; the latest one is the title, and one with
  an empty title takes the name back, so the task is read by its first
  message again. Names are at most 120 characters, on one line. The store
  stays a view of the logs (`core.Store`), not a second database: a file of
  names beside the logs would be one.
- **A task can be archived.** A `session.archive` record says whether the
  task list leaves it out; the latest one counts. Only a closed task is
  archived, so a task that can ask for an answer is never hidden; the
  console closes it first. Resuming an archived task brings it back
  (`core.Resume` records `archived: false`).
- **A task can be deleted.** `session.delete` removes the session's log,
  which is all nodwyn keeps of a closed session: its scratch directory went
  when it closed (ADR 28). The files it changed stay as they are. Only a
  closed task is deleted, and the console asks first.
- **Who writes these records.** Only the daemon, or another nodwyn process
  holding the log's lock, writes a session log, in nodwyn's data directory
  and never in a repository, so no stranger's clone can name, archive or
  delete a task. An open session records a name itself, which its
  subscribers see; a closed one is changed through the store, holding the
  log's exclusive lock, and a session another process has open is refused
  rather than waited for. Both go through the log's mandatory redaction,
  so a key typed into a name is redacted like a message.
- **Methods.** `session.rename {id, title}`, `session.archive {id,
  archived}` and `session.delete {id}`; `session.list` entries carry
  `archived`. A paired device may rename and archive, which change how the
  list reads. Only this computer deletes: a log is the record of what a
  session did, devices included (ADR 30).
- **The sidebar lists tasks by folder.** What needs an answer stays first,
  across folders. Every other task that is not archived is under its
  folder, newest activity first, and the folders come in the order of
  their newest task. A folder shows five tasks and "Show N more"; its
  heading folds it, and this browser remembers which folders are folded.
  Two folders with the same name are told apart by the folder each is in.
  Archived tasks wait, folded, under Archived at the end. A search shows
  its matches in every folder and in the archive.
- **The order follows activity.** The console reads the list again when a
  task's turn ends or a task closes, which the content-free `session.watch`
  feed already reports, so a folder moves up when one of its tasks works.
- **Renaming in the console.** The task's heading has a pencil (and a
  double click) that turns it into a field: Enter or leaving it saves,
  Escape keeps the name. Archive or Unarchive and Delete… are among a
  closed task's actions.

## Consequences
- Older readers reject logs that hold the two new kinds, as they do every
  unknown kind (ADR 2): a renamed or archived session is unreadable to a
  `nodwyn-stable` built before this change until it is promoted.
- Another console sees a closed task renamed, archived or deleted when it
  next reads the list (a turn ending, a task closing, a reconnect); a
  rename of an open task reaches every console showing it at once.
- Deleting a task loses its audit trail with it. That is the point of
  deleting, and why it is a closed task's, this computer's and a second
  click's.
- Not built: `nodwyn sessions` and the TUI's session list still show each
  session's first message, and leave nothing out for being archived; only
  the console reads names and the archive.

## Amendment 2026-10-17: the person's device deletes, after a touch

Deleting a task stays out of a paired device's reach, since a log is the
record of what devices did. A device signed in with the person's passkey
deletes one after a touch of that passkey, as it changes setup (ADR 30,
amended 2026-10-17).

## Amendment 2026-10-23: the model names a task in its first reply

A task nobody had named was listed by its first message cut down, which
read badly: a request written as a person talks ("hey, so the login page
keeps sending me back…") is not a name. A task the daemon opens is now
named by the model, when nothing has named it yet.

The first design asked the conversation's model for a title in a request
of its own, before the turn on the first message. It was a round trip the
first turn waited for, on every new task, and it was replaced the same day
by a tool the model calls in a reply it was making anyway: a few words of
output instead of a second request.

- **The tool.** `name-task` takes `title`, three to seven words
  (`prim/prim.go`). The daemon (`nodwyn daemon`, `nodwyn app`) offers it
  through `core.Options.Titles`; a session embedded without it, as the
  tests' are, has no such tool and its prompt does not mention it, so its
  tool list and its scripted model are as they were. The lean profile's
  tool list is `shell-exec` and `fs-edit`, so a session nobody approves is
  not offered it and its task is read by its first message, as before.
- **Prompting for it.** The interactive system prompt says, where the tool
  is offered (`name-task-rule`, `prompts.scm`), that the first reply also
  names the task, together with the first step and not instead of it, once,
  and that an answer saying the task has a name is gone on from in silence.
  It is a line of the prompt's fixed part, so it stays in the cached
  prefix. It is not a working rule of ADR 32, and has no measurement
  behind it beyond the failure it answers (the first message as a name).
- **What is recorded.** The title, its whitespace collapsed as a person's
  name is, without the quotes or the full stop around it, and at most 120
  characters, is recorded as `session.title` with actor `model` through the
  session's own `Record`, so it crosses the log's redaction as a person's
  name does. An empty title is refused, and the model is told so.
- **Once, and never over a name.** When the log holds any `session.title`
  the call answers that the task has a name already and records nothing: a
  name a person or a workflow gave, one the model gave, and a name a person
  took back with an empty one all count, so the model does not name a task
  twice and never replaces a person's choice. A workflow's session has its
  name before its first message, and its model is told so if it calls.
- **A model that does not call it** leaves the task read by its first
  message, as before; nothing waits for the name and nothing retries.
- **A person's name still wins.** A rename records a later `session.title`,
  which is the title; an empty rename takes the name back, the model's
  included, and the task is read by its first message again.
- Not built: `nodwyn sessions` and the TUI's list still show first
  messages, as above.
