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

## Amendment: a server holds a provider's address and key (2026-10-03)

A model's table carried the provider, the URL and the key's class along
with the model's id, so the models one provider offers repeated those three
in every table, and a new key or address meant finding each of them. In the
app, choosing another model on the same server meant typing its address
again and its id from memory.

- `config.toml` gains `[servers.NAME]`: `provider`, `url` and `credential`.
  A model, `[control]` or `[models.NAME]`, names one with `server = "NAME"`
  and takes those three from it; one that also sets any of them, or names a
  server with no table, is refused. Loading copies the three into the
  model, so the loop, the egress check, the log and replay read a model as
  they did, and a request records where it went. A model may still give the
  three itself.
- A server is a destination like a model: only the user configures one, in
  their own config. Its key is injected only where it is bound, checked on
  every request, as a model's was.
- The app saves servers. `settings.control.write` takes a configured
  server's name with the model, or a provider and an address: then the main
  model names the server `config.toml` has at that address (read as the
  file is now, so a server added by hand is found and never written over),
  or a new one named after its host. A key given with it becomes the
  server's credential, so every model on the server sends it.
  `models.offered` lists the models a configured server offers, asked
  without a key as `models.status` asks, so the console suggests the
  model's id; a server that lists its models only to a key says so, and the
  id is typed. (Since ADR 41, amendment 2026-10-05, it is asked with the
  server's key, which is bound for `GET` as well as `POST`.)
- Not built: adding and removing `[models.NAME]` tables and servers from the
  app. They are written in `config.toml` by hand.

## Amendment 2026-10-09: labels, and the task's model chooses its helper

A sub-agent always asked the control model, and nothing told a strategy or
the model which configured models suit which work. Names are the user's to
change, so choosing by name does not last.

- A model, `[control]` or `[models.NAME]`, may carry `labels = [...]`:
  lower-case words (`[a-z0-9][a-z0-9_-]*`, no repeats). They are found as
  efforts are (`Config.LabelsOf`): a model's own, else those of another entry
  for the same model. `[control]` chosen from the list keeps no copy
  (`Model.Choice`), since a copy would go stale when the entry's labels are
  edited and win over them: the default goes by its entry's.
  `(model-info)` reports them and `(model-names label)` lists the models that
  carry one, so user code routes by label. Settings › Models edits them
  (`settings.model.write` `labels`), and a label edited reaches the tasks
  already open (`Session.SetLabels`): labels change what a model is called by,
  not where a request goes or which tools are offered, so the prompt cache
  and the frozen tool list (ADR 9) are untouched.
- `(sub-agent system task tools [name [options]])` takes
  `'((model . "NAME"))` and asks that model, wrap-up included. An error on
  the way folds the sub-agent's context before it is raised again,
  unchanged, so a caller that catches it goes on in its own context.
- **Exposure is per task, and the default is none.** A task opens with the
  `[models.NAME]` its model may hand work to (`session.open` `delegates`,
  `core.Options.Delegates`, recorded as `session.delegates` and restored on
  resume for the models `config.toml` still has). With none, the `delegate`
  tool does not exist and the task works with its own model alone. With some,
  `delegate` is offered: its description is fixed with the tool list, so it
  lists nothing; a call with an empty task answers with the exposed models, their
  context lengths and their labels as they are then, which is how an edited
  label shows mid-task. The model chooses by labels. A name outside the exposed
  set is refused whatever the model asks for. The helper is offered every
  tool but `delegate` (`(tool-names)`, hidden), so it cannot hand the work
  on. A helper that fails, its provider down, out of quota or refusing the
  request, answers the call with its error: a helper is on another provider,
  and its failure should not end the task's turn. `(delegate-models)` (hidden)
  gives user code the same set. An earlier draft gated eligibility on a
  `subagent` label; per-task consent replaced it, since a label says what a
  model is for, not that every task may send it work.
- Who chooses: the person, on this computer. Labels are written only in the
  user's `config.toml` and Settings, never by a project's files, and
  `delegates` is refused from a paired device as a model choice is (ADR 30).
  Exposing a model is the consent that the task, and what its helper reads on
  the way, may reach that model's provider, as configuring one already was for
  a strategy. The console says so where the choice is made.
- Not built: changing a task's exposure after it opened (the tool list is
  fixed for the session, so a task that opened with none cannot gain the tool;
  one that opened with some could change the set, which nothing asks for yet),
  offering the default model in the console as a helper of a task on another
  model (the session takes its `[models.NAME]`, but the console's list shows
  the default as `control`, which in a task names the task's own model), the
  TUI, line mode and `nodwyn run` choosing delegates, and descriptions beyond
  labels.
