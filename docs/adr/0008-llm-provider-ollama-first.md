# ADR 0008: LLM client is a Provider interface, Ollama first

- Status: accepted
- Date: 2026-09-20

## Context
The control-plane model is a local Qwen served by Ollama on `workshop`
(plan §3.4). It needs no key. Other providers may come later as data-plane
tools or as an alternate control plane.

## Decision
`llm.Provider` exposes streaming chat with tool schemas and a stop reason.
The first implementation speaks Ollama's native `/api/chat`, not the `/v1`
OpenAI shim: it streams, returns `thinking` as a separate field, returns
tool-call arguments as JSON objects, and accepts `options.num_ctx` and
`keep_alive`. The control plane is configured in `config.toml`
(`[control] provider url model num_ctx keep_alive`), not keyed; authorization
is the tailnet ACL. Keychain class `nodwyn-control` is reserved for a keyed
control plane and unused while workshop is the control plane.

## Consequences
- No secret exists on the build machine until P5 registers the Jev key.
- Swapping the control plane later is a config change behind the interface.
- Tests run against a fake Ollama; `make smoke` is a manual run against workshop.
- *Amended 2026-09-27 (ADR 27):* `[control]` may be any configured model,
  including a keyed chat-completions server, and has no built-in default.
