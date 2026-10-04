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

## Amendment 2026-10-04: Chat Completions has explicit server dialects

Cerebras returned reasoning and tool calls, then refused the next request
because nodwyn sent the reasoning back as `reasoning_content`. Sharing the
Chat Completions shape does not establish which extensions a server reads.
The `Provider` interface and canonical `llm.Request` remain the boundary;
Ollama keeps its native adapter. Chat Completions shares transport and wire
types, with a typed `llm.ChatDialect` choosing history encoding separately
from response decoding. Both response aliases are still read.

The user chooses `dialect = "cerebras"` alongside `provider = "openai"` in
`[servers.NAME]`, or directly on a model that names no server. The setting
comes from the user's existing config.toml, never a repository file. It
changes neither destinations nor authorisation: credential injection still
checks the configured host's binding on each request. URLs and model names
never infer a dialect, because either may name a proxy or custom deployment.

The Cerebras dialect sends thinking as `reasoning`; empty or `compatible`
keeps `reasoning_content` so existing configurations keep their behaviour.
Unknown dialects and dialects on Ollama are rejected. A model naming a
server inherits its dialect and cannot set a second one. Selecting a model
by server, named registry entry or control uses the same choice. Saving
Settings retains it, including converting an inline control to a server at
the same endpoint. Settings reports the dialect; configuring it is through
the file in this slice, with no new console selector.

Only the dialect boundary and reasoning mapping are built here. Output-cap
and cache-routing overrides remain as they were. A general model capability
registry, more provider presets, effort controls and arbitrary provider
options are deferred until a concrete incompatibility needs them. There is
no automatic retry with fields removed and no opaque reasoning persistence.

Strict fake servers exercise a tool call, its result and a later user turn,
rejecting `reasoning_content` in Cerebras history. They also check default
compatible encoding, configuration saves and model selection. Replay still
rebuilds the canonical requests; the dialect transforms only the wire.
