# ADR 0041: Providers, models and efforts are chosen in the console

- Status: accepted; implemented 2026-10-04
- Date: 2026-10-04
- Amends: 8 (effort controls, deferred there), 40 (the picker it left unbuilt; a switch while a turn runs; the fallback for a model that is gone)

## Context
ADR 40 made a conversation's model a choice and built the wire, the log and
the session, not a picker. The console still had one form, "Change the main
model": one server, one model, no effort, no way to see which providers could
be used and which could not, and a dialect that only the file could set. A
person could not choose how hard a reasoning model thinks at all: ADR 8 had
deferred effort controls "until a concrete incompatibility needs them", and a
provider that refuses a field it does not know (OpenAI refuses
`reasoning_effort` for a model that is not a reasoning model) is that
incompatibility.

## Decision
- **An effort is a word config.toml declares for a model.** `efforts = ["low",
  "medium", "high"]` in a `[models.NAME]` (or `[control]`) lists the words the
  model's provider accepts, and `effort = "medium"` is the one a conversation
  starts with when none is chosen. Nothing is inferred from a model's name or
  a server's address (ADR 8 says why for dialects, and it holds here): a model
  that declares none takes no effort setting, is offered none and is sent
  none. The word is passed on as it is: `reasoning_effort` to a
  chat-completions server, `think` to Ollama (`off` and `on` are its booleans,
  any other word the level a model such as gpt-oss takes). `[control]` takes
  its efforts from the entry for the same model at the same address
  (`Config.Efforts`), so a model's efforts are listed once, and a copy cannot
  go stale beside its entry. `llm.Request.Effort` is omitted when empty, so a
  request without one hashes as it did; `llm.req` records the effort it asked
  for and replay rebuilds it, so every request still verifies.
- **A conversation's selection is a model and an effort.** `session.open` and
  `session.model.set` take `effort` beside `name`, or `server` and `model`.
  Empty is the model's own default effort. An effort the model does not take is
  refused with the words it takes, never dropped: a person who asked for one is
  not answered with another. The choice is in `session.model` (`effort`), a
  conversation opened asking an effort records it, and resuming restores it.
  A change of effort alone sizes nothing again and checks nothing.
- **A choice made while a response is running waits for it** (amends ADR 40,
  which refused it). The turn under way has asked its model, so the choice is
  queued: recorded at once as `session.model` with `queued` and no window
  recipe, reported as `queued` in the state, and applied when the turn ends,
  under the lock that ends it, so no turn can start on the model it replaces.
  A later choice replaces an earlier one that has not applied. If it cannot
  apply (the conversation outgrew the target's context, config.toml no longer
  offers it) the conversation keeps what it had, and the state (`notice`) and
  the log (a note, then a `session.model` that says again what the next turn
  asks) say why. A task closed meanwhile has the choice in its log, and
  resuming applies it. An attachment being added, and a run, still refuse.
- **A model that cannot be had is never replaced by another** (amends ADR 40,
  which fell back to `[control]`). A conversation whose recorded choice can no
  longer be resolved (its model left the list, or the model no longer takes
  its effort) opens, reports `unavailable` with what it was left on, and asks
  no turn until a model is chosen for it. It would otherwise send a
  conversation to a provider nobody chose for it.
- **Providers are set up in Settings.** A provider is a `[servers.NAME]`:
  `settings.provider.write` adds one or changes its address, kind (Ollama, or
  an OpenAI-compatible server) and dialect, which the person chooses and
  nothing infers, and its key; `settings.provider.remove` removes one no model
  is on. A key goes to the keychain with one binding row, as
  `settings.control.write` always did it (ADR 5, ADR 6): replaced in place when
  the provider has one, over https unless the server is on this computer, never
  in config.toml, in an answer or in the page. A key follows a change of path
  or port and never a change of host: a new host gets none until one is typed
  for it.
- **Readiness says what to fix, and a test can prove it.** `models.status`
  reports each model as `ready`, `missing`, `unreachable`, `unconfigured`,
  `needs_key`, `locked` or `unbound`, with the sentence that names the next
  step; the key's state is read from the keychain and the binding table
  before the server is asked, since that is what is to be fixed, and
  `fresh` asks again at once. `models.test` sends one short request to a model
  with its effort, through the same binding check a task's request passes, and
  says what came back: `ok`, `rejected` (the key), `no_model`, `refused` (an
  effort the provider does not accept, say), `limited`, `unreachable`. It sends
  no task content. A request that fails in a task carries the same advice
  (`llm.Hint`), and a refusal sending again cannot fix (400, 401, 403, 404,
  422) is not retried: the turn says so at once instead of after six seconds.
- **The models a person chooses between are the list.** `[control]` and the
  `[models.NAME]` tables, shown as `choices` with the efforts each takes.
  `settings.model.write` and `.remove` change an entry (its provider, id,
  efforts and starting effort; its other settings stay while it is the same
  model), `settings.control.write` takes `name` and `effort` to make an entry
  the default, and choosing another default keeps the one it replaces in the
  list. The same model listed twice is shown once, as the default.
- **The pickers.** The New task window and a conversation share one picker:
  a model select grouped by provider, with the readiness of each in its
  label, and an effort select of the efforts that model takes, or "Not
  configurable". Moving to a model keeps the effort if the new model takes it,
  else takes the new model's own default. A selection that cannot be had is
  shown as it is with why, not replaced. New task: it starts on the default
  model and effort of Settings; a choice made there is kept, in this tab and
  across a reload, until the task starts or the default is picked again, and
  never changes the default; a choice left alone sends nothing, as a task
  always started. A conversation: changing either select is a
  `session.model.set`; the lines under it say that a change takes effect from
  the next message, and, while a response runs, that the response keeps what it
  has and the choice waits.
- **It is this computer's act** (ADR 30, ADR 40). The provider and model
  writes, `models.test`, and a model or effort at `session.open` or
  `session.model.set` are refused to a paired device, which sees the model a
  task has and the default new tasks start on, and cannot change either.

## Consequences
- A model's efforts are what the person declared. Settings offers shortcuts
  (low · medium · high, minimal added, off · on for Ollama) and any other
  words; it does not read what a provider reports (Ollama's `thinking`
  capability, OpenRouter's `supported_parameters`), so the list can say less
  than the provider allows. Reading them is a later, additive change.
- A model whose own default effort is a word cannot be left to its provider
  from a picker; clearing the default in Settings does that.
- Changing the effort changes the request body, not the messages' prefix
  (ADR 9); a provider that keys its prompt cache on reasoning settings starts
  it again.
- A model that gives its own address in config.toml, as every model did before
  servers, is listed and used, and its provider is shown read-only: its
  efforts are set in the file, and saving a default model on a provider moves
  it to one.
- Not built: a picker in the TUI or line mode, more than one choice queued, a
  per-request effort for `(model-turn "NAME")` beyond the entry's own, a
  provider catalogue beyond the presets Settings fills a form with, and
  remembering the New task window's choice across an app restart (the window's
  console is on a new loopback port each launch, ADR 30, so the browser
  remembers nothing; new tasks start on the default).
