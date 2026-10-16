# Session memory

The lossless session log remains the source of truth. The window, summary
selection and structural retrieval are derived views. Memory never restores
credential permissions, approvals or Scheme state.

## Exact request replay

For logs recorded since v0.27, find an `llm.req` sequence in the transcript/log
and run:

```sh
nodwyn replay SESSION --window REQUEST_SEQ
```

The command rebuilds the provider-neutral request from its preceding log prefix
and recorded runtime recipe, then verifies its digest before printing JSON.
It does not load current tool definitions, call the model or run tools. Legacy
logs without the recipe/hash still support ordinary transcript replay, but
cannot establish exact request reconstruction. Unknown recipes and mismatches
are errors, not guessed output. The digest is not an authenticated log seal.

## Reviewable, revocable summaries

This first summary slice accepts user-authored text. It does not generate text
with a model or rewrite the source conversation. Close the session before editing;
an attached or detached daemon session still owns its log until explicitly closed.
The CLI refuses to race an active owner. Incomplete logs must be recovered by the
normal resume path before summary editing.

Select a span beginning at a `user.msg` event and ending at a completed
`llm.resp` without pending tool calls. Put its reviewed summary in a UTF-8 file
(maximum 16 KiB). For example:

```sh
nodwyn summary --from 12 --through 41 --text summary.txt SESSION
nodwyn summary SESSION
nodwyn summary --drop SUMMARY_SEQ SESSION
```

Flags precede the session ID. Creation prints the new summary event sequence;
use that sequence with `--drop`. Inspection lists every summary and its status:
`active`, `recent`, `dropped`, `stale`, or `superseded`. Text passes the same
mandatory redaction boundary as other log content. Operations append `summary`
or `summary.drop`; neither rewrites original events or creates another database.
Transcript replay and the TUI show those records without reapplying an edit.

The window replaces an eligible older span with one assistant message labeled
as a revocable summary, never with a system instruction. The latest two user
turns are excluded from summary replacement. Existing token-budget elision and
large-result caps still apply; this is not a claim that unlimited recent tool
output is sent verbatim.

A summary carries a digest of its redacted source conversation. Missing or
changed source invalidates it and leaves original conversation events available.
A newer overlapping summary supersedes an older one; revoking or invalidating
the newer cache does not revive the older cache. Summary edits reset derived
sticky-elision decisions, then the budget policy recomputes from source. This
makes revocation effective rather than leaving an old cached omission in place.

Recipes from `window-v2` on record this policy (new sessions record
`window-v15`, which adds later window features such as attached images,
a run's carried plan, the model's thinking on its replies, and the request
and its plan in the conversation rather than the system prompt);
historical `window-v1` requests retain their original policy. Replaying an
earlier request uses only the log prefix before
that request, so a later revocation cannot change what the model originally saw.
The checked-in v0.27 selfcheck fixture exercises real v1 compatibility; synthetic
and fake-service tests separately exercise replacement, revocation and canaries.

## Repository text retrieval

`index-grep` is a read-only Scheme primitive and model tool:

```scheme
(index-grep "internal/window" "func (Build|ReplayRequest)")
(index-grep "README.md" "summary")
(index-grep "." "TODO" "*.go" 2)    ; optional glob, and up to 5 lines of context
```

It returns an object/alist with `matches` (path, one-based line, text, and
with context the lines before and after), `files` searched, `skipped`
candidates and `truncated`. Results are sorted by path and
line. An empty match list is an ordinary no-match result. A truncated result is
explicitly incomplete: narrow the path or pattern. There is no persistent index;
every call observes current files under that session's repository root.

`fs-find` lists file names the same way, without reading them.

Search runs entirely in Go and needs no program on `PATH` (before 2026-09-30
it needed ripgrep, `rg`, which an app on a machine without developer tools
lacked). Discovery walks the directory in path order and lists regular files
only: it skips hidden entries below the path searched (a name starting with a
dot, `.git` included) and symbolic links, follows no link, and loads no
ignore file, since an ignore file could itself link to protected content.
Non-hidden ignored files are therefore candidates; select a narrower
directory, or pass a glob, to leave out build and vendor output. An explicit
file path may name a hidden file, subject to the same guard.

A glob follows ripgrep's `--glob`: `*`, `?` and `[...]` match within a path
component, `**` spans directories and `{a,b}` is either alternative; a glob
without a slash matches any component (`*.py` at any depth, `tests` for every
file under a `tests` directory); one with a slash matches from the start of
the path, relative to the directory searched or to the repository root, and
matching a directory matches the files under it; a leading `!` keeps every
path the rest does not match.

Nonregular, protected, oversized, binary and non-UTF-8 candidates are
skipped; the `skipped` count excludes paths discovery never listed (hidden
files, for example). Each selected read goes through `fs-read` and the full
shared guard, including extension checkers. A denial stops the search; it is
never treated as an empty file. Results carry file provenance and pass
ordinary tool-result redaction before logging or entering a model request.

