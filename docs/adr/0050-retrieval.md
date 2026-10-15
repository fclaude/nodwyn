# ADR 0050: Retrieval: a project index the model searches, recall of the conversation, and earlier tasks when allowed

- Status: **accepted** 2026-10-15, with the person's answers below; the
  conversation (R2) and the repository (R1) implemented 2026-10-15, earlier
  tasks (R3) and embeddings (R4) not built (see the order of work at the
  end)
- Date: 2026-10-15
- Amends: SPEC (Memory: v0 had no cross-session index and no embeddings;
  invariant 1 stands, and every index here is a derived view of it),
  docs/memory.md ("There is no persistent index"), 22 (a workspace gains a
  setting), 32 (a working rule and the read-only tool lists), 2 (one new
  kind, `embed.req`, in the last slice only)

## Context

The person asked for retrieval exposed as tools: the model can index the
whole repository when it wants to, indexes are reused and updated, the
model can search the conversation it is in, and, when the person allows
it, the earlier conversations of the same workspace.

**What exists** (2026-10-15, at 1c2b327):

- `index-grep` matches an RE2 pattern against the current files, with no
  persistent index (docs/memory.md): at most 3,000 candidates, 100 matches
  and 30 seconds, every candidate read through a guarded `fs-read` that
  logs a `guard.decision` (`prim/index_grep.go:96-113`). `fs-find` lists
  names. Both are pure Go.
- Nothing reads back the conversation. The window shows an old result as
  `[elided seq=N]`, the prompt says to "re-read a file if you need it
  again" (`loop/prompts.scm:33, 127`), and a folded read says "The full
  text is in the log; read again what you need with fs-read"
  (`loop/hygiene.scm:109-120`). `fold-result`'s documentation mentions a
  `recall` that does not exist (`loop/prims.go:68`).
- `session-list` and `session-events` read the current project's tasks,
  redacted and tainted `external:log` (`core/history.go`). They are hidden:
  only the person's Scheme calls them, and only `/optimize` does. No
  setting gates them.
- A repository's notes are the one thing the model carries across
  sessions (ADR 32, 2026-10-05).
- `index-symbols`, a Tree-sitter outline, was removed on 2026-09-27: no
  model called it in 85 sessions, and it carried 1.1 MB of vendored
  grammars (docs/memory.md). A retrieval tool the model does not reach for
  is dead weight.
- The derived `<id>.idx` beside each log (ADR 49 §10,
  `core/log_index.go`) is the precedent for an index: checked against its
  source, rebuilt on any mismatch, safe to delete.
- No embeddings anywhere. Anthropic's API has no embeddings endpoint; a
  ChatGPT plan's credential reaches only `GET /v1/models` and
  `POST /v1/responses` (`keychain/openai.go:215-227`). An Ollama server is
  never keyed, so no binding check sees what is sent to it. A binding names
  host, method and key position but no path, so a key bound for chat at
  `api.openai.com` also covers `/v1/embeddings`. The judge's request
  (`prim/judge.go:302-375`: redact, hash, record `jev.req`, authorize, send,
  charge the budget) is the pattern for a remote call that is not a chat.
  Task spend counts only `llm.resp` and `jev.req` (`core/spend.go:18-33`).
- No search or storage library in `go.mod`; builds are cgo-free (ADR 30,
  ADR 42). A new dependency is a decision of its own (ADR 44, ADR 48).
- Primitives get raw values; only what is recorded is redacted
  (`core/events.go:12-37`, ADR 25). A Go indexer that read files itself
  would skip both the guard and redaction.

**What the person's logs show.** The 78 logs of 2026-09-30 to 10-09 hold
9,778 calls by models.

- *Searching.* `index-grep` 1,167 calls, a project's own `grep-repo` 447,
  `grep` in a shell 412, `fs-find` 60. Of the 1,614 regular-expression
  searches, 917 (57%) are alternations of three or more guessed names,
  such as `modelForm|credential|host|bind|freeClass|api_key`: the model
  looking for a concept by listing what it might be called. Search results
  are 18% of all tool-result bytes (`index-grep` 6.6 MB of 36 MB). There
  are 300 runs of three or more searches in a row.
