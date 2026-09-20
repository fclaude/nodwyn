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
- Reported `cached_tokens` after an elision is the acceptance check.
