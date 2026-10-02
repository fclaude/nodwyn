# ADR 0027: Configured models and narrowed contexts

- Status: accepted
- Date: 2026-09-26

## Context
ADR 26 made the loop a Scheme procedure, and its addendum set the standard
for the core: the behaviours we want should be writable as user code. One of
them is routing: plan on one model, hand the implementation to the model a
rule picks, and escalate to a stronger one if the work diverges, with at
most a switch or two per task. Two things stopped a user writing that:
`model-turn` could only ask the one control model, and the window was always
Go's view of the whole log, so any model asked got the whole conversation.

## Decision
**Configured models.** `config.toml` gains a registry, `[models.NAME]`:
provider (`ollama`, or `openai` for any chat-completions server), URL, model
id, context length and, optionally, the keychain class of an API key.
`(model-turn "NAME")` asks that model; `(model-turn)` still asks the control
model. Its tool calls run like any model's: the core saw the model make
them. Only the user configures models, in their own config: a model is a
destination, so neither a project nor the model can add one.

A keyed model's key is injected per request, and only if the class is bound
to the model's host for the method at `header:Authorization:Bearer`. The
check runs on every request, so a binding change applies at once, and it is
logged as `egress.check`. The key never enters Scheme, the provider or the
log.

**Narrowed contexts**, after Emacs's narrowing. `(narrow-context system)`
records `context.narrow`: from there until `(widen)` (`context.widen`) or the
user's next message, the window is only what follows the narrowing, under
the strategy's system prompt. The strategy hands over the task with
`add-message`. After widening, models see the whole conversation again,
including what happened while it was narrowed.

**Replay.** Narrowing is a log event, so window recipe v4 rebuilds it. An
`llm.req` sent to a configured model records the model's name and its
context length and keep-alive, which differ from the session's recipe; with
them `nodwyn replay --window` proves every request whichever model it went to.

**The adapter** for `openai` is not streamed: output is shown only once
whole anyway (ADR 25).

## Consequences
- Routing is user code (`internal/core/userland_test.go`,
  `TestUserlandRouteImplementation`): policy, the number of switches and
  what each model sees are the strategy's, and the core adds no routing
  policy of its own.
- Configuring a model is consenting that what a strategy sends it leaves the
  machine. ADR 19 said the only content leaving in v0 was Jev's; that is no
  longer so once a remote model is configured. Narrowing is what keeps that
  to a task and a plan.
- `[control]` was still Ollama-only, so a user with only a keyed provider
  could not make it the default model. Amended below.

## Amendment: folded contexts and sub-agents

A narrowed context could only end by widening, which shows the model
everything that happened inside it, and could not open while the model's
calls were unanswered. So a sub-agent's work leaked into its caller's
context, and a tool the model calls could not be answered by one.

- Narrowed contexts nest. `(fold-context)` ends the innermost one and folds
  it away: later windows skip everything since its `narrow-context`, and see
  the enclosing context again. The log keeps the span, logged as
  `context.fold`; window recipe v5 rebuilds it (a log without the event
  builds as under v4).
- `narrow-context` no longer waits for the latest model-turn's calls to be
  answered: they wait for the context to fold, and the context's own
  model-turns start with none pending. `(answer-call call text)` answers one
  of them with the strategy's text, counted against the tool-call budget.
  `widen` is refused while a context holds calls waiting for an answer.
- A turn that ends with such a context open folds it and answers its
  waiting calls as not executed, so a call is always followed by its result
  in every later window.

A sub-agent (`sub-agent`) and a tool the model calls that a sub-agent
answers (`define-agent-tool`, dispatched by `run-calls`) are user code over
these primitives, in `internal/loop/agents.scm` (docs/scheme.md §9.3).

## Amendment: the control model is a configured model (2026-09-27)

`[control]` has the registry's shape and is built by the same code: it may
be a keyed chat-completions server whose key is bound, and it is the entry
`control`, which `(model-turn)` asks; no `[models.NAME]` may take that
name, and the loop has no special case for it. There is no built-in
default: the default had been one user's own server, unreachable for
anyone else. A session opens without `[control]`, so logs can still be
replayed and Scheme evaluated, and its first model request fails with the
table to add. That failure, like a key that is missing or not bound, is a
permanent error the request retry does not repeat.

## Amendment: the planner and the control model — 2026-09-27

The plan gate is user code (`plan.scm`) and asks the control model like any
other model turn, in a narrowed context that folds away, offered no tools;
its requests are recorded and replay (window-v7 for the tool-less request).
`[control]` is a configured model like `[models.NAME]`, registered as
`"control"`, with no built-in default.

## Amendment: lasting narrowings and compaction on request (2026-09-30)

A narrowing ended at the user's next message, and compaction (ADR 32) is a
narrowing, so a compaction lasted only for the rest of its turn: the next
message brought the whole conversation back, trimmed at the high water, and
the handover note was thrown away. In a long interactive session the note
was written again each time the conversation grew past the threshold, and a
user who asked for a compaction between turns would have had it undone by
their very next message.

- `narrow-context` takes an options alist after the agent name;
  `'((lasting . #t))` makes a narrowing outlast its turn. If it is still
  open at the user's next message, the conversation starts there from then
  on, under the session's system prompt with the new task and plan. A fold
  or widen in its own turn ends it like any other. It is recorded as
  `context.narrow` `lasting`, and window recipe v11 rebuilds it; a log
  without the field builds as before.
- Compaction narrows lasting, labelled `compaction`, so later turns go on
  from the note. The log keeps what came before, for replay, audit and the
  optimizer.
- The user compacts whenever they like with a `/compact` message, optionally
  followed by what the note must keep. Between turns it hands over the
  user's latest request before it, read with a new primitive,
  `(session-requests)`, and ends the turn without asking the model to go on.
  A strategy asks for a compaction at its next round with
  `(compact-next-round)`. Both are user code in `compact.scm`
  (docs/scheme.md §9.2).

## Amendment: models that take images (2026-10-01)

An attached image (ADR 22, amended 2026-10-01) is sent only to a model that
takes images: ollama says so (`/api/show` lists `vision`), and a
chat-completions model says so in its table, `vision = true`, since those
servers do not. It is attached only if the control model takes images, and
it goes only to the host its confirm named, the control model's: a request
to any other configured model, at another host, carries a line saying the
image is not for it. Routing is still user code; the image's consent is not
stretched by it. A narrowed context, the planner's or a sub-agent's, starts
after the attachment and so carries no image.
