# ADR 25 — Log-derived ingress redaction

Status: accepted, P7 implementation (2026-09-22)

The context already derives from session log events. `Session.Record` therefore
is the single context ingress; adding a separate mutable context would weaken
replay. Sanitize payloads there, persist through the log writer's mandatory
redaction pass, and retain only the returned event. Forward sanitized typed
payloads to frontends. Emit `redact.event` with source sequence and substitution
metadata. No audit event contains the removed value.

Redaction scans decoded JSON strings, including nested tool arguments and
JSON-encoded tool output, so unicode escapes and PEM newlines cannot evade it.
A collision between redacted object keys fails closed. The writer applies the
same controller after custom filters, including for callers outside core.
Ingress always uses the default known-class detector; egress allowlists must
not permit a credential into context or the log. Entropy-only findings remain
unchanged, consistent with the existing ladder principle; L3 is deferred to P8.

User messages enter the log before the plan gate. The planner's direct provider
request, system text, and tool schemas use the same controller. Tool execution
still receives original arguments and values so guard checks and taint semantics
remain intact; sanitization is applied to the contextual and displayed copies.

A credential can span arbitrary model stream chunks. Buffer each response and
redact the complete content and thinking before emitting display events. This
trades token-by-token display for containment; cancellation still cancels the
provider request. Scheme display fragments are similarly buffered until the
evaluation or turn completes. A future incremental redactor must prove split-prefix and
multiline-key handling before streaming is restored.

Limits: this detects known credential shapes, not every secret or encoding.
Standalone Scheme eval/repl are operator tools, outside the conversational
boundary. Existing historical logs are not rewritten. The stable binary and
bootstrap operator-rule retirement require a separate successful promotion.

## Amendment 2026-10-05: editing what the model cannot read

The model cannot read a credential-shaped text, a fake one in a test fixture
included, and so could not match a line holding one with `fs-edit`: a chat
failed thirty times to change one. ADR 43 has `fs-edit` match in the text the
model reads and keep the credential a placeholder stands for. Redaction itself
is unchanged: ingress still redacts everything, with the default scanner, and
the log still holds no credential-shaped text.

## Amendment 2026-10-05: a credential inside a token run

L1 took each run of token characters (letters, digits and the separators
`- _ . / +`) as one candidate and matched each known shape against the whole
run. A full stop ending a sentence, a path's slash or a `+` joined a key to
its neighbours, the run matched no shape, and the key was neither redacted
nor checked: a probe found `The bucket key is AKIAABCDEFGHIJKLMNOP.` and
`The token is ghp_….` unchanged, and the same text in an `fs-write` asked
nothing. Every place L1 runs was affected, since all of them use this
scanner: the log, what is sent to models and the judge, and the guard's
checks of payloads, command lines and writes.

L1 now looks for a known shape inside a run. A credential of a class begins
at the run's start or after a separator its shape cannot hold, takes the
longest text the shape allows, and may leave only separators (a full stop, a
closing dash) before the next such separator or the run's end. A separator
the shape can hold is no boundary for it: the `/` of a path ends an OpenAI
key, but the `-` of a base64url blob does not make `-sk-…` inside it one,
and the `.` of a dotted name is no JWT boundary. A run that matches a shape
whole is the same finding as before; a run with no known shape in it is
judged whole for the entropy residual, as before; an allowlisted value is
compared with the credential, not the run. No shape, class, threshold or
allowlist rule changed, and ingress still uses the default scanner.

What it costs: a shape that already matched loose text whole now matches it
as a path component too, so `docs/sk-` followed by twenty letters, digits,
dashes or underscores is an OpenAI key there as it was standing alone. Paths,
version strings, base64 and base64url blobs, Go symbols and dotted test names
are no new findings (`TestSeparatorsMakeNoKeys`, `TestDottedNamesAreNotSecrets`).
The Phase 4 benchmark is unchanged (about 0.09 ms for 64 KB); 64 KB of paths
or base64 scans as fast as before, and a 64 KB run with a separator every
other byte takes 1.2 ms instead of 0.9 ms. Each rule names the separators its
shape holds, and `TestJoinsAreWhatTheShapesHold` checks them against the
regex. Still missed: a key joined to a letter or digit (`AKIA…x`), or after
a separator its own shape holds (`my_ghp_…`). An Anthropic key's shape holds
`.`, so a full stop after one goes into its placeholder.

## Amendment 2026-10-05: findings are in order, and the guard judges all of them

`Scanner.Scan` listed every PEM block first and the token findings after, so a
credential written ahead of a private key came out behind it, and both
redaction loops (`Scanner.Redact`, `redact.Redact`), which slice the text from
the end of the last finding to the start of the next, sliced backwards and
panicked. A `.env` holding an AWS key and then a private key was enough to
crash whatever redacted it: the log writer, ingress, the Jev send path and
`fs-edit`'s view (ADR 43). A token run can also lie inside a block (a PGP
key's `Comment:` line) or cross its start, since a dash is a token character
("sk-ant-api03-…-----BEGIN"), and those findings overlapped the block's.

`Scan` now returns the findings in order of appearance and never overlapping,
which its doc comment always said. A block keeps its class and takes in the
tokens that overlap it: its span grows to cover a known token that crosses its
edge, so redaction leaves none of that credential behind. An unknown-entropy
token that overlaps a block is dropped, since nothing redacts or decides on the
residual and growing a block over it would redact text no known class claims.
Nothing is loosened: a block is a finding wherever it was one before, only its
span can be wider.

The guard's `TextSecret` took the first known finding and judged its class
alone. Blocks used to be first, so a private key was always the class judged;
in order of appearance the first is whatever the model wrote first, and a key
bound for the host would carry a private key after it. The same hole was
already there between two token classes (a bound Anthropic key ahead of an
unbound GitHub token). The guard now judges every known finding of an egress
payload, contained or not, and of text that `Outgoing` is asked about: all of
them must be bound for the destination, and the first that is not is the one
the refusal names. The command-line check and the in-repository write confirm
act on any known finding and are unchanged.

## Amendment 2026-10-16: a failed keyed send names no query

Redaction recognises a secret by its shape, and a key bound at a `query:`
position need not have one. When such a request failed to send, Go's
`*url.Error` named the whole address, `?key=` and the key included, and that
error became `http-request`'s error, logged and shown to the model; an MCP
server bound at a query position did the same. `keychain.SendError` now
reduces the address in a send error to its scheme, host and path, and both
keyed senders (`Session.Do` and the MCP HTTP transport) return their errors
through it. A reply body that repeats the query is outside this: the binding
chose a server that sees the key.
