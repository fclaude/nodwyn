# Session memory

The lossless session log remains the source of truth. The window, summary
selection and structural retrieval are derived views. Memory never restores
credential permissions, approvals or Scheme state.

## Exact request replay

For logs recorded since v0.27, find an `llm.req` sequence in the transcript/log
and run:

```sh
nibb replay SESSION --window REQUEST_SEQ
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
nibb summary --from 12 --through 41 --text summary.txt SESSION
nibb summary SESSION
nibb summary --drop SUMMARY_SEQ SESSION
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

`window-v2` records this policy. Historical `window-v1` requests retain their
original policy. Replaying an earlier request uses only the log prefix before
that request, so a later revocation cannot change what the model originally saw.
The checked-in v0.27 selfcheck fixture exercises real v1 compatibility; synthetic
and fake-service tests separately exercise replacement, revocation and canaries.

## Repository text retrieval

`index-grep` is a read-only Scheme primitive and model tool:

```scheme
(index-grep "internal/window" "func (Build|ReplayRequest)")
(index-grep "README.md" "summary")
```

It returns an object/alist with `matches` (path, one-based line, text), `files`
searched, `skipped` candidates and `truncated`. Results are sorted by path and
line. An empty match list is an ordinary no-match result. A truncated result is
explicitly incomplete: narrow the path or pattern. There is no persistent index;
every call observes current files under that session's repository root.

Install [ripgrep](https://github.com/BurntSushi/ripgrep#installation) (`rg`) on the
host running the session, including a daemon host. The binary still builds with
cgo disabled on Linux and macOS. Missing ripgrep produces an actionable tool
error and does not prevent nibb from starting. CI installs it to exercise real
searches, rather than skipping those tests.

Discovery skips hidden paths and symlinks. It does not load ignore files or
ripgrep configuration: an ignore file could itself link to protected content.
Non-hidden ignored files are therefore candidates; select a narrower directory
to exclude build/vendor output. Explicit file paths may select hidden files,
subject to the same guard. Nonregular,
protected, oversized, binary and non-UTF-8 candidates are skipped. The `skipped`
count excludes paths that discovery never selected (for example hidden files).
Each selected content read goes through `fs-read` and the full shared guard,
including extension checkers. A denial stops the search; it is never treated as
an empty file. Results carry file provenance and pass ordinary tool-result
redaction before logging or entering a model request.

Searches accept no arbitrary command flags, invoke no shell or preprocessors,
and inherit no host credential environment. Ripgrep matches text supplied on
stdin after its guarded read. Limits are 128 discovered files, 100 matching
lines, a 4 KiB pattern, the existing per-file read/output caps, and 30 seconds
for the whole query. Oversized discovery or raw structured output returns an
error asking for a narrower search; it never reports a partial JSON prefix as
complete. Cancellation stops the active child process. File edits concurrent
with a query retain the ordinary filesystem read semantics; this is not a
transactional repository snapshot.

## Source outlines (removed)

`index-symbols`, a Tree-sitter outline of one Go, JavaScript or Python file,
was removed on 2026-09-27. Models did not use it (no call in 85 local
sessions, against hundreds of `fs-read` and `shell-exec` calls), and it
carried about 1.1 MB of vendored grammar binaries plus a WebAssembly runtime:
the binding's Go module embeds only JavaScript and JSON, so Go and Python
could not come from it. `index-grep` and `fs-read` cover retrieval.

There is no cross-session memory, semantic index or automatic summary generator.
Those are not claims made by this P10 implementation.
