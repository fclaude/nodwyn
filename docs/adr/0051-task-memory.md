# ADR 0051: Task memory: turn cards and notes, searched by words, and later by meaning

- Status: accepted; search by words over turn cards and compaction notes
  (§1, §3, §4, §5's keyword half, §6, §7) implemented 2026-10-15; the rest
  not built (see "Not built")
- Date: 2026-10-15
- Amends: 9 (memory across sessions is no longer only project notes), 50
  (its leave for earlier tasks also covers their memory)

## Context

A task that runs every day keeps every run in one log (ADR 49), and each run
starts from a handover note: what an earlier run found reaches the model only
through that note, and nothing let the model, or a person in the console,
look further back.

The same day, ADR 50 gave the model `index-search` over its conversation
and, when the person allows it for the project, over the project's earlier
tasks, and `recall` to read one event back: events, ranked by their words,
indexed in memory per process. What it does not give is a step up from
events: a turn's request, outcome, files and commands in one place, or a
compaction's note as what a stretch of conversation came to. The console's
search still matches titles, folders and branches.

The person asked for a hierarchical summary of the history, kept in a
database and searchable with embeddings, with a small model shipped with
nodwyn so nobody needs to pay for an embeddings API. The design was worked
out as a page (2026-10-15) and its first slice built here, alongside ADR 50
(both landed the same day, in separate sessions; see "Beside ADR 50").

## Decision

### 1. Memory has levels, each made the cheapest way it can be
- **L0, events:** the log, read on demand.
- **L1, a card per turn:** what was asked, how the turn ended and how long it
  took, what it cost, its plan with the marks made, the files written, the
  commands run and how they ended, errors, and the start of the answer.
  Made in Go from the turn's events (`memory.Folder`); it costs nothing.
  A turn nothing ended is closed by the next message ("unfinished") or by a
  resume ("interrupted").
- **L2, an episode:** a note the model wrote over a stretch of
  conversation. Today that is each compaction's note (`context.compact`,
  its `note_seq`, or the latest reply in a log that names none), covering
  the conversation since the compaction before it. A run's handover (ADR
  49 §8) joins it when it is built.
- **L3, a task's digest:** not built (§8).

### 2. What Go derives is not recorded
Cards and episodes are made again from the log whenever they are needed, so
they are never log records. What a model writes for memory alone (§8) will
be a `memory.summary` record, since only the log can be its truth.