- *Ranked words, replayed.* The searches were replayed as plain words
  against this commit's files, cut into the chunks of §6 and ranked by
  BM25 (a script, not nodwyn code). Of the 336 alternation searches
  followed by a read of a file they matched, ranking put that file first
  for 170 (50%) and in the top five for 275 (81%); the searches themselves
  had returned a median of 4 files (90th percentile 17). So ranked words
  find what the regular expression found, in a shorter list, and not more.
  Of the 143 runs of three or more searches that ended in a read, the file
  read was in the top five for the first search's words in 35 (24%), and
  for all the run's words together in 63 (44%).
- *Requests.* For the 46 requests that went on to edit files that still
  exist, the request's own text as the query put one of the edited files
  in the top five for 27 with words, 25 with vectors from
  `qwen3-embedding:0.6b` (run locally, each chunk cut at 800 characters)
  and 28 with both fused by reciprocal rank; in the top ten for 31, 35 and
  32. Asked without the instruction Qwen3's documentation puts before a
  query, the vectors gave 28 and 34. On 46 requests these differ by a few
  requests either way: this embedding model did not find more than words,
  and fusing the two did not add up what each found. The requests name
  code (`ScrubEnv`, `models.list`), which favours words; a larger or hosted
  model was not tried.
- *Reading again.* 2,172 of 3,864 `fs-read` calls read a path the session
  had read before: 420 after editing it, 110 after a compaction, 422 the
  same lines with neither, 1,220 other lines.
- *What the model stopped seeing.* The window elided 691 results in 8
  of the sessions; 129 calls then repeated exactly a call whose result had
  been elided (99 reads of a file not edited since, 13 `fs-list`, 13
  `git-status`). Compactions cleared earlier context in 3 sessions, and 51
  call repeats followed them. 51 shell results lost their middle: of the
  3,160 folds, 1,761 are `shell.scm` rendering a shell result as plain
  text, which keeps the first 1,500 and last 6,000 characters of its
  output, and 1,386 fold old reads to an outline (`hygiene.scm`). A
  repeated read of an unchanged file costs what a read back from the log
  would, and a repeated `git-status` is often meant, so the need the logs
  show for reading the conversation back is modest; it is asked for, and
  §13 measures its use. (A first count here, 370 repeats after a fold,
  took the shell rendering for a fold that hid the output; it does not.)
- *Across sessions.* One `project-notes-save` in all 78 logs, and a
  handful of requests that refer to an earlier task. There is little
  evidence yet for searching earlier tasks; it is asked for.
- *Embedding cost.* That run, `qwen3-embedding:0.6b` under Ollama 0.35 on
  this computer's CPU over loopback, took 702 seconds for the 4,201 chunks
  cut at 800 characters (3.2 million characters); whole chunks are about
  twice that. A hosted model at the catalogue's price for OpenAI's
  `text-embedding-3-small` ($0.02 a million tokens) would charge about
  four cents for nodwyn's 1.8 million tokens.

## The person's answers (2026-10-15)

1. Embeddings wait until there are models configured for them; the design
   keeps them addable (§10 stays the design, and R4 is not built).
2. Searching earlier tasks is off by default (§9).
3. An index build logs every file read's guard decision, and also a
   summary of each build or refresh (§3).
4. The conversation first (R2), the repository after (R1).

## Decision

### 1. Three primitives, offered as tools

- `(index-search query scope [limit [path]])` ranks what matches
  `query`, plain words or names: `scope` is `"repo"`, `"conversation"` or
  `"earlier"`, and is always given, so that building the conversation
  first changes no default later; `limit` is the number of results, 8
  unless given, at most 20; `path`, added with the repository, narrows it
  to a directory or file. Read-only, no approval.
- `(index-update [path])` brings the index of the repository, or of
  `path`, up to date and says what it covers. It writes nothing in the
  repository, so it asks nothing.
- `(recall seq [start [count [task]]])` returns one event of this
  conversation by its seq, as `[elided seq=N]` names it, or with `task`,
  added with earlier tasks, of that task when allowed: whole or as lines
  `start` to `start+count-1`, as the log recorded it. Read-only, no
  approval.