A pattern is a Go regular expression (RE2, the syntax ripgrep also uses), at
most 4 KiB; it is never a command-line option. Limits are 3000 candidate
files (each read's guard decision is logged, so this also bounds the log),
100 matching lines, the per-file read and output caps, and 30 seconds for the
whole query; more candidates is an error asking for a narrower path or a
glob, never a partial list. Cancellation stops the walk. File edits
concurrent with a query have ordinary filesystem read semantics; this is not
a transactional snapshot.

## Source outlines (removed)

`index-symbols`, a Tree-sitter outline of one Go, JavaScript or Python file,
was removed on 2026-09-27. Models did not use it (no call in 85 local
sessions, against hundreds of `fs-read` and `shell-exec` calls), and it
carried about 1.1 MB of vendored grammar binaries plus a WebAssembly runtime:
the binding's Go module embeds only JavaScript and JSON, so Go and Python
could not come from it. `index-grep` and `fs-read` cover retrieval.

## Compaction

Within a session, long work is compacted: just under the window's high water,
or when you send `/compact`, the model writes a handover note and the
conversation goes on from it, in that turn and later ones (the manual,
"Changing the loop"; docs/scheme.md §9.2). The note is model-written and
narrows the window (`context.narrow`); it is not a `summary` record, and the
log keeps the whole conversation.

## Conversation retrieval

What a model sees of a long conversation shrinks: the window elides old
replies and results (`[elided seq=N]`), strategies fold results to an
outline, shell output keeps its first 1,500 and last 6,000 characters, and a
compaction leaves a note. Two read-only primitives, offered as tools, read
the rest back from the log (ADR 50):

```scheme
(index-search "go test core failure" "conversation")   ; the best 8 events
(index-search "ScrubEnv" "conversation" 20)            ; up to 20
(recall 812)                                          ; seq 812, whole
(recall 812 200 50)                                   ; its lines 200 to 249
```

`index-search` ranks the events a model could have seen: the person's and
nodwyn's messages, attached files, plans, summaries, replies with their tool
calls, and tool results with the call each answers, in sub-agents' contexts
too, each named by its seq. Not guard, egress, request or note records, not
thinking, and not earlier searches and recalls, whose results repeat other
events (a search would otherwise find its own call). Words are split as code
names them, so `ScrubEnv` matches `scrub` and `index-grep` matches `grep`,
and events are ranked by BM25: more of the query's words, and rarer ones,
rank first, and a result's call counts three times its output. Each event
found shows its three lines that hold the most of the words, numbered as
`recall` numbers them. The repository's scope and earlier tasks' are
below.

`recall` returns one of those events as the log recorded it, already
redacted, under a heading naming it (`seq 812, the result of shell-exec
{…}, which failed, in the explorer's context`), in lines as a ranged
`fs-read` returns a file: `[lines A-B of N]`, stopping at a whole line
within the window's cut for one result. A tool's result is recorded as the
tool returned it, and a shell's or a search's is one line of JSON, so both
primitives read a result as text with lines: a shell's as the model read it
(`exit N`, the output, then `[stderr]` and the errors, as `shell.scm`
renders it) but whole, other JSON indented. A reply names the seq of each of its
calls' results. It shows what was then: a file as it is now is `fs-read`'s.

The index is built in memory from the whole log at the first search or
recall, reading from disk the part a resumed session does not hold (ADR 49
§10), and extended with what is recorded after. It is never written, so it
is a view of the log and nothing else (SPEC, invariant 1). Both values
carry `external:log` provenance, as `session-events`'s do. A log of
gigabytes (ADR 49) would make that first build slow and the index large;
none is that size yet.

## The repository index

`index-grep` reads every candidate file on every call and keeps nothing.
`index-search` with scope `"repo"` ranks chunks of the repository's files
from an index nodwyn keeps (ADR 50):

```scheme
(index-search "credential binding check egress" "repo")        ; the best 8
(index-search "deploy" "repo" 20 "docs")                       ; only docs/
(index-update)                                                ; build it all now
(index-update "third_party")                                  ; add an ignored directory
```

Which files: those git lists, tracked or untracked and not ignored (in
`.nodwyn/` only tracked ones, so other sessions' worktrees and attachments
never enter); without git, the walk `index-grep` does. `index-update path`
adds a directory or file of the repository whole, git's ignores
notwithstanding, from then on. A repository's `.gitignore` is written by
whoever commits to it, a stranger's clone included; it can only narrow
what is indexed, since every file enters through the guard. At most 20,000
files and 128 MiB of text a tree; a file over 1 MiB, binary or not UTF-8
is skipped, and past the limits a search says the index holds only part.

How a file enters: through `fs-read`, so the pin, the denylist, a
project's `register-check` checkers and the turn's limits apply, and each
read logs its `guard.decision`. A read the guard would ask about is not
asked (`guard.WithoutAsking`): the file is left out and named, as one it
refuses is. The text is redacted as the log redacts a tool result, then cut
into chunks: a chunk starts at a line beginning a declaration or a heading
in its first column, once the chunk before has 20 lines, and has at most
80. What is stored is each chunk's line numbers and its words with their
counts, never text.

Where: `projects/<key>/index` in nodwyn's data directory, beside the
project's notes, so the model's file tools cannot reach it and a clone
cannot write it. A file's chunks are stored once under the hash of its
redacted text, so the worktrees and branches of a repository share them; a
manifest per working tree records each file's size, modification time,
inode and hash, updated under a lock and renamed into place. All of it is
derived: deleting the directory loses nothing.

Fresh: every search first reads again, for at most 10 seconds, the files
whose size, time or inode changed, and drops the ones gone; the first search
builds the index (nodwyn's 712 files took 1.3 seconds, and a search about
90 milliseconds, on the computer this was written on). `index-update`
reads for up to 60 seconds a call and goes on at the next, removes the
manifests of working trees that are gone and chunks no tree has named for
seven days. A build or refresh that read or removed a file records an
`index.update` (an `index-update` call always does): files, read,
removed, skipped, refused, left to read; never content.

What a search shows is read from the file again, through `fs-read` and the
whole guard: `path:A-B: first line`, then the chunk's three lines holding
the most of the words, numbered as in the file. A refusal there is the
answer, and drops the file from the index. Ranking is BM25 over the words,
with the path and a chunk's first line counted three times. There are no
embeddings: they wait for a model to make them (ADR 50, the person's
answer 1).

## Earlier tasks

With the person's leave for a project, `index-search` with scope
`"earlier"` ranks the same kinds of event in the project's other tasks, and
`recall` with a task's id brings one back (ADR 50 §9):

```scheme
(index-search "why did the release fail" "earlier")
(recall 812 0 0 "20261009T034803Z-3981612f")
```

The leave is `earlier_tasks` on the project's entry in `workspaces.toml`,
which only the console and the TUI on this computer write (the folder's
editor on Workspaces, `:earlier on`) and no model or repository can reach; it is read at
every call, so turning it on or off counts at once, and off is the default.
Which tasks: those whose project (`SessionInfo.Project()`) is this
session's, so a worktree's tasks are its repository's; archived tasks are
included and marked; a deleted task's log is gone and the task with it.
The events of all of them are ranked as one collection, a word weighing by
how many events of all the tasks hold it, a tie going to the newer task.
Each result names its task (id, name or first message, the day of its last
event, whether it is archived) and shows its three best lines, read from
its log again by its seq.

Each log is indexed in memory once per process, shared by the process's
sessions as the session list's cache is, and again only when its size or
modification time changed; its words and its events' headings are kept,
its texts read again when shown. On the computer this was written on,
the 73 earlier tasks of nodwyn among the 78 local logs (17,663 events)
were indexed in 3.6 seconds, read in parallel, and the cache held 87 MB;
a search after that reads it in about a millisecond. The memory is mostly
the words of tool results, and it grows with the logs and stays for as
long as the process runs. Nothing of it is written beside the logs.

## Task memory

A step up from events, each task keeps a card for every turn and the note
of every compaction (ADR 51): what was asked, how the turn ended, how long
it took and what it cost, its plan with the marks made, the files written,
the commands run and how they ended, errors, and the start of the answer.
The cards are made in Go from the log, so they cost nothing, and kept beside
it as `<id>.mem`, extended a whole turn at a time from where they end and
checked against the log line they end at, as the index is; deleting them is
safe.

```scheme
(memory-search "flaky pager test")                    ; this task's turns
(memory-search "flaky pager test" "project")          ; the repository's tasks
(memory-search "connection reset by peer" "task" #t)  ; the words as written
(memory-read "m:20261004T073130Z-40187c24:6-648:1")   ; a turn and its events
```

Cards and notes are ranked by BM25 over their own text, words split as
code names them. With `exact`, the tasks' messages, replies and tool output
are scanned for the words as written, newest first, each log read backwards
through its index's marks. A task's own memory is always searched; the
repository's other tasks' only under the leave above, `earlier_tasks`, read
at every call. The person searches them all from a terminal with
`nodwyn memory search` and `nodwyn memory read`.

Across sessions, a repository keeps its project notes (`project-notes`,
docs/scheme.md), its index (above), its tasks' memory (above) and, when the
person allows it, the model searches its earlier tasks (above); there is no
embedding index.
That is not a claim made by this P10 implementation.
