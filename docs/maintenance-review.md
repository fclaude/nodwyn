# Preserving nodwyn while making it easier to read and smaller

Review date: 2026-10-06. This is a proposed maintenance programme, not an
implemented redesign. The accompanying change addresses the six reproduced
security and identity findings; it deliberately retains the Go host, Scheme
behaviour, event log, protocol, frontends and keychain format.

The strongest direction is to preserve the architecture and reduce the number of
places a developer must understand to change one behaviour. Moving Scheme into Go,
replacing the interpreter, adopting a web framework or splitting the daemon into
services would all enlarge the compatibility problem before demonstrating a size
benefit. Begin with ownership, repeated boundary operations and duplicate reference
material. Treat line reduction as a measured result, not a quota.

## What should stay fixed

- The Go host owns authority: guard refusals, bindings, secret injection, protected
  configuration, budget enforcement and user-only controls. Scheme remains the
  replaceable strategy layer. A new abstraction must not turn a refusal into an
  exception that a strategy can waive.
- Preserve the Scheme language, existing primitive names/metadata, origin rules,
  tool schemas and registration/freeze/reload behaviour. Keep explicit embedder
  authority separate from repository-supplied code and policy.
- Preserve conversation log records, ordering requirements, redaction before
  persistence, replay recipes, resumption and the rule against automatically
  repeating interrupted tool calls. Logs are the conversation history; avoid a
  second mutable conversation database.
- Preserve CLI commands, RPC wire shapes, local/remote method permissions, frontend
  behaviour, user configuration defaults and the intentional exception for remote
  HTTP credentials. Keep the current keychain wire format and existing credential
  restrictions, including ChatGPT plan sinks.
- Keep fake-provider, exfiltration and browser coverage. Preserve old configurations,
  notes, sessions and kept versions with fixture-based compatibility tests. The new
  project-trust requirement is an intentional security change, not a precedent for
  silently breaking future compatibility.

## Where the reading cost is concentrated

These are rounded working-tree counts of immediate production Go, Scheme and JS
files, excluding `_test.go`, generated PDFs and nested directories. They locate
reading effort; they are not complexity scores or promised savings.

| Area | Approximate lines | Main reading obstacle |
|---|---:|---|
| `internal/core` | 6,500 | Session construction, lifecycle, model state, approvals and external integrations share one owning object. |
| `internal/loop` | 5,100 | A 1,263-line `prims.go` and multiple Scheme wrappers require knowing load order and when names are read. |
| `internal/frontend/rpc` | 6,100 | Providers, credentials and settings repeat parts of request authorisation and persistence orchestration. |
| `internal/frontend/web/assets` JavaScript | 8,600 | `task.js` is 1,963 lines; task state, live events and several panels meet there. |
| `internal/scheme` | 4,100 | Language semantics are specialised, but have a coherent boundary and direct tests. |
| `internal/prim` | 5,100 | Several primitives combine argument decoding, path policy, effects and result conversion. |
| `internal/keychain` | 1,800 | Storage, OAuth state and final sinks need careful error/publication reasoning. |

The largest file is not automatically the best extraction target. Splitting it
into arbitrary chunks reduces a file count on screen while adding navigation.
The current source catalogue is useful for coverage, but should supplement a
short reading path rather than become the first thing a contributor reads.

## Conservative programme

### 1. Make the existing path through the system explicit

Start with a short developer entry point linked from README. Trace exactly four
operations using the current functions: open/resume a session; send a message and
run a tool; send a credential-bearing request; save user-owned policy. State the
owner of each piece of mutable state and where its authority came from. Extend
the existing maintainer map instead of starting another parallel architecture guide.

For each operation, identify input, last reversible point, side effect, durable
record and failure result. For example, a keychain rename followed by a directory
sync failure has a published file and uncertain durability. That cannot share the
same rollback rule as a failure before rename. These concrete distinctions teach
more than a generic “storage layer” diagram.

Make a reading order for the Scheme files: definitions loaded once, wrappers that
capture earlier definitions, settings read at call time, and values frozen into a
session. Preserve the wrappers initially; document their dependencies and use
existing strategy tests to show why load order matters.

**Expected effect:** much lower entry cost, very little code reduction. This is the
first slice to build and review before choosing a broader reorganisation.

### 2. Consolidate repeated boundary mechanics, with named owners

There are related credential request paths in `core/credentials.go`,
`core/mcp.go` and `frontend/rpc/providers.go`. They differ in logging, refresh,
caller context and how the binding table stays current. Do not replace them with a
single helper that silently drops those differences. First write a table of their
contracts. Then extract only the shared authorise/refresh/inject sequence with
explicit policy and recording inputs; let each caller retain its lifecycle.

A possible call shape, for discussion rather than a committed API, is:

```go
err := credentials.Authorize(request, Use{
    Class: class,
    Position: position,
    Transport: transportPolicy,
}, policySnapshot, recorder)
```

The benefit would be one place to verify that a binding is checked before refresh,
that transport is checked before a key is inserted, and that an observer cannot
see credential values. Keep “no session recorder” explicit for setup probes. Reject
this extraction if it needs a large interface family or more glue than it removes.
Retain separate tests for session requests, setup/model lists and MCP inspection.

Similarly, `policyfile.Update` and keychain publication both need exclusive temp
creation, modes, sync and rename. A small internal durable-file writer may remove
mechanical duplication, but must return whether publication occurred. Advisory
locking, rereading, encryption, credential refresh and rollback remain with their
current owners. Do not merge policy and secret storage into one general repository.

