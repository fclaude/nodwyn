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
