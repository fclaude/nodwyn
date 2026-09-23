# Session memory

The lossless session log remains the source of truth. The window, summary
selection and later structural indexes are derived views. Memory never restores
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

## Remaining P10 work

Session-scoped structural retrieval (`index-symbols` and `index-grep`) remains.
There is no cross-session memory, semantic index or automatic summary generator.
