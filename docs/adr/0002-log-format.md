# ADR 0002: Log format and event kinds

- Status: accepted
- Date: 2026-09-20

## Context
SPEC invariant 1: the log is the single source of truth; index and window are
derived, revocable views. Every later phase says "logged".

## Decision
One JSON object per line, one file per session under
`$XDG_DATA_HOME/nibb/sessions/<session>.jsonl`:
`{seq, ts, session, kind, payload}`. `seq` is 1-based and contiguous; a reader
rejects gaps and unknown kinds. The writer fsyncs after every event and never
overwrites an existing session file.

Kinds are enumerated in `internal/log`: `session.start`, `session.end`,
`user.msg`, `llm.req`, `llm.resp`, `tool.call`, `tool.result`,
`guard.decision`, `egress.check`, `gate.plan`, `cred.register` (class + id
only), `redact.event`, `summary`, `note`. Adding a kind is an amendment to this
ADR.

The writer has a payload `Filter` hook. The ingress redactor (P7) installs
itself there, so from P7 nothing reaches disk unredacted. The keychain file is
outside the log entirely.

## Consequences
- A crash between write and fsync leaves a partial last line; `Open` repairs
  it and reports `ErrTruncated` so the session can note the repair.
- `nibb replay` must reproduce a transcript byte-for-byte from this file
  alone; anything that cannot be rebuilt from the log is a bug.
- Until P7 the log may contain what the model was shown. Bootstrap runs only
  inside the safety envelope (plan §3.3).

## Amendment 2026-09-22: resume lifecycle

Add `session.resume` (the current `SessionStart` configuration payload) and
`turn.end` (a note marking completion, cancellation or failure). Resume keeps
sequence numbers contiguous in the same file. A writer owns an exclusive file
lock so two processes cannot append concurrently. A store's unfinished-turn
indicator describes durable history, not process liveness. Older readers reject
the new kinds, as they do every unknown kind.

P7 redaction now occurs in `core.Record` before persistence and history insertion;
the log writer retains its defensive filter. Resume redacts legacy history before
it enters the model window. Original historical bytes are not rewritten.
