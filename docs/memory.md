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

## Source outlines

`(index-symbols "internal/window/window.go")` reads one file through `fs-read`
and the full guard, then returns a Tree-sitter outline. Supported files are Go
(`.go`), JavaScript (`.js`, `.mjs`, `.cjs`, `.jsx`) and Python (`.py`, `.pyi`).
Other extensions fail explicitly and suggest `index-grep`; there is no fallback
that guesses declarations with regular expressions.

The result contains `language`, `definitions`, `parse_errors` and `truncated`.
Each definition has a name, kind, one-based name line and ending declaration
line. Definitions stay in source order, including nested declarations. Go
covers functions, methods and named types; JavaScript covers named functions,
classes and methods; Python covers functions and classes. This is a syntax
outline, not type resolution, a call graph or a complete variable index.

Incomplete code can still yield useful definitions; `parse_errors` reports that
Tree-sitter recovered from syntax errors. At most 100 definitions and 100,000
tree nodes are visited. Names over 512 bytes are omitted rather than fabricated
by truncation; either limit sets `truncated`. The ordinary file/output caps
apply, with a 1 MiB parser input ceiling and a five-second parsing deadline.
Symlinks, protected files and non-UTF-8 content are rejected. Results retain file
provenance and the ordinary tool-result redaction path.

`internal/symbols` owns parsing only. Each call creates and closes an isolated
WebAssembly runtime with a 128 MiB guest-memory cap and cancellation enabled.
It receives no host filesystem, environment or network access, and stores no
persistent index or compilation cache. Grammars are bundled and version-pinned;
users do not need Tree-sitter, a native compiler, or runtime downloads. This
adds two Go dependencies: the thin upstream-runtime binding and wazero, keeping
all four existing Linux/macOS builds free of cgo. Asset provenance, update
instructions and redistribution notices live in
[the grammar directory](../internal/symbols/grammars/README.md). The standalone
binary includes those notices; `nibb licenses` prints them without loading a
session or contacting a service.

There is no cross-session memory, semantic index or automatic summary generator.
Those are not claims made by this P10 implementation.