`index-grep` stays: an exact string or a pattern is its job, and the
replay shows it finds what it looks for. The new search is for what the
model can describe but not spell. `recall` is separate because it takes a
reference, not a query, and `index-update` because the person asked for an
explicit build the model chooses to pay for.

### 2. What the repository index covers

In a git repository, the files git lists: `git ls-files -z --cached
--others --exclude-standard`, run with fixed arguments and the hardening
`git-status` has (no external programs, no file system monitor), which is
what git already records about which files belong to the project.
`.nodwyn/` is left out but for its tracked files, so other sessions'
worktrees, attachments and kept versions never enter. Elsewhere, the walk
`index-grep` does (hidden entries, symbolic links and ignore files
skipped). `index-update path` with a directory indexes it whole, ignored
files included, within the root. A workspace mount (ADR 22) is not
indexed: not built.

A repository's `.gitignore` is written by whoever commits to it, a
stranger's clone included. It can only narrow what is indexed: every file
that enters is read through the guard (§3), so an ignore file can hide a
file from the search but cannot expose one the guard refuses, and
`index-grep` and `fs-read` still reach a file it leaves out.

Limits: a file is indexed whole up to 1 MiB (`fs-read`'s whole-file cap);
binary and non-UTF-8 files are skipped; a tree indexes at most 20,000 files
and 128 MiB of text. Past that it indexes what fits in path order, and
every search says the index is partial and how to narrow it. nodwyn is 712
files and 7.2 MB.

### 3. Every file is read through the guard, and the index holds words, not text

The indexer reads each file by applying `fs-read`, as `index-grep` does:
the repository pin, the denylist, the person's `register-check` checkers
and the turn's budget all apply, and each read logs its `guard.decision`
(about 300 bytes; 701 files is about 210 KB, once per tree, then only for
files that changed). A read the guard refuses leaves the file out and
names it in the result; it is never indexed as an empty file. A read the
guard would ask about is not asked: the file is left out and named, so a
build never prompts once per file. Each build or refresh also records one
summary of what it read, skipped and was refused, beside those per-file
decisions (the person's answer 3), so a log says what an index holds
without adding up its reads.

The text is redacted as the log redacts a tool result (`redact.Redact`
with the default scanner) before it is cut into chunks, so the index is
built from the text the model would read (ADR 43) and no known credential
shape reaches it. What the index stores is, per chunk,
its line numbers and its words with their counts: no text. What a search
shows, the chunk's first line included, is read again from the file at
that moment (§7).

Every refresh, and so every search (§5), checks each listed file against
the pin and the denylist without a read, as `index-grep`'s pre-filter does
(`index_grep.go:239-254`), and drops the ones refused, so a file the
denylist refuses now never ranks even if it was indexed before. The live read of each result applies the
whole guard again; a refusal there stops the search with the refusal as
the answer, as in `index-grep`, and drops the file from the tree's index.

An unknown-shape secret, which L1 does not redact, enters the index's
words as it would enter the log if the model read the file. The index
lives beside the logs and under the same protection.

### 4. Where it lives, who writes it, and how it is shared

In nodwyn's data directory, under the project's key, where the project
notes already are: `projects/<key>/index/`, with `<key>` the notes key
(`core/scratch.go:123-136`) of `Session.project()`, which is the main
repository for a session in one of its worktrees (ADR 34, ADR 45 §7).
Only nodwyn writes it. The model's file tools cannot reach the data
directory (the denylist), and a clone cannot plant or poison an index.

- A file's chunks and words are stored once, under the SHA-256 of its
  redacted text and the version of the chunker and tokenizer, so every
  worktree, branch and session of the project with that file shares them.
  A new worktree from the same base costs a listing and nothing else.
- Each working tree (the main checkout, each worktree) has a manifest:
  path, size, modification time, inode and content hash per file. It is
  updated as a policy file is (`policyfile.Update`): under a lock beside
  it, written to a temporary file and renamed into place.
- A manifest whose root is gone (a removed worktree) and stored files no
  manifest names for seven days are removed by the next update.
- Everything here is derived: a version mismatch, a file that fails to
  parse or a missing entry is rebuilt from the files, and deleting the
  directory loses nothing (SPEC, invariant 1).

Each session keeps the index it loaded from a manifest in memory and loads
it again when the manifest changes. A daemon's sessions on one tree each
load their own: sharing it is not built.

### 5. Kept fresh by the search that uses it

Every `index-search` of the repository lists the files and compares their
size, modification time and inode with the manifest, and reads again the
ones that changed (each read guarded and logged, §3), for at most 10
seconds; past that it answers from what it has and says how many files it
did not get to. The first search in a project builds the index; for
nodwyn that is 712 reads, which took 1.3 seconds, and a search afterwards
about 90 milliseconds. `index-update` does the same without the
10-second bound, for at most 60 seconds a call, and goes on from where it
stopped on the next call. Both run inside a tool call, so the turn's limits
check every read (`core/budget.go:76`, which checks a read and counts it as
no action) and any embedding request (§10) is charged to the turn; nothing
indexes in the background.

### 6. Chunks and ranking

A chunk starts at a line that begins, in its first column, a declaration
or a heading — `func `, `type `, `var ` and `const ` (Go), `(define`
(Scheme), `function `, `class ` and `export ` (JavaScript), `def ` and
`class ` (Python), `#` (Markdown), `\chapter` and `\section` (TeX), in
any file — once the chunk before it has 20 lines, and a chunk has at most
80 lines. No parser and no grammar: this is the chunker the replay
measured; nodwyn cuts into 4,298 chunks.

Words are split as code names them: `ScrubEnv` gives `scrub`, `env` and
`scrubenv`; `index-grep` gives `index`, `grep` and `index-grep`;
`max_result_tokens` its three words and the whole. They are lowercased.
The path's words and the chunk's first line count three times. Ranking is
BM25 (k1 1.2, b 0.75) over the chunks of the tree.

### 7. What a search returns

A header (how many chunks and files it ranked, and, when it applies, how
many changed files were not read again in time and that the index is
partial), then each result: `path:A-B: ` and the chunk's first line, then
its three lines that hold the most of the words matched, numbered as in
the file, read from the file now through `fs-read`, so the model goes on
with a ranged `fs-read`. The refresh before the search has read every
changed file; a chunk whose lines are gone by the time it is read is left
out. Results carry `external:file` provenance, as `index-grep`'s do, and
pass ordinary tool-result redaction.

### 8. The conversation

`index-search` with `"conversation"` ranks the events of this session that
a model could have seen: the person's messages, nodwyn's messages,
attached files, plans, summaries, replies with their tool calls, and tool
results with the call each answers (counted three times its output), in
the main context and in sub-agents' (labelled with the agent). Not guard,
egress, request or note records, not thinking, and not earlier searches
and recalls, whose results repeat other events and whose calls would make
a search find itself. Each result is `seq N` with what the event is, for a
result its call, and its three lines that hold the most of the words
matched, numbered as `recall` numbers them. The index is built in memory
at the first search or recall from the whole log (`Session.History`, which
reads from disk what a resumed session does not hold, ADR 49 §10) and
extended with the events recorded since; it is never written.

`recall N` returns that event as the log holds it, redacted, under a header
naming its kind, seq, agent and, for a result, its call; a long one is read
in lines as `fs-read` reads a file (`[lines A-B of N]`), within the
window's result cap. It reads only the kinds the search ranks. Its value is
tainted `external:log`, as `session-events`'s is, so a command line built
from it is a security confirm (L2). What a task recorded stays readable to
it: the guard decides reads of files, not of the log, as for
`session-events` today. A reply comes back with a line per call naming the
seq of its result, which an elided reply's window no longer shows.

The prompt's line about elided results names `recall` and `index-search`,
and keeps `fs-read` for a file as it is now (ADR 32, amended 2026-10-15).
Fold texts stay as they are: `hygiene.scm` folds reads of files, which
`fs-read` brings back as they are now, and `shell.scm`'s rendering hides
only the middle of long output.

### 9. Earlier tasks, when the person allows it for the project

`index-search` with `"earlier"` ranks the same kinds of event in the
project's other tasks: those whose project (`SessionInfo.Project()`) is
this session's, archived ones included and marked, deleted ones gone with
their logs (ADR 39). A worktree made by hand is a project of its own, as
ADR 34 has it. Each result names the task, its date and the seq, which
`recall` takes with the task's id.

It is off unless the person allows it for the project, on this computer:
`earlier_tasks = true` on the project's entry in `workspaces.toml`, the
per-project registry in the configuration directory that only the console
writes. A clone cannot write it and the model's tools cannot reach it. The
console's Workspace tab has the switch; setting it is refused to a paired
device, as Settings is (ADR 30). It is read on each call, so it takes
effect at once. Off, the scope answers that searching earlier tasks is off
for this project and that a person can allow it in the Workspace tab, so
the model can ask.

Each earlier task's words are kept in `<id>.ridx` beside its log, derived
like `<id>.idx`: checked against the log's end, extended with what the log
gained, rebuilt on any mismatch, deleted with the log.

### 10. Embeddings, when the person chooses a model for them

Off unless the person chooses an embeddings model in Settings › Models:
a server already configured and a model on it, `[index] embed_server` and
`embed_model` in `config.toml`. The server's kind must have an embeddings
endpoint: Ollama's `/api/embed` or an OpenAI-compatible `/v1/embeddings`.
Anthropic's has none and a ChatGPT plan's credential cannot reach one, so
neither is offered. Choosing it is consent to send the redacted text of
every indexed chunk, and each search's query, to that server, as choosing
a model is consent to send it the conversation (ADR 27); Settings says so
beside the choice.

- Each request is made as the judge's is: redacted text, a hash, the key
  through `Session.authorize` for a keyed server (binding, `egress.check`,
  HTTPS unless loopback, no redirect), and a record, `embed.req` (server,
  model, inputs, bytes, hash, usage, cost), which the budget and the task's
  spend count (`core/spend.go` reads it beside `llm.resp` and `jev.req`).
  It is a new kind: ADR 2 is amended, and an older binary cannot read a log
  that holds one.
- Vectors are stored per embeddings model under the same content hash as
  the words, so they are shared as they are; a chunk's vector is made once
  per model and content. 4,201 chunks at 1,024 dimensions in float32 is
  17 MB. A query compares against every vector; under 50,000 chunks that
  takes milliseconds, so there is no approximate index.
- Ranking fuses the words' and the vectors' top 400 by reciprocal rank
  (k 60). A chunk without a vector yet ranks by words alone, and the
  header says what share has vectors.
- `index-update` embeds the chunks that lack vectors, in batches, within
  its 60 seconds a call, and goes on at the next call. `index-search`
  embeds only its query.
- A query is sent as it is, with no instruction before it: on the
  requests above, the one Qwen3's documentation asks for changed nothing
  measurable.

Whether the conversation and earlier tasks are embedded too is left to
what their word searches miss once they are used.

### 11. The default loop

`index-search` and `recall` join `look-tools` (`strategy.scm:148`) with
the conversation, so rounds spent searching it count as reading. With the
repository, `index-search` also joins the lists of the sub-agents that
read it: the plan explorer's (`plan.scm:102`), `second-look-tools`
(`checkloop.scm:57`) and `optimizer-may-call?` (`optimize.scm:90`); before
that it searches only a conversation they do not need. `index-update`
joins none: a sub-agent's rounds are few, and a search builds what it
needs. Each tool's
description says when to use it rather than `index-grep`. One working rule
is added, from the 57% of searches that were alternations: to find code
by what it does or under several possible names, search for the words with
`index-search`, and use `index-grep` for an exact string or pattern.

### 12. Clients

The model's calls show as tool calls in every client, as any tool's do.
The console's Workspace tab shows the index (files, chunks, when updated,
size, the share with vectors) with a button that forgets it (`index.status`,
`index.forget`), and the switch of §9. Settings › Models has the
embeddings choice of §10. The TUI, line mode and the CLI have none of
these: not built, recorded in `docs/clients.md`.

### 13. How it is judged

From the logs once it is in use, against the numbers above: the share of
searches that are `index-search`; the share that are alternations of
guessed names (57%); runs of three or more searches (300); calls repeated
after their result was elided (129); `recall` calls on elided seqs; and,
where the person allows it, searches of earlier tasks. On the SWE-bench
cohort of ADR 32, when the person asks for a run, the resolved count and
tokens with and without the tools. If the model does not use
`index-search` where it used to list names, it is removed, as
`index-symbols` was; the conversation scope and `recall` stand or fall on
their own counts.

## Consequences

- Every session offers three more tools, and its system prompt gains one
  rule and a changed line.
- A session's first search in a project reads every file and logs a guard
  decision for each; later ones read only what changed. A worktree shares
  its repository's index.
- nodwyn's data directory gains an index per project: 6.5 MB of words
  for nodwyn's 712 files (7.2 MB of text), more with vectors.
- The window, its recipe and replay are unchanged: results are tool
  results.
- With embeddings chosen, a project's text goes to that server, once per
  content and model, and each query too.

## Order of work

Each slice is built end to end and run where it runs, with fakes only
(AGENTS.md).

1. **R1, the repository:** §1 to §7 for `"repo"`, `index-search` and
   `index-update`, §11's tool lists, description and rule. Tests: a
   temporary repository with a denied file, a `register-check` that
   refuses one, an ignored and an untracked file, a worktree sharing the
   main checkout's entries, a file changed between searches, a canary
   credential that never reaches the index or the log; the replay above
   as a test of ranking on a fixture. Docs: docs/scheme.md, the book's and
   the expert guide's primitive tables, docs/memory.md, docs/manual.md,
   README.md's retrieval paragraph, the book where it says there is no
   index (`idea.tex`, `exists.tex`), SPEC's Memory section,
   docs/clients.md, an ADR 32 amendment, the catalogue.
2. **R2, the conversation:** §8, `recall`, the prompt's line and fold
   texts. Tests in `internal/core`: an elided result recalled, a range of
   lines, a sub-agent's result, taint on the value, resume rebuilding the
   index. Docs: as R1's for a primitive, and an ADR 32 amendment.
3. **R3, earlier tasks:** §9, the switch, `<id>.ridx`, the Workspace tab
   (index status and forget included), `workspace` RPC fields. Tests:
   another project's tasks never ranked, a deleted task gone, the switch
   off and on, refused to a paired device; a browser check that turns the
   switch on and off and forgets the index, checking the request and the
   state after each. Docs: docs/protocol.md and the schema,
   docs/clients.md, docs/manual.md, an ADR 22 amendment.
4. **R4, embeddings:** §10, an `/api/embed` handler in `test/fakes/ollama`
   and `/v1/embeddings` in `test/fakes/chat` with vectors seeded from the
   text, `embed.req`, spend and budget, the Settings choice and its browser
   check. Docs: an ADR 2 amendment, docs/manual.md, docs/clients.md,
   docs/memory.md, the book.

R2 depends on nothing in R1 and is the smallest, and goes first (the
person's answer 4); then R1, then R3. R4 waits for models to embed with
(the person's answer 1). R2 and R1 are built.

## R2 implemented — 2026-10-15

- `internal/retrieval`: `Words`, which splits text as code names it, and
  `Index`, BM25 over weighted fields, in memory. It imports only the
  standard library; R1 ranks chunks with it, and R4 would fuse a second
  ranked list with its own, not change it.
- `internal/core/recall.go`: the session's `conversation`, built at the
  first search or recall from `Session.History` and extended from
  `EventsSoFar`, and the primitives `index-search` (scope
  `"conversation"` only; any other is an error saying the repository and
  earlier tasks are not built) and `recall`. Both are offered as tools,
  tainted `external:log`. A result's call is known from its reply's calls
  as well as from `tool.call`, as the window pairs them.
- A result is read as text with lines: a shell's as `shell.scm` renders
  it for the model, but whole, and other JSON indented. Tried on a copy of
  the person's largest log (6.7 MB, 6,849 events, 1,143 of them indexed in
  0.3 s, each search in under 4 ms), a recalled shell result was otherwise
  one line of JSON, cut at the result's cap and not to be paged.
- `prim.ReadLines`: a ranged `fs-read`'s line reader over any text, so
  `recall` pages an event as `fs-read` pages a file, within the same cut
  (`Session.rangeBytes`, which `fs-read`'s options now use too).
- The default loop: the prompts' line on older results, and `look-tools`
  (ADR 32, amended 2026-10-15).
- Not built here: `index-search`'s `path`, `recall`'s `task`, and the
  rest of R1, R3 and R4.

Tests: `internal/retrieval` (how names split, ranking by rare words and
weighted fields, ties in the order added);
`TestModelSearchesAndRecallsItsConversation` (the model's search through
the fake model finds a result it read, redacted, without finding its own
call; recall whole, in a range, a reply naming its result's seq, the
provenance, no match, and each error) and
`TestRecallReadsAResumedTasksWholeLog` (a 2,000-event log resumed holding
its end: recall of its first result, a search counting all its events, a
sub-agent's result named as the explorer's, and a compaction not taken for
an agent's context), both in `internal/core`. They fail on the code before
this change: the tools do not exist.

## R1 implemented — 2026-10-15

- `internal/retrieval`: `Chunks` (§6), `Store` (§4: chunks by hash,
  a manifest per tree, `Collect`), and `Index.AddCounts` and a filter on
  `Search` for a search of one directory.
- `internal/prim/repoindex.go`: the refresh (§2, §3, §5), the in-memory
  index, `index-search`, which moved here beside `index-grep` with its
  conversation scope given by the core (`Options.Conversation`), and
  `index-update`. `Options.IndexDir` is the project's
  `projects/<key>/index`; a session without a data directory has no
  repository index and is not offered `index-update`.
- `guard.WithoutAsking`: under it a confirm is a refusal nobody is asked
  for, recorded with the answer `no` and the actor `not asked`. It only
  tightens.
- `index.update` (ADR 2, amended): recorded by every `index-update` and by
  a search that read or removed a file.
- The default loop: the rule on reading code, and the explorer's,
  second look's and optimizer's tools (ADR 32, amended 2026-10-15).
- The console's Connections view counts the built-in tools, and the
  `screens` browser check expected 17. R2 made them 19 and its commit
  left the check failing: it changed no console file, so its validate ran
  no browser check. The check now expects 20, `index-update` included.
- Not built here: indexing a workspace mount, a daemon's sessions sharing
  one loaded index, R3 and R4.

Tests: `internal/retrieval` (chunks, the store, a filtered search);
`internal/prim` `TestRepoIndexSearchesWhatGitLists` (tracked and
untracked files in, ignored, denylisted and `.nodwyn/` files out, no
credential in the store, a guarded read per file, one record, a directory,
provenance, no match), `TestRepoIndexFollowsChanges`,
`TestRepoIndexIsSharedByTrees` (a second tree stores nothing new; a gone
tree's manifest goes), `TestIndexUpdateAddsAnIgnoredDirectory` and
`TestRepoIndexLeavesOutWhatTheGuardRefuses` (a confirm refused unasked
and named; an indexed file the guard now refuses is the answer and leaves
the index); `internal/guard` `TestWithoutAskingRefusesAConfirmUnasked`;
`internal/core` `TestModelSearchesTheRepository` (through the fake model:
the result redacted, the index beside the project's notes, the record, the
build's and the result's guarded reads). On this repository at this commit
a build read 712 files in 1.3 seconds into 6.5 MB, and a search took 90 to
120 milliseconds.

## Not decided

- Whether embeddings come with R1 instead of last: answered, they wait
  (the person's answer 1). On the person's requests a small local
  embedding model found no more than words (Context); a larger or hosted
  model might, and R4 measures that on the searches R1's logs record.
- A reranker over the top results (a Qwen3 reranker is on this computer):
  measured after R4, if R4 is kept.
- Splitting chunks with Go's own parser (`go/parser`, no new dependency)
  instead of the line rule, if Go results are cut badly.
