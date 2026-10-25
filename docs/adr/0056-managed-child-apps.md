# ADR 56: A managed service for children's static apps

- Date: 2026-10-24
- Status: implemented; deployment and parent enrolment are separate acceptance steps
- Amends: 17, 18, 22, 30, 34, 36, 39, 45

The family laptop launchers pointed to two consoles which had never been
implemented or deployed. Giving those accounts the ordinary executable, or
ADR 55's benchmark sandbox, would grant authority the child environment must
not have. The infra review draft numbered 55 predates the accepted sandbox
ADR; this is its deliberately separate implementation decision.

`cmd/nodwyn-kids` embeds the existing core, with no ordinary daemon/RPC,
scheduler or outbound host links. An operator supplies an absolute profile on
the read-only image. Its policy is compiled into the binary, materialised in
private state and selected explicitly with `Options.InitFile`. Writable project
instructions, project trust, Scheme init and allow files supply no authority.
Ordinary Nodwyn and Raven keep their existing defaults and pairing state.

`Options.Managed` is trusted Go embedder authority. Its immutable invocation
check runs before guard approvals, including nested calls. Only fs-read,
fs-list, fs-write and fs-edit may touch the host, inside a fixed project root.
Metadata, absolute/escaping paths, symlinks, special files and executable file
types are refused. Scratch, mounts, processes, terminals, credentials, MCP,
judges, delegates and model/limit selection cannot broaden that envelope.
The approved provider wraps every model request, including helpers, and checks
its exact local model, context and output limits. Container isolation remains
the OS boundary; this does not claim to contain a compromised Go process.

The HTTP bridge has a distinct managed surface. It reuses existing host-scoped
pairing cookies and WebAuthn, on a private Unix HTTP socket, but exposes no
ordinary RPC or attach route. Each request rechecks its device. Child devices
can only list/create/rename/delete projects, converse and stop. Parent device
invitation/revocation requires an enrolled passkey and fresh touch. Operator
bootstrap issues the first parent invitation privately. Plain invitations
cannot register passkeys. Host links are refused in this distribution.

Projects have host-generated stable IDs and fixed physical roots. Renaming
changes the slug, not the task pin; there is one persistent conversation and
one writer per project across browsers. UI lifecycle actions are validated
host operations, not shell or model commands. Deletion consumes a one-use,
expiring token bound to the exact project/version. Undo versions live in
private state, outside the project. The child view projects user messages and
the final assistant reply, suppressing tools, thinking and compaction notes.
A model may still utter internal words; prompts are not a confidentiality proof.

Every acknowledged mutation preserves immutable content and a versioned,
ordered record in a credential-free export. A durable intent precedes an
atomic file/registry application; the committed record precedes acknowledgement.
A crash rolls the intent forward before another mutation. Missing/corrupt
records or blobs stop recovery; storage exhaustion stops new mutations.
Interrupted turns get recovery boundaries so the publisher can flush history.
Exports contain task/turn IDs and deterministic operation/outcome summaries,
not chat logs or credentials. Per-child service groups can read exports only.

Daily accounting belongs to the child, across projects and restarts, in
America/Los_Angeles. Reserve 32,768 tokens and 120 seconds before a request;
success refunds unused allowance using provider usage and host elapsed time.
Failure or process death retains the full reservation; unknown usage charges
the full token reservation. A backwards clock cannot reset the day. Missing
existing or corrupt accounting fails closed. Operator ceilings are two million
tokens and 60 model minutes daily; 40 calls, 20 actions, 200,000 tokens and
eight minutes per turn. The immutable ceilings can only be lowered at deploy.
There is no fictitious dollar price for local inference.

The infra publisher separately owns private Git and public generations. It
reads only immutable exports, commits every mutation, pushes at turn/recovery
boundaries and validates the pushed snapshot before atomic static activation.
It never receives daemon state; agents never receive Git or publishing keys.
The public path stays VPS to the web host. This replaces the draft's unreviewed
privileged VPS image controller with read-only static generations and no
Docker API, registry or VPS credentials. Private previews are a separate origin
served from committed exports with the existing static CSP. Public activation
requires the real, private family privacy policy and fails closed without it.

The draft's optional inbound parent host links, per-stage UI variations,
conversation-driven project lifecycle and interactive budget adjustment are
not built: this first distribution deliberately has one fixed child policy,
explicit lifecycle buttons and operator-only lower deployment budgets. No
omitted clause grants broader child authority. Parent notifications are local
durable publication receipts and operator logs; no third-party messages are sent.

Acceptance covers fake-model mutation, nested/guessed tool refusals, metadata
and link paths, restart accounting, interrupted-write recovery, raw child HTTP
requests, revocation, cross-origin refusal and actual browser project/chat/
rename/delete actions. A production claim additionally requires isolated
container, model-proxy, laptop UID, HTTPS and public publication checks.
