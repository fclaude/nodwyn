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

## Amendment 2026-10-04: effort controls (ADR 41)

The deferral above ended: a model declares the efforts it takes
(`efforts = [...]`, `effort = "..."`), and a conversation chooses one. They are
sent as `reasoning_effort` to a chat-completions server and as `think` to
Ollama; nothing is inferred from a URL or a model name, as for dialects, and a
model that declares none is sent none. The model capability registry, more
provider presets and arbitrary provider options stay deferred.

## Amendment 2026-10-05: OpenAI's Responses API is a dialect

In session `20261005T033050Z-a7ed2b66` a conversation on gpt-6.1-sol,
reached at `api.openai.com` with effort `high`, failed its first request:
OpenAI refused `reasoning_effort` beside function tools on
`/v1/chat/completions` ("To use function tools, use /v1/responses or set
reasoning_effort to 'none'"). Every turn offers tools, so on chat completions
that model takes no effort at all. The same model through OpenRouter had
worked in session `20261005T022031Z-06eaad19`, which asked no effort.

`dialect = "responses"` on an `openai` server sends each request to
`POST /responses` instead, in that API's shape: the conversation as input
items (messages, a reply's `function_call`s after its text, each result as
the `function_call_output` of its call), the tools flat and not strict (strict
wants every parameter required; ADR 29's are not), the effort as
`reasoning.effort`, the cap as `max_output_tokens`, the cache key and the ask
to cache as they are on chat completions. `store` is false: each request
carries the whole conversation, as before, and the server keeps nothing to be
continued from. A reply's reasoning summary, when the server gives one, is
read as its thinking, and its usage names (`input_tokens`, `output_tokens`)
as the chat-completions ones; a reply cut short by the cap ends as `length`.

What is not sent: the thinking a reply came with. The API takes reasoning back
only as the encrypted item it came as, which nodwyn does not keep (ADR 9,
amended 2026-10-02), so each request reasons afresh from the conversation. No
reasoning summary is asked for: OpenAI may refuse one to an organisation it
has not verified, and a request that asks for nothing cannot be refused for
it. A configured `seed` is sent as it is, and the server, which has
no such field, refuses it: nothing a person set is dropped.

Nothing infers the dialect, as before: the person chooses it, in
`config.toml` or in Settings, where the OpenAI preset fills it in and it can
be changed before saving. A server already configured for OpenAI keeps chat
completions until it is changed. The refusal above now ends a turn with advice
naming the Responses API instead of another effort. The canonical request,
its hash and replay are unchanged; the dialect transforms only the wire. A
strict fake Responses server checks the wire, and the chat fake refuses an
effort beside tools on chat completions as OpenAI does, so a conversation
shows the failure on one dialect and the answer on the other.

## Amendment 2026-10-05: Anthropic's Messages API is a provider kind

Settings were built around servers that copy OpenAI's chat completions, and
a person who wants Claude on Anthropic's own API had no provider to choose:
`provider = "anthropic"` was refused. Anthropic's API is not a dialect of
that protocol. It takes its key in `x-api-key`, names a version in every
request, refuses a request without `max_tokens`, asks for an effort, thinking
and a prompt cache in fields of its own, and reports `input_tokens` as only
the prompt after the last cache breakpoint. So it is a third kind, beside
`ollama` and `openai`, with a client of its own (`llm.Anthropic`) and not
Anthropic's OpenAI-compatible endpoint, which has no effort, no thinking and
no cache control.

- **The wire.** `POST {url}/messages` with `anthropic-version: 2023-06-01`
  (the one version the API has) and the key in `x-api-key`. The system messages
  are the top-level `system`; each assistant turn is its text and its
  `tool_use` blocks; each tool result is a `tool_result` block of a user
  message, and a run of user messages and results is one message with the
  results first, which the API requires. An id another provider issued, in a
  conversation that moved between models (ADR 40), is changed to the
  characters Anthropic accepts, the same way in a call and its result; a
  message with nothing to say is left out (the API refuses an empty text
  block); a call whose arguments are not a JSON object is sent with none,
  since `input` must be an object, and the tool reports what the model sent.
  `max_tokens` is the model's reply cap, 16,384 when a request names none.
  Images are base64 `image` blocks. Not streamed, for ADR 25's reason, so a
  very large cap with heavy thinking is one long request: streaming is not built.
- **Effort.** The word is `output_config.effort`, and a request that has one
  also sends `thinking: {"type": "adaptive", "display": "summarized"}`, since on
  some models thinking is off until it is asked for and the summary is what
  nodwyn shows. A model that declares no effort is sent neither (ADR 41).
  Adaptive thinking exists on Claude 4.6 and later; a model before that (Opus
  4.5 takes an effort, but with a token budget) refuses it with a 400, which
  the test and the turn explain (`llm.Hint`, `models.test`): efforts the
  catalogue publishes for Opus 4.5 are therefore offered and refused. A token
  budget is not built; the budget is a number nodwyn has no way to choose.
- **Thinking goes back as nothing.** The API takes a reply's thinking back only
  with its signature, which nodwyn does not keep (ADR 9, amended 2026-10-02:
  the log redacts what looks like a key and would break one). With adaptive
  thinking a turn need not begin with a thinking block, so each request
  reasons afresh from the conversation, as it does on the Responses API. The
  summary of a reply is still its `Thinking` in the log and the window.
- **Usage and done reasons.** The prompt is `input_tokens` plus the tokens
  read from the cache plus those written to it; `cache_read_input_tokens` is
  `cached_tokens` (a reported 0 is not an unknown), `cache_creation_input_tokens`
  the cache write, `output_tokens_details.thinking_tokens` the reasoning. The
  API reports no cost, so one is estimated from the prices; an hour's cache
  write costs twice the input price and is estimated at the published (five
  minute) write price. `max_tokens` is `length`; `tool_use` is `tool_calls`; a
  `refusal` ends the turn with a reply saying the model declined and why.
- **The key's position is the provider's.** `config.KeyPosition` says where
  each kind reads its key, and the key's binding must allow exactly that:
  `header:x-api-key` for Anthropic, a bearer `Authorization` for the others.
  Core and Settings use it for the authorization of a request, the readiness
  of a key, the rows saved with it and the key sent to list models, so a key
  bound as a bearer token is not released to Anthropic and the page says
  *key not allowed*. The rows are `POST` and `GET`, as for any model key.
- **What config.toml takes.** An Anthropic server takes a `credential`;
  its models take `vision`, `cache_control`, `temperature` (0 to 1) and prices,
  and refuse a `dialect`, `cache_key`, `output_field` and `seed`, which the API
  has no field for. A model's context is its entry's, the catalogue's, or the
  `max_input_tokens` of Anthropic's model list, asked with the key.
- **Not verified against Anthropic.** No key was used (AGENTS.md): the wire
  follows Anthropic's documentation and a strict fake (`test/fakes/chat`,
  `/v1/messages`) that refuses a bearer key, a missing version or `max_tokens`,
  an unknown field and an effort without adaptive thinking. One request against
  the real API, with Settings › Models › Test, is the check that remains.

## Amendment 2026-10-05: Responses streams for plan-authenticated inference

ADR 24's OpenAI plan credential uses the public Responses API with
`stream = true` and `store = false`. The Responses dialect now requests a
stream for all credentials. It buffers through `response.completed`,
`response.incomplete` or `response.failed`, emitting whole output only after
success, so ADR 25's redaction boundary is unchanged. A stream that ends
without a terminal response is an error. Compatible servers answering JSON
remain supported. Chat completions still request non-streamed replies.

## Amendment 2026-10-07: the Responses dialect sends the system prompt as instructions

Session `20261007T152356Z-a337f2a9` ran gpt-6.1-sol on a ChatGPT plan
credential at `api.openai.com` with the Responses dialect. Both of its turns
failed on their first request, in milliseconds, with `400 {"detail":"System
messages are not allowed"}`: the request carried the system prompt as an input
item with role `system`, which that server refuses. The refusal is in the
shape of the ChatGPT backend's errors (`detail`), not OpenAI's API errors, and
the advice nodwyn attaches to a 400 (choose another effort) did not apply.

The dialect now sends the system prompt as the request's top-level
`instructions`, which the Responses API has for it, and no `system` message in
`input`. The window builds one system message, first; were there more, their
text is joined in order, so none is dropped. This holds for every credential:
the field is the API's own, and one wire shape is easier to check than one per
credential. Nothing else of the request changed, and the canonical request, its
hash and replay are as before; the dialect transforms only the wire.

The fake Responses servers now refuse a `system` input item in that server's
shape, so a request that carries one fails a test, and accept `instructions`,
counting them as the head of the prompt that a system message was. Not
checked: nothing run here talks to OpenAI (tests use no real key), so that the
plan server accepts the request as sent is not shown, and a field of the request
it refuses next, as it refused this one, would show on the first real turn.
