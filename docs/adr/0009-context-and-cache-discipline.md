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
the ask is never trimmed and the prefix changes only at turn boundaries
(replaced from recipe v15 by the amendment of 2026-10-04: the task and its
plan are messages, and the system prompt never changes).
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
(window-v7). A turn may also offer only some tools (`'((tools "fs-read"))`, a
sub-agent's); `llm.req` records their names as `offered_tools` and replay
offers the same subset of the catalog. The window's size budget still counts
the whole catalog, so the window, and the recipe, are unchanged by it. Every request also caps the model's output at
`window.output_reserve` (window-v8: `max_output` in the recipe), so the
prompt and the reply fit `num_ctx`. Recipes v1 to v7 still replay under their
own rules. A strategy may also fold a tool result (`fold-result`): later windows show its text instead, recorded as `result.fold` (window-v9), so what the model no longer needs (a failed attempt, a stale or old read) stops taking room while the log keeps it. A request may be let past the high water, up to the window's ceiling (`(model-turn name '((headroom . #t)))`, compaction's note request); `llm.req` records its trim line as `high_water` and replay builds that request with it (window-v10). The window's water marks grow with the control model's context when config.toml leaves them unset (ADR 32, amendment 2026-09-30), and shrink to fit a context too small for them, set or not, less the model's reply cap (ADR 41, amendment 2026-10-05); the recipe records the sizes the session used, so replay needs no configuration.

## Amendment: attached images in the window — 2026-10-01

An attached image (ADR 22, amended 2026-10-01) is a user message: a line
framing it as data, and the image named by its SHA-256. The log never holds
its pixels; the request's sender reads them from the workspace's attachment
store and checks their size and digest. An image it cannot send as
attached, because its copy is missing or changed, the model takes no
images, or the request goes to another host than the image's confirm named,
is replaced by a line saying which, and `llm.req` records the reasons as
`withheld_images`. The request hash covers the digest, never the bytes, so
replay applies the recorded reasons and verifies the request without
reading a file (window-v12; a log without images builds as before). The
window counts an image as the estimate its record holds (85 tokens and 170
per 512-pixel tile, after the chat-completions scaling), not as bytes/3, and
trims it with its line, oldest attachment first.

## Amendment 2026-10-02: requests name their session

This ADR keeps the prefix byte-stable, but a shared remote server caches per
replica. In a 34-request repository review with meta/muse-spark-1.3 through
OpenRouter, 126,082 of 1,521,085 prompt tokens were read from the cache
(8.3%). Rebuilt with `nodwyn replay --window`, 21 of those requests were
their predecessor's request with messages appended. One of them read the
predecessor's whole prompt from the cache, 14 read none of it, and the
partial hits matched older requests: seq 70 read seq 13's 5,120-token prompt
and none of the three requests between them. That is what replicas with
caches of their own look like. OpenRouter routes the model to one provider,
Meta, so OpenRouter's own provider routing does not explain it.

Each request to a chat-completions server now names its session, so a
server that routes by it can keep the session on one cache. The name is a
SHA-256 digest of the session id, which is the same across a resume and
says nothing else (the id holds the start time). It goes as OpenRouter's
`X-Session-Id` header and as `prompt_cache_key`, OpenAI's field, which
OpenRouter also reads. `cache_key` in a model's entry sends it as the header
only (`header`), for servers that refuse unknown fields, or not at all
(`off`). Both go only on the wire: the provider-neutral request, its hash
and replay are unchanged. Whether the key reaches Meta's replicas is not
documented; the cached share of append-only requests in the next session's
log is the test.

## Amendment 2026-10-02: thinking goes back on its reply

The decision above kept the model's thinking out of the window when the
model was a local Ollama with a 98,304-token context, where every token of
it crowded out a tool result. The control model is now usually a remote one
with a context of a million tokens, and keeping the thinking out has a cost
of its own. In a chat turn about worktrees (session
`20261003T052943Z-4e994237`, meta/muse-spark-1.3 through OpenRouter) every
reply's text was empty, so each of 40 requests began from the tool results
alone: the thinking said what the answer was at the sixth and restated it,
in other words, at each of the next 34 (ADR 32, amended 2026-10-02).

From window recipe v14 an assistant message carries the thinking its
`llm.resp` recorded, redacted as everything in the log is
(`window.Policy.Thinking`). The prefix stays byte-stable: a reply's
thinking is part of it from the first request that shows the reply, and
trimming elides it with the reply, never on its own. It counts toward the
window's estimate. Ollama gets it as `thinking`; a chat-completions server
as `reasoning_content`, the field DeepSeek, vLLM and Qwen's chat templates
read and an alias OpenRouter takes for `reasoning`. A recipe before v14
replays without it, so its requests still match the hashes they recorded.

Only the text goes back. OpenRouter also returns `reasoning_details`, which
for some models carry the reasoning encrypted, and wants them back
unchanged. nodwyn does not keep them: the log is the source of truth and
redacts what looks like a key, and an encrypted blob in base64url looks
like one, so the copy the window would send back could be altered. What
muse-spark reports as thinking is a one-line summary per reply, so for it
this gives back little; a model that reports its whole reasoning gets all
of it. The working rules no longer say the thinking is not kept: they say
it comes back only as the server reported it, often a summary or nothing,
and still ask for a written plan.

The cost is the thinking's tokens on every later request, less what the
provider caches. In the worktree turn the 40 replies reported 6,938
bytes of thinking in all. A server that refuses fields it does not know
would refuse `reasoning_content`, but only after it sent thinking itself,
which such servers (OpenAI's) do not.

## Amendment 2026-10-04: the request and its plan join the conversation

The 2026-09-21 amendment put the user's request, and later its plan, in the
system prompt, and expected one full prefill per user turn. That was cheap
with a 60Ki window on a local server. A window sized for a million-token
context now grows far past that, and the system prompt is the first thing
in the prefix. Every new request and every plan rewrote it, so the server
read the whole conversation again. In session `20261004T063427Z-97214b45`
(z-ai/glm-5.3-flash through OpenRouter, recipe v14), the person's second
message read none of its 120,906 prompt tokens from the cache, and cost
$0.0104. The request after it, 122,613 tokens with 120,896 cached, cost
$0.0027. The request after the model's `plan-write` read none of its 51,433
tokens from the cache either. Rebuilt with `nodwyn replay --window`, both
were their predecessor with messages appended, except message 0: the plan
added to it at seq 145, and at seq 1120 the new request in place of the old
one, with the plan gone. A third miss in that session (seq 943, 100,555
tokens) had a byte-identical prefix: that one is the server's, as in the
amendment of 2026-10-02.

From window recipe v15 (`window.Policy.InConversation`) the system prompt is
the session's alone and never changes:

- Each request is the person's message under `task-header`. Earlier
  requests keep the header they had, so the window reads as it did when
  each was the latest. The request is no longer sent twice, once in the
  system prompt and once as the message.
- A recorded plan (`gate.plan`) is a user message of its own under
  `plan-header`, with a carried plan's marks, where it was recorded.
  `plan-write` records it while its call is being answered, and no message
  may come between a reply's calls and their results. So the plan waits
  until the next message of the context it was recorded in that is not a
  result, or the end of the window: that is the same place in every later
  window. A plan written again in the same batch replaces it before any
  window shows it. A plan recorded in a context that is then folded goes
  with that context.
- Both are user messages, which are never trimmed, as the system prompt
  never was. A turn's plan stays in the conversation after the turn,
  followed by the next request; before, it left with the turn.
- A narrowing's own system prompt still replaces the session's. A plan
  written inside a compacted context now shows; before, the narrowing's
  system prompt hid it.

A new request, or a plan, now adds to the cached prefix instead of starting
it again. An older recipe replays as it was built, with the request and the
plan in the system prompt. Not measured: the working rules (ADR 32) were
benchmarked with the request and plan in the system prompt. Whether a model
follows a plan as well from a message is for the next benchmark run to
show; the cached share of the next session's later turns is the test of
the rest.

## Amendment 2026-10-04: reasoning response fields do not imply history support

The 2026-10-02 amendment assumed a server exposing thinking would accept
`reasoning_content` in its history. Cerebras disproves that: it returns
`reasoning` and rejects `reasoning_content` on the next request. ADR 8's
server dialect now chooses the outgoing field independently of response
parsing. `cerebras` sends `reasoning`; the default `compatible` dialect
keeps `reasoning_content`. Ollama continues to send `thinking`.

The log's thinking, redaction, window recipe and request hashes are
unchanged. Dialect selection changes only the final wire representation;
reasoning is not discarded to recover from a validation error. Encrypted
reasoning remains unsupported for the reasons in the earlier amendment.

## Amendment 2026-10-04: trimming preserves tool-call pairs

In session `20261004T180305Z-f53d94b0` (Cerebras, qwen-3.8-27b), request
668 crossed the window's high water while asking for a compaction handover.
Recipe v15 elided old assistant replies, clearing their tool calls, but
kept their results as `tool` messages. Cerebras rejected the orphaned
results with HTTP 400. The reasoning-field dialect was already correct.

From recipe v16, the window links each result to its calling assistant
batch before applying elision, after narrowing and folding have selected
the conversation. A reused call ID refers to its latest batch. Eliding an
assistant batch clears it as before and omits its matching results from
the request; their seqs join the sticky elided set and their tokens leave
the estimate. A result may still be shortened or elided in place while its
calls remain. If any result is among the four protected recent entries,
the calling batch is also protected. An incomplete batch is not newly
elided. Protected history may prevent reaching the low-water target.

This is shared window behaviour for every provider, with no new setting
or change to credentials, tool execution or the source log. Older recipes
replay with their original shape, including malformed old requests. A
resume records v16 and rebuilds the next request from the original events;
it removes the results of calls an earlier recipe had already elided,
even without a new high-water crossing. The change cannot restore context
already removed by compaction.

The regressions first reproduced the orphan results in both direct window
builds and a strict fake Cerebras server's compaction request. They cover
multi-call batches, result order, repeated IDs across batches, failed
results, recent-result protection, sticky estimates and a v15-to-v16
resume whose old and new requests both replay to their recorded hashes.

## Amendment 2026-10-05: a Claude model caches only when asked

A session on anthropic/claude-sonnet-5.5 through OpenRouter
(20261005T003253Z-6c6e3544, recipe window-v16) read 0 of 807,710 prompt
tokens from the cache over 18 requests, and its replies reported no cache
write either. Every request was billed at the same $2.00 per million input
tokens: fitting cost to tokens leaves no residual. Rebuilt with `nodwyn replay
--window`, each request was its predecessor with messages appended, but for one
hygiene fold, so the prefix was stable. The glm-5.3-flash and qwen sessions
of the same days, on the same route, read 83 to 99% of their prompts from the
cache. What those models share is a server that caches the start of a prompt by
itself; the session key above routes to such a cache and does nothing for one
that is not there. Anthropic's caching is opt-in: a prompt is cached only if
the request carries a `cache_control` marker.

OpenRouter reads it as a top-level `cache_control` on the request, which it
documents as applying the breakpoint to the last cacheable block and moving it
on as the conversation grows, for Anthropic and the providers it routes Claude
to (Bedrock, Vertex, Azure). `"ttl": "1h"` asks for a one-hour lifetime
instead of five minutes. A model's entry now says whether to ask:
`cache_control = "ephemeral"` or `"1h"` (openai only), sent as that field.
Like a dialect it is the person's to set, never inferred from the model's name
or the server's address, and for the same reason: the docs do not say what a
route that does not know the field does with it, and a model that caches by
itself needs nothing. It goes only on the wire, so the provider-neutral
request, its hash and replay are unchanged, and the cached tokens a provider
reports are read as before (`llm.resp` `cached_tokens`). `models.test` sends
the ask as a task would, so a provider that refuses it is found there, with
what to turn off. Settings has it with the model's other tuning.

Estimated, not measured: at Anthropic's usual prices (a read at a tenth of the
input price, a write at 1.25 times) that session would have cost about $0.44
instead of $1.73. Whether the marker reaches a given route as documented is
verified by one request against it: the first reply should report
`cache_write_tokens` and the second `cached_tokens`. The fake provider
(`test/fakes/chat`) caches as such a route does, only when asked, which tests
what a request asks for and not what a provider does with it. Not built: a
breakpoint of its own on the system prompt, which would survive a hygiene fold
that rewrites an earlier message; the docs do not say whether it combines with
the top-level ask.

## Amendment 2026-10-05: Anthropic's own cache, and Baseten's session header

Anthropic's own API (ADR 8, amended 2026-10-05) reads the same top-level
`cache_control` and caches nothing without it, so `cache_control` is a setting
of its models too; `models.test` sends it as a task does. Its usage reports
`input_tokens` as only the prompt after the last breakpoint, so the prompt is
that plus what was read from the cache and written to it, which keeps
`cached_tokens`, the budget's tenth and the context meter as they were. A model
added in Settings on an Anthropic provider starts with the five minute ask
chosen, written to its entry like any setting: without it every request is
billed in full, and nothing infers it from the model's name or later.

Baseten routes requests by its own header, `X-Session-Affinity`, as OpenRouter
does by `X-Session-Id`. The session key goes as both headers whenever it is
sent (`cache_key` `session` or `header`), the model list asked without it. A
server that knows neither ignores them; the key says nothing about the session.

## Amendment 2026-10-05: the result cap follows the window, and a cut names its lines

A tool result larger than `window.max_result_tokens` (3,072 by default) is
shown head and tail. The cap stayed at 3,072 while the window grew with the
model's context to a 654k or 774k high water, so a whole-file read of more
than about 9 KB lost its middle: 496 of the 3,701 reads (13%) in the 75
local session logs of 2026-09-30 to 10-05, and 126 of those were read again
in ranges straight after, the marker saying only "about N tokens omitted;
re-read the file". transcript.js was read 20 times in
20261003T190332Z-547f0bd6 that way.

- A window that grows with the model's context grows the cap with it, to a
  48th of its high water and at most 16,384 tokens (16,128 at a 1M
  context), unless `config.toml` sets `max_result_tokens`
  (`Window.ResultsFixed`); a window that keeps its size keeps 3,072. The
  session's recipe records the cap it ran with, as before.
- A ranged `fs-read` stops within the cap as the window is sized now
  (`prim.Options.RangeBytes`), not as the configuration had it before the
  session sized its window, so the window still never cuts a ranged read.
- Recipe v17 (`Policy.NamedCuts`) cuts a result at line ends and names the
  lines it leaves out: of the file for an `fs-read`, counted from line 1 or
  from a ranged read's `[lines A-B of N]` header, as the fs-read that brings
  them back; of the result for anything else. A result with no line end to
  cut at keeps the old marker, and older recipes replay with it.

## Amendment 2026-10-05: a Claude reply's signed thinking goes back with it

A reply's thinking went back on its reply from recipe v14, as text, which
Anthropic's API does not read: it reads a thinking block only as it gave
it, with its signature, which nodwyn did not keep. In session
20261005T151325Z-d9519026 (claude-sonnet-5-5, $13.77), 446,000 of the
569,000 output tokens were reasoning; in its fourth turn 14 calls in a row
averaged about 19,000 reasoning tokens each, each emitting one read and no
text, the thinking summaries working out the same parseDiff fix again
(seq 4073, 4110, 4117, 4144): that turn cost $9.29.

- An Anthropic reply's thinking and redacted thinking blocks are kept as
  given, signature or data included, with where each came among the reply's
  calls (`llm.ThinkingBlock`, `llm.resp` `blocks`).
- Recipe v18 (`Policy.Blocks`) gives them back with their reply, each
  where it was. The API checks that what came before a block is what it was
  when the block was made, and this window edits what it sent: it folds old
  results and elides old entries. So a reply made before a fold, or an
  elision, of an earlier entry goes back without its blocks; one made after
  it keeps them, since its conversation already had the change. Removing a
  stale run while keeping later blocks made without it keeps the chain the
  API checks.
- A request that carries blocks names `thinking.block_binding`
  `prefix_mismatch_behavior: "drop_block"` (beta
  `thinking-binding-controls-2026-08-01`), so a block the window missed is
  dropped by the API rather than failing the request, and asks for adaptive
  thinking when the effort did not, as the models that make such blocks
  run anyway. That first request may miss the prompt cache once.
- The blocks count toward the window's estimate, their signatures included,
  and go with their reply when it is trimmed.

The window's own staleness rule was written from the API's documented check,
and nothing here was run against Anthropic's API: the tests use recorded
shapes.

## Amendment 2026-10-07: whole-session token cache rate

The console showed cache use per turn, but its whole-session Totals had no
cache figure for comparing sessions. Totals now has a plain **Token cache
rate** row: the rounded percentage of raw prompt tokens read from the
provider's cache across the session's recorded model replies. It sums
`cached_tokens` and `prompt_tokens` before dividing, across turns and
models, rather than averaging request percentages. Only replies reporting
cache reads and a positive prompt count contribute; missing or
`unreported` usage stays out. A reported cache read of zero with positive
prompt tokens gives 0%; without a positive prompt total the rate is
**unknown**, never an invented 0%.

This measures reported prompt-token share, not the fraction of requests
hitting cache, cache writes or the budget's token count (which discounts
cache reads to a tenth). It uses the same retained events as the other
Totals figures, so a truncated log keeps the label **from the log that
remains**. The raw token totals are cached with the existing event
aggregation and refreshed on a new recorded event, not on clock ticks.
The RPC and log formats are unchanged; no usage is inferred for providers
or older logs that do not report it. Browser regressions cover weighted
aggregation, unknown versus zero, live replies, reopening, retained
history and aggregation reuse over a large log, using fake providers only.

## Amendment 2026-10-07: a ChatGPT plan's requests name their session as Codex does

A ChatGPT plan's requests read almost nothing from the cache. Three
sessions with gpt-6.1-sol on a plan's credential (20261007T182910Z-0eb4973f,
20261007T181306Z-8facf414 and 20261007T191149Z-0d89bd79) read 0.9%, 5.6% and
3.6% of their prompt tokens from it; 20261007T183700Z-dc3eb569, the same
model at the same address with an API key, read 94%, each request reading
its predecessor's whole prompt but three tokens. Rebuilt with `nodwyn replay
--window`, every request of 0d89bd79 matched its recorded hash and was its
predecessor with messages appended, under the same system prompt and 29
tools, so the prefix the cache needs was there. The plan's hits were partial
and matched older requests, in multiples of 128 tokens (2,560, 6,784, 24,448
and 56,704): what replicas with caches of their own look like, the shape this
ADR's 2026-10-02 amendment saw at OpenRouter. The plan's replies also took two
to five times as long.

The requests differed only in their credential: both carried the session's
`prompt_cache_key` and headers. Codex, signed in to the same plan, read 95%
of a 257-reply session's prompt tokens from the cache that day, but over its
own endpoint (`chatgpt.com/backend-api/codex`), which a plan's token may not
reach (`checkOpenAISink`). opencode reported the same miss with a plan's
token at `api.openai.com/v1` (3.44%, anomalyco/opencode#53765) and now sends the
session headers Codex sends there, `session-id`, `thread-id` and
`x-client-request-id`, which a maintainer saw raise the hits
(anomalyco/opencode#53767). Codex dropped the underscored `session_id` for
the hyphenated name (anomalyco/opencode#29471).

A request whose credential was granted ChatGPT plan usage
(`chatgpt.tokens.use.direct`, a scope the keychain requires of a plan's
sign-in) now carries the session's cache key as those three headers as well,
whenever it carries the key at all: `cache_key` `session` or `header`, never
`off`. The scope comes from the token response the keychain stored, not from
a file in the repository, and the headers say only what `prompt_cache_key`
already says. An API key's requests, which the server caches, are unchanged,
as are the provider-neutral request, its hash and replay. The test was the
cached share of the next plan session's append-only requests.

The next plan session on this build, 20261007T194139Z-eef2d1d4, read 84.3% of
its 520,881 prompt tokens from the cache. Each of its nine requests after the
first read its predecessor's whole prompt but less than 128 tokens (98% to
99.9% of it), where only one of 0d89bd79's twelve had read any of its
predecessor's. The share is below the per-request figure because a
request adds what the reply and its results brought, which no cache holds
yet. The hits are still multiples of 128 tokens, unlike the API key's, and
the replies still took 17 to 56 seconds, so the headers changed where the
plan's requests go, not how fast its server answers. The reply's encrypted
reasoning (`include: ["reasoning.encrypted_content"]`), which Codex and
opencode also send back, was not needed for the cache; it is not built.
