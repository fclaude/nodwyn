# ADR 0002: Log format and event kinds

- Status: accepted
- Date: 2026-09-20

## Context
SPEC invariant 1: the log is the single source of truth; index and window are
derived, revocable views. Every later phase says "logged".

## Decision
One JSON object per line, one file per session under
`$XDG_DATA_HOME/nodwyn/sessions/<session>.jsonl`:
`{seq, ts, session, kind, payload}`. `seq` is 1-based and contiguous; a reader
rejects gaps and unknown kinds. The writer fsyncs after every event and never
overwrites an existing session file.

Kinds are enumerated in `internal/log`: `session.start`, `session.end`,
`user.msg`, `llm.req`, `llm.resp`, `tool.call`, `tool.result`,
`guard.decision`, `egress.check`, `gate.plan`, `cred.register` (class + id
only), `redact.event`, `summary`, `note`. Adding a kind is an amendment to this
ADR. Amendment (ADR 27): `context.fold` ends the innermost narrowed context
and hides it from later windows.

The writer has a payload `Filter` hook. The ingress redactor (P7) installs
itself there, so from P7 nothing reaches disk unredacted. The keychain file is
outside the log entirely.

## Consequences
- A crash between write and fsync leaves a partial last line; `Open` repairs
  it and reports `ErrTruncated` so the session can note the repair.
- `nodwyn replay` must reproduce a transcript byte-for-byte from this file
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

## Amendment 2026-09-22: budget checkpoint records

Add `turn.checkpoint` with dimension, used amount, soft/hard limits, a snapshot
of calls/tokens/actions/elapsed nanoseconds and attempted files/commands, the
chosen continuation and soft limit, and an optional cancellation/error string.
Core applies the ordinary ingress redaction before recording and emitting it.
The record is history, not an instruction to replay approvals. Readers predating
this kind reject it as unknown, following the existing schema policy.

## U2 approval and evaluation amendment — 2026-09-22

Add `approval.change` (actor, prompt ID, scope, command/write kind and derived
prefix), `eval.req` (redacted user Scheme source and optional actor), and
`eval.result` (redacted written value or error). Evaluations complete with the
existing `turn.end`; a request without a result can have an unknown outcome.
These events are audit history, not model conversation or executable replay.
`approval.change` records a requested scope before applying it; cancellation
or a later error may prevent that grant. Current session state is authoritative
for effective transient approval.

`guard.decision` gains optional `actor`, `prompt_id` and `scope`;
`turn.checkpoint` and `cred.register` gain optional actor and prompt ID.
Credential registration still records no secret value. All additions pass the
existing redaction/append path. Old records remain readable; older binaries
that do not know the new kinds cannot read logs containing them, consistent
with this ADR's strict reader. A guard decision that cannot be recorded stops
the host operation instead of allowing an unaudited effect.

## Amendment — 2026-09-30: `cred.refresh`

`cred.refresh` records one refresh of an OAuth2 credential (ADR 24 §3): the
class, the handle id, the token endpoint's host, the new expiry, whether the
refresh token rotated, and ok or the error. Never a token. It is written by
the session that made the use the refresh was for.

## Amendment — 2026-09-30: `plan.step`

`plan.step` records the model marking a step of the turn's recorded plan
done or skipped (the `plan-step` tool, answered by the strategy with
`record-step`): the step's number, the plan's step count, the state and the
model's note. It is the model's claim, not a proof, and the window ignores
it: the model already sees its call and the answer. A front-end shows the
plan's progress from it.

## Amendment — 2026-10-01: images in `attach` and `llm.req`

No new kind. An `attach` record may be of type `image` (ADR 22, amended
2026-10-01): it then has no text and never the pixels, but the media type,
width, height, the window's token estimate, the host its confirm named
(`destination`) and who answered the confirm (`approved_by`, `prompt_id`).
`llm.req` gains `withheld_images`: the attached images the request did not
carry, by attach sequence, and why (`missing`, `changed`, `no-vision`,
`elsewhere`). A declined image leaves a `note`. Both fields are optional,
so older records read as before.

## Amendment — 2026-10-01: `doc.upload`

`doc.upload` records one document sent to a document service (W5,
`google-doc-upload`): the tool, the call's origin, the file's absolute path,
media type, size and sha256, the destination host and the credential class,
whether the request was sent, whether it succeeded, and the document's id
and link or the error. The file's bytes never were a Scheme value, so no
guard record shows them; this one says what left the machine. Never the
content, never a token. A document refused before sending (a secret shape
L1 found, a file nodwyn cannot read) is recorded with `sent` false.

## Amendment — 2026-10-01: what a request cost

No new kind. `llm.resp` `usage` gains optional `cached_tokens` (a pointer:
absent means the provider did not say, which older logs and Ollama read
as), `cache_write_tokens`, `reasoning_tokens`, `cost` (USD) and
`cost_estimated` (the cost came from configured prices, not the provider).
`turn.checkpoint`'s budget state gains `cost`, `priced`, `unpriced` and
`cached_tokens`. An older record reads as unknown, never as free or as 0%
cached.

## Amendment — 2026-10-01: `mcp.egress`

`mcp.egress` records one attempt of a contained MCP server to reach a host
through its egress proxy (ADR 23 W3b): the server, the method (`CONNECT`,
or a plain-HTTP request's), the host and port, whether the proxy made the
connection, and why not. Never a path or a query, which can carry what the
server is sending. A session records up to 1000 per server; one more record
then gives the count left out (`dropped`). Records made while the session
is being built wait for `session.start`, as `egress.check` and
`cred.refresh` do; the last are written before `session.end`. The window
ignores them: they are audit history, not conversation.

## Amendment — 2026-10-02: `setting.change`

`setting.change` records a setting a session saved because a person chose
it at a prompt: who answered (`actor`, `prompt_id`), the setting (`key`,
such as `docs.google_upload`), its `value`, and whether it was saved in
`config.toml` (`state` `applied` or `failed`, with the `error`). It is the
answer to a `prompt.choice`, which, like every live prompt, is not logged
itself. A setting changed from Settings or by editing the file is outside
any session and leaves no record. The window ignores it.

## Amendment — 2026-10-02: the spend limit

No new kind. `turn.checkpoint` may have dimension `cost`, its `used` and
`limit` in millionths of a US dollar, and its budget state gains
`earlier_cost`: what the task's earlier turns cost. `turn.end` gains the
outcome `spend_limit`, and `run.state` a run's own spend limit, `cost`
(US dollars). All optional; an older record reads as before.
