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
`window-v14`, which adds later window features such as attached images,
a run's carried plan and the model's thinking on its replies);
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

Across sessions, a repository keeps only its project notes (`project-notes`, docs/scheme.md); there is no semantic index.
That is not a claim made by this P10 implementation.
