# ADR 0009: Context and cache discipline

- Status: accepted
- Date: 2026-09-20

## Context
workshop serves one request at a time with a 98,304-token context. A cold prompt
evaluation of a large prefix costs tens of seconds on the RTX 4090; a warm one
is free because Ollama caches the KV prefix.

## Decision
Window budget is 80 Ki input plus 8 Ki reserved for generation
(`[window]` in config, validated against `num_ctx`). The request prefix —
system prompt, then tool schemas in stable order, then messages in
append-only order — must be byte-stable between turns. Elision replaces
payloads in place (`[elided seq=N]`) and never reorders or rewrites earlier
turns. Model `thinking` is logged but never fed back into the window.

## Consequences
- `dev/*.scm` loads before the first message, never mid-session, so the tool
  schema block does not change.
- A sub-second `prompt_eval_ms` after an elision is the acceptance check (the
  native API reports eval time, not cached tokens).

## Amendment 2026-09-21: hysteresis, sticky elision, the task block

The single `input_budget` did not deliver cache hits in practice. Once the
window sat at the budget, every request elided the next-oldest live entry,
which is the front of the live region, so the whole live tail re-prefilled
each time (40–48 s at 85K tokens on workshop); and because elision was
recomputed from scratch, a large result ageing out of the protected tail
could *un-elide* earlier entries and break the prefix even earlier.

Now: (1) elision is **sticky** — every earlier `llm.req.elided` set stays
elided, derived from the log; (2) **hysteresis** — above `high_water` the
window is trimmed, oldest first (tool results, then assistant text, never
user messages), down to `low_water`, then left alone until it crosses
`high_water` again; (3) tool results over `max_result_tokens` are shown
head+tail with an omission marker, deterministically; (4) the system prompt
carries the **current task** (the user message that started the turn), so
the ask is never trimmed and the prefix changes only at turn boundaries.
Defaults: 60Ki / 30Ki / 3Ki. Expected cost: one full prefill per trim
(every ~30K tokens of growth) and one per user turn, instead of one per
request.


## P10 request replay foundation — 2026-09-22

Each new `session.start` and `session.resume` records its redacted window recipe:
base system prompt, frozen model-facing tool catalog, window limits, context
size, keep-alive and algorithm version. Each loop `llm.req` records a SHA-256 of
the provider-neutral request immediately before sending it. Conversation text is
still derived from the log rather than copied into every request record.

`nodwyn replay SESSION --window SEQ` folds only the prefix preceding that request,
using the latest recorded runtime recipe. It outputs the reconstructed request
only if its canonical JSON digest matches. Current configuration and init files
are never consulted for reconstruction; no model or tool runs. Tests compare
its canonical bytes to the actual provider inputs, including resume with changed
settings/catalog, capped tool output, sticky elision and redacted text.

This digest detects reconstruction drift; it is not an authenticated log seal.
It covers the provider-neutral request, not an HTTP transport's encoding.
Legacy requests without a recipe/hash and unknown algorithm versions fail
explicitly. Ordinary transcript replay remains available for old logs. Partial
or malformed logs are rejected by exact CLI replay. Summaries and the structural
index remain subsequent P10 work; this slice changes no window-selection policy.


## P10 revocable summaries — 2026-09-22

User-reviewed summary files can now be appended to a closed session's log with
explicit source bounds and revoked by summary event sequence. The store acquires
the existing exclusive log lock, validates source boundaries, and uses the log's
mandatory redaction/append path; it never constructs a runtime or replays init.
A summary's source digest is derived from the redacted original conversation.

`window-v2` replaces eligible older spans, keeping the latest two user turns
outside summary replacement and retaining existing token caps/elision. Newer
overlapping caches supersede earlier ones; dropped/stale caches expose source,
not superseded cache text. A lifecycle edit resets derived elision state before
rebuilding. Summary text is an assistant history message, not a system prompt.
`window-v1` remains executable for exact historical request replay. Later edits
cannot alter a reconstructed earlier request. See the memory guide for controls
and limits. Automatic text generation is not part of this slice.

## Amendment: headers and tool-less requests in the recipe — 2026-09-27

The window's task and plan headers are Scheme prompts (`task-header`,
`plan-header`), read once after `init.scm` and fixed for the session like the
system prompt; the recipe records them (window-v6), and an absent header
means the default text, so older logs rebuild byte for byte. A model turn may
offer no tools (`(model-turn name '((tools . #f)))`, the planner's); its
`llm.req` records `no_tools` and replay omits the catalog for it
(window-v7). Every request also caps the model's output at
`window.output_reserve` (window-v8: `max_output` in the recipe), so the
prompt and the reply fit `num_ctx`. Recipes v1 to v7 still replay under their
own rules.