### 3. Memory is kept beside each log, derived and revocable
`<id>.mem` beside `<id>.jsonl` holds the session's entries up to the end of
its last whole turn, with where that turn's line ends, its hash and the
folder's state there. A read folds in only the turns the log gained, and
checks the line it ends at against the log first: one that does not match
is made again from the log, and deleting it is always safe. Deleting the
session deletes it. It is written by whichever nodwyn process searches, as
a temporary file renamed into place, as the log's index is (ADR 49 §10).
(The design page had one directory per project; per session needs no
second key and shares the index's way of keeping in step with the log.)

### 4. No database library
At thousands to tens of thousands of entries a project, files we own and
an in-memory ranking cover it. ADR 39's "a view of the logs, not a second
database" holds: nothing is only in `.mem`. A pure-Go SQLite could replace
the files later under the same rule.

### 5. Ranking: words now, meaning later, fused
Entries are ranked by BM25 over their own text. A word is lower-cased and
an identifier also split where its case changes and at its underscores
(`TestPagerOrder` is found by "pager"), and the commonest English words
count for nothing. A task's title is not added to its cards: with it, every
turn of a task whose title matched ranked alike, a "commit and push" as high
as the turn that did the work (seen on the person's 73 sessions). Vectors
from the shipped model (§9), fused with this ranking by reciprocal rank
fusion, are not built.

### 6. An exact scan for what no card kept
`(memory-search words "project" #t)` finds the words as written, in any
case, in the events `index-search` reads (messages, attachments, plans,
summaries, replies with their calls, tool output, a shell's as the model
read it) of the task, or with leave the project's tasks, newest first: each log is read backwards from its end, a
mark's span at a time, through its index, so a long log is read only as far
as needed. Each place names the card of its turn.

### 7. The model searches with two read-only tools: its task always, the project's with leave
`memory-search` (query, scope `"task"`, the default, or `"project"`,
exact) returns at most 8 results: id, level, task, when and a snippet.
`memory-read` opens one: the card or note whole, a turn's events from the
log (each cut to fit, 12,000 characters in all), an episode's turns. Both
are offered to every session and carry `external:log` like
`session-events`.

A task's own memory is its conversation, which ADR 50 §8 lets the model
read back always. The project's other tasks are earlier tasks, and ADR 50
§9 has the person's answer for them: off unless allowed for the project on
this computer (`earlier_tasks` in `workspaces.toml`, which no model or
repository can write). So `"project"`, and `memory-read` of another task's
entry, are refused with the same message as `index-search`'s `"earlier"`
(where a person turns it on), read at every call. With leave, they see the
repository's tasks, its worktrees' included; another repository's task is
refused whatever the switch. People search their own logs the same way
from a terminal with `nodwyn memory search` and `nodwyn memory read`, which
the switch does not govern: it decides what the model reads.

Who can read what, with leave: the model could already read anything a
task in this repository read, by reading the repository; what is new is
that it can read what earlier tasks here said, ran and were shown,
including what a person typed into them. Logs are redacted as they are
written and again as they are read, and results are tool output, tainted,
so a command built from one still asks. A planted instruction in an old
tool result can come back through a search, as data in a tool result, with
the standing any file the model reads has.

### 8. Model-written summaries (not built)
A long chat's summary when it closes (L2) and each task's rolling digest
(L3), written by the task's own model at the end of a turn while its cache
is warm, recorded as `memory.summary`; past tasks summarized only when a
person asks, with an estimate first.

### 9. A small model shipped inside nodwyn (not built)
`static-retrieval-mrl-en-v1` (Apache-2.0), cut to 256 dimensions: a token
table looked up and averaged in plain Go, 7.8 MB at 8 bits, no cgo and no
runtime; it keeps 96% of its full-size benchmark score at that width, and its
MTEB retrieval is about 81% of all-MiniLM-L6-v2's. English only. It ships only
if words plus vectors beat words alone on the measurement (§11).

### 10. Providers' embeddings, chosen on this computer (not built)
Ollama's `/api/embed`, OpenAI's `/v1/embeddings` format (OpenAI, llama.cpp,
LM Studio, vLLM), and Voyage, chosen beside the model and the judge, since
it decides where content goes.

### 11. Measured before it is relied on (ADR 32; not built)
About fifty questions from the person's own sessions, each answered by a
known turn, scored by whether the answer is in the top five, for
`index-grep`, words alone, the shipped model, and both fused. The questions
are the person's to write or approve, since they come from their sessions.

### Beside ADR 50

ADR 50 ranks events (L0) and reads one back; this ranks what a turn or a
stretch of conversation came to (L1, L2) and opens a turn with its events.
They share the leave for earlier tasks and one function for an event's
text (`eventText`, which the exact scan of §6 reads too, so a shell result
is scanned as the model read it). They differ in what they keep: ADR 50's
earlier-tasks index is words of every event held in memory per process
(87 MB and 3.6 s for the first search, each process); `.mem` is cards on
disk beside each log (628 KB, 1.9 s once, then 0.05 s in any process),
which is the `<id>.ridx` ADR 50 §9 left as the step to take if its memory
or first search matters. Whether the two searches of earlier tasks should
become one tool, with cards as a level of `index-search`'s results, is
left to how the model uses each (ADR 50 §13, §11 here).

## Consequences

- Every session is offered two more tools: their schemas are 1,528 bytes,
  about 510 tokens by the window's estimate, of the tool list's 17,445
  bytes, in every request.
- `<id>.mem` files appear beside the logs: 628 KB for the person's 73
  sessions of this repository on 2026-10-15. On those, the first search
  built them in 1.9 s and a later one took 0.05 s; an exact search for a
  test name took 0.7 s.
- Keyword ranking ranks a long review that names a word many times above a
  short turn about it. That is the baseline the vectors must beat.

## Not built

Built on 2026-10-15: §1's L1 cards and L2 episodes from compaction notes,
§2, §3, §4, §5's keyword ranking, §6, §7 (both tools, ADR 50's leave for
the project's scope, and `nodwyn memory`).
Tests: `internal/memory` (cards, episodes, unfinished and interrupted turns,
identifier splitting, ranking, snippets), `internal/core` (`memory_test.go`:
search within the project only, `.mem` equal to a fold of the whole log as it
grows and after it is tampered with or deleted, the exact scan newest first
across marks, `memory-read`, and the tools in a session: this task's memory
always, the project's refused while earlier tasks are off, allowed when
turned on, a read of another task's entry refused again once turned off).

Not built, in the order proposed: §11's questions and measurement; §9 the
shipped model and fused ranking; the console's search inside conversations,
a task's History panel and Settings › Search (with ADR 49 §11); §8 and §10.
A run's handover as an episode comes with ADR 49 §8.
