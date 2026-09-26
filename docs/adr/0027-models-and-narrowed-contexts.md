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
them `nibb replay --window` proves every request whichever model it went to.

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
- `[control]` is still Ollama-only, so a user with only a keyed provider
  cannot yet make it the default model. Making the control model an entry
  of the same registry is the next step.