**Expected effect:** a modest net reduction and fewer drift-prone security paths.
Count the removed code after each candidate extraction; the exact savings are not
known until both versions are compared.

### 3. Give console state a clear lifetime

Separate `TaskView`'s conversation state and event application from its independent
panels, one panel at a time: review/versions, notes, credentials/policy and task
controls. Pass the state and actions a panel actually uses. Avoid a service locator,
a global event bus or a universal component base class.

Keep `h()`'s text-only rendering and the no-build ES-module delivery. The hard cases
are focus during redraw, unsaved drafts, changing tasks, reconnection, pending
approvals, stop/resume and setting changes reflected on another page. Preserve the
browser checks that perform actions and inspect requests/results; a selector-only
test is not evidence of preserved behaviour.

For reusable settings forms, extract repeated field/save/error mechanics only
where the semantics match. The model picker, transport exception, destructive key
operation and live “Don't ask” revocation do not all have the same application
rules. Keep those rules visible at the call site.

**Expected effect:** smaller individual files and less accidental shared state;
net JavaScript size may stay level in the first extraction. Measure import hops and
ownership clarity alongside lines.

### 4. Separate primitive definitions by capability, not by arbitrary file size

`loop/prims.go` is a good candidate for a few capability-specific registration
functions: model/window operations, history/plan operations and sub-agent/context
operations. Keep primitive metadata beside its implementation. Use an ordinary
explicit registration list so a reader can still see what exists and in what order.
Do not generate handlers or add a plugin framework for built-in primitives.

For the host primitives, extract repeated argument decoding only when several
handlers genuinely use the same contract. Keep path checks, effectful operations
and error handling legible. Avoid a general “operation” abstraction that hides
which file, process or network boundary is crossed.

**Expected effect:** easier browsing, little immediate line reduction. Approve a
split only if it reduces the concepts needed for a common change.

### 5. Reduce documentation duplication while keeping all reading levels

The manual, Scheme reference, book, expert guide and ADRs serve different purposes;
retain them. Give each fact one maintained reference location: current user
behaviour in the manual/security reference, primitive contracts in the Scheme
reference/schema, historical rationale in ADRs, explanation and worked examples
in the books. When code and prose disagree, inspect and run the code, correct the
current reference and mark a historical ADR as superseded rather than pretending
the earlier decision never existed.

Generate repetitive reference tables from the existing metadata/schema where that
metadata is complete. Keep narrative examples handwritten and executable with
fakes. Use shared TeX inclusions for identical primitive tables or limits instead
of copying paragraphs between books. Keep generated source listings as a separate
reference appendix or volume; tutorials should lead a developer through a small
working slice before presenting the complete source.

This is likely the largest safe reduction in maintained text. It reduces repeated
maintenance, not the need for useful explanations. Do not replace the beginner
material with terse API listings or remove the source catalogue as a shortcut.

## A broader redesign, only if the conservative work proves insufficient

A later option is to divide `core.Session` into a conversation runtime and a host
services owner, with the runtime receiving a few explicit capabilities. That could
make ownership more visible and reduce repeated frontend setup code. It could also
add forwarding methods, lifecycle synchronisation and hidden dependencies. Require
a concrete before/after slice, a dependency map and an accounting of deleted versus
added code before selecting it. Keep the current entry points as adapters during
migration; do not combine this with a log, language or protocol migration.

Another option is replacing chains of Scheme wrappers with a named phase pipeline.
That changes how extensions compose, including which earlier definitions they
capture. It is substantially riskier than splitting registration files. A prototype
would need to run existing custom strategies unchanged through a compatibility
adapter and reproduce the same fake-provider requests and guarded effects. There
is no present evidence that this redesign is needed, and it should not be bundled
with the security fixes.

## Acceptance and stop rules

For each slice, state the behaviour being preserved, the owner of the state,
expected deletions and the paths of the relevant tests/docs. Make one change at a
time. Compare the existing and proposed implementations on representative fake
sessions and recorded fixtures, normalising only intentionally nondeterministic
fields such as timestamps and random IDs. Preserve ordering of approvals, tool
results, redaction and durable events, not just final text.

Run the complete affected packages, exfiltration checks for boundary changes, and
browser actions for console changes. Regenerate the protocol/source inventory and
build both books when relevant. Finish with `make validate`; explain tests that
pass unchanged and tests that cannot compile against the old API. Keep real-model
quality benchmarks separate and run them only when explicitly requested: structural
refactoring should not silently spend money or alter measured working rules.

Track production lines, maintained reference lines, duplicate sequences removed,
files/import hops required for the four reading paths and critical test coverage.
Stop an extraction if it replaces understandable duplication with a larger framework,
adds forwarding layers without removing responsibility, changes a default or weakens
a refusal. Do not promise a percentage reduction in advance.

Recommended order: developer reading path; one credential-authorisation extraction;
one TaskView panel extraction; a primitive registration split; shared generated
reference tables. Review each sample and its measured outcome before committing to
the next. The broader redesign remains a separate choice.

## Correction to the preceding review

`internal/frontend/web/licenses.go` already embeds the websocket and font notices,
and `nodwyn licenses` exposes them. The earlier suggestion that those notices were
absent from standalone binaries was incorrect. This maintenance plan does not assert
Apache-2.0 release readiness or change the project's licence; repository licensing,
provenance and release packaging remain a separate release decision.
