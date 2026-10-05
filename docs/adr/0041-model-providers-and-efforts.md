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

## Amendment 2026-10-05: a model's settings are its own

Session `20261005T035352Z-5a76f6e4`, gpt-6.1-sol at `api.openai.com`, kept
the window at its default size ("openai: model list: 401 Unauthorized") and
priced none of its 21 replies; session `20261004T182412Z-747045aa`, on
Cerebras, did the same with a 403. Neither can be fixed by asking better:
OpenAI's and Cerebras' model objects are `id`, `object`, `created` and
`owned_by` (OpenAI's also `shutdown_date`), with no context length and no
price, and neither API reports what a request cost. OpenRouter's list says
both, and its replies carry their cost, which is why the same models worked
there. So a model's context and prices are said by the person, as its efforts
are. Settings could say them only for the default, as `[control]`'s own
"Default model tuning": a copy that a model chosen in a picker did not have,
that choosing another default replaced, and that went stale beside the
model's entry. Whether a model takes images was set the same way.

- **A model's settings are its entry's.** `num_ctx`, `max_output`,
  `output_field`, `keep_alive`, `temperature`, `seed`, `vision`, `cache_key`,
  `cache_control` and the `price_*` entries of a `[models.NAME]` are that
  model's. `settings.model.write` takes them as `settings`, all of them (one
  left out is unset; `settings` left out keeps the entry's own), and each
  choice in `settings.read` reports them. Settings › Models edits them in each
  model's form: its context length, its prices and whether it takes images,
  the rest under More settings. The Default model tuning section and the
  `control.*` names in `settings.write` are gone.
- **The default names its entry.** `[control]` keeps which model it is and
  the effort it starts at; each setting it leaves unset comes from the first
  `[models.NAME]` for the same model at the same address (`Config.Tuned`), as
  its efforts do (`Config.Efforts`). What a hand-written `[control]` sets
  itself still stands. Choosing a default by name writes only the choice
  (`Model.Choice`); saving the model that is the default moves what
  `[control]` held to its entry, filling only what the entry leaves unset, and
  leaves `[control]` without; choosing another default fills the old one's
  entry the same way, so nothing set is lost. A model chosen by server and id
  gets its entry's settings too. An entry borrows nothing from another entry
  for the same model: two entries for one model, such as one with a smaller
  context, are two sets of settings.
- **Nothing is inferred from a model's name**, as before: nodwyn has no table
  of context lengths or prices. A conversation on a model whose context
  nobody said keeps the default window, and its note now says to set the
  model's context length in Settings › Models (it named `[control]`, which was
  wrong for any other model), and the note a spend limit leaves for a request
  of unknown cost says the same of prices.

Not built: reading a context length or a price from a catalogue other than
the model's own server's list (OpenRouter's lists OpenAI's models, but asking
it for a model on another server would send a request nobody configured).

## Amendment 2026-10-05: change notices follow a choice

The conversation picker showed that the running response kept its model and
that a change would apply to the next message even when nobody had changed
anything. That hypothetical notice was noise. Timing notices now describe
actual model or effort choices: a queued choice names both the running and
chosen selections, and a choice between messages says when it applies. An
unchanged running response shows no change notice; warnings and errors remain.

## Amendment 2026-10-05: a model's list is asked with its key, and its reply cap comes out of its context

Session `20261005T042752Z-871dfe24`, gpt-6.1-sol at `api.openai.com` after
the amendment that made a model's settings its own, still noted "openai: model list: 401 Unauthorized": the
request for a server's model list carried no key, on the reasoning that the
lists that report a context length are public. OpenAI and Cerebras list their
models only to a key, so the list was refused there, and the console's model
picker offered nothing for them either. The key is the host's to send: it is
injected by Go after the same binding check every request with it has, and a
model's list goes to the host its requests already go to. The reason it was
not sent was the binding: model keys were bound for `POST` alone, so a `GET`
with the key would have been refused.

The same session's settings made the reply cap a puzzle. A model's context
length was up front in its form, its reply cap folded under More settings
with "the window's reserve" for a default, and Settings › Context window had
a "Kept for each reply" of its own; the window grew with a large context but
never shrank, so a 32768-token model kept the 61440-token high water and was
sent requests it could not hold, unless `[window]` was changed for every
model at once, which config validation demanded of `[control]` alone.

- **A model key is bound for its list.** Saving a provider's key binds it for
  `POST` and `GET` at the provider's host, as a bearer header. At start the
  daemon gives each key that config.toml names for a chat-completions server,
  and that the binding table already allows to `POST` to that server's host,
  the `GET` row it lacks; it adds no host, position or credential the table
  did not allow. `llm.OpenAI.Models` asks `/models` through the authorized
  path chat requests take, so the window's context length and
  `models.offered` are asked with the key, and the egress check is recorded
  as any other. A key whose binding refuses `GET` is not sent, and the
  window's note says why. `models.status` still probes without a key: it
  reads nothing from the list and only asks whether the server answers.
- **The reply cap is the model's, and comes out of its context.** A model's
  cap is its `max_output`, else `window.output_reserve`, but no more than a
  quarter of a context too small for the window and that reply
  (`Window.ReplyCap`; a context that holds both, such as the one an Ollama
  model without `num_ctx` is asked for, keeps the whole reserve);
  `max_output` must be below `num_ctx`. The window is sized for the context less that cap
  (`Window.Sized`): it grows as before, and it now shrinks, set water marks
  included, when the context cannot hold it. The validation that
  `window.high_water + window.output_reserve` fit `[control]`'s `num_ctx` is
  gone with the reason for it. An Ollama model without `num_ctx` is asked for
  the high water plus its own cap. A cap that differs from the window's
  default goes with each request (`llm.req` `max_output`), so it replays.
- **Settings say it that way.** The model form has the reply cap beside the
  context length, its default spelled as a number, and a line saying what a
  conversation on the model gets as both are typed; Settings › Context window
  calls `output_reserve` the *Default reply cap*. The note a cut reply leaves
  names "the reply cap of model NAME".

Not built, and not decided: a bundled catalogue of providers, models, context
lengths and prices (models.dev, LiteLLM's map), which would size and price
OpenAI's and Cerebras' models without the person saying it. OpenAI's model
object, asked with the key, is still `id`, `object`, `created`, `owned_by`
and `shutdown_date`, so their models are sized and priced only from their
entries until then.

## Amendment 2026-10-05: what providers publish fills what an entry leaves unset

Even with the key sent, OpenAI's model object is `id`, `object`, `created`,
`owned_by` and `shutdown_date`, so gpt-6.1-sol at `api.openai.com` still kept
a window of no particular size unless someone typed 1,050,000, still capped
replies at 16384 for a model that writes 128,000 (OpenAI recommends room for
at least 25,000 when reasoning), offered efforts only if someone typed them
(and the ones typed for gpt-6-luna lacked OpenAI's `none`), and priced
nothing. Every agent that works with these providers has the same gap; they
fill it the same way: a community catalogue shipped with the program, its own
settings winning (LiteLLM's `model_prices_and_context_window.json`, which
Aider uses; models.dev, which opencode uses).

- **The catalogue is models.dev's**, read into `internal/catalogue` as a
  snapshot built into nodwyn: per provider, per model id, the context, the
  longest reply, the `effort` reasoning options (a toggle or a token budget is
  not a word a request names), whether it reads images, and the prices per
  million tokens, with cache reads, cache writes and a context tier.
  `go generate ./internal/catalogue` regenerates it (`gen`, which also reads
  a saved copy); nothing in the build or the tests asks models.dev, and a
  regeneration's diff shows each provider's changes, one model to a line.
- **A model is found by its server's host and its exact id.** The hosts are
  a table in nodwyn's source (`catalogue.Providers`: OpenAI, OpenRouter,
  Cerebras, Groq, DeepSeek, Mistral, xAI, Together, Fireworks, DeepInfra,
  Moonshot, Z.ai, Ollama Cloud); models.dev never supplies an address, so the
  catalogue describes models and decides nothing about where a request goes.
  Nothing is inferred from a model's name: an id the provider does not list
  is not described.
- **An entry's settings win; the catalogue fills what it leaves unset**
  (`Config.WithPublished`, `Config.Efforts`): the context length (asked of
  the catalogue before a server's model list), the reply cap (the published
  longest reply, at most a quarter of the context, so a model whose limit is
  its whole context still leaves the conversation most of it), the efforts,
  that a model reading no images takes none, and the prices, cache writes and
  a prompt past a tier priced at their own rates (`llm.Rates.CacheWrite`,
  `Tiers`). Prices typed in an entry still price both as input. An effort is
  checked against the published efforts when the entry lists none.
- **Settings say where a number comes from.** Each choice in `settings.read`
  carries `published`; `models.published` answers for a model not yet in the
  list. The model form shows published values as placeholders, offers *As
  published* for its efforts (the entry then lists none, so the provider's
  apply as the catalogue changes), says when typed efforts differ from the
  published ones, and the row says *as published*. The effort labels read
  `xhigh` as *Extra high*.

Not built: refreshing the catalogue while nodwyn runs (a daily ETag'd fetch of
models.dev into the data directory, taking only numbers and models for the
providers above), and listing the catalogue's providers in Settings before
any key is added for them.

## Amendment 2026-10-05: the judge's model settings sit with the models'

A task on gpt-6.1-sol showed "Judge … 4 calls · 3,267 tokens · not priced":
`[jev]` had no prices, though typesafe.ai publishes jev's ("$42 per billion
input tokens"), and the judge's model, prices and wait sat apart from every
other model's settings, under Judge.

- **The judge is priced as published** when `[jev]` sets no prices
  (`Config.JevPrices`): `internal/catalogue` keeps a small entry of its own
  for models models.dev does not list, by host, each citing its provider's
  page; `api.typesafe.ai`'s `jev-latest` is $0.042 per million input tokens
  and nothing for output. `settings.read` carries it as `jev_published`.
- **Settings › Models holds the judge's model settings**, after the models':
  its model, its prices with the published ones as placeholders, and its
  wait. Settings › Judge keeps whether it is available, what it sees and the
  probability at which it allows a command, and points to Models for the
  rest.
- The side panel's Context meter shows from the start of a task, empty
  until the conversation's first request: the window is known when the task
  opens, while the first requests are the planner's in a context of its own.

## Amendment 2026-10-05: the images choice shows what applies

Settings' *Takes images* showed Yes for a model whose entry said nothing,
and saved only No, while the catalogue said some of those models read no
images: the form said Yes, the row said *takes no images*, and choosing Yes
saved nothing, so the catalogue's word could not be overridden from
Settings. The choice now shows what applies (the entry's answer, else what
the provider publishes, else Yes) and saves an answer only when one is
chosen, Yes included (`vision = true`), so an entry still follows the
catalogue until a person answers.

## Amendment 2026-10-05: a price set alone is kept

A model's rates were its entry's when it set both input and output, else
the published ones whole, and the judge's the same (`Config.JevPrices`). A
price typed alone in Settings, whose empty fields say they are the published
price, was dropped: an output price typed alone left requests priced at the
published output price, and on a model nothing is published of, priced
nothing. Each price is now the entry's when set,
else the published one (`Config.Rates`, `Config.JevPrices`), and the row
shows the pair that applies. The published cache-write price and long-prompt
tiers still apply only while the entry types no price, since a price typed
is the one meant whatever the prompt's length; Settings' hint says so.

## Amendment 2026-10-05: the fallback reply cap

Settings › Context window called `output_reserve` the *Default reply cap*,
"for a model whose own reply cap is empty", though a model with an empty
cap first has the one its provider publishes (above). It is now the
*Fallback reply cap*, for a model with none of its own and none published,
and the model form's hint names it.

## Amendment 2026-10-05: Anthropic and Baseten are providers to choose

Settings offered presets for Ollama, OpenAI, OpenRouter and Cerebras, and
no way to use Claude on Anthropic's own API or a model on Baseten.

- **Anthropic is a kind** beside Ollama and OpenAI-compatible (ADR 8, amended
  2026-10-05): its preset fills in `https://api.anthropic.com/v1`, the form
  offers no dialect, and the key is stored with `POST` and `GET` rows at
  `header:x-api-key` instead of a bearer header. `settings.provider.write` and
  `settings.control.write` take `provider: "anthropic"`; readiness, the test
  request and the model list use the key where Anthropic reads it. The first-run
  form offers it too. The model form offers an Anthropic model no seed, no
  reply cap field and no session key (it has none of them), says temperature
  is 0 to 1, offers the efforts Claude takes (low to max) among its shortcuts,
  and starts a model added there with the prompt cache's five minute ask
  chosen. The catalogue describes Anthropic's models at `api.anthropic.com`:
  context, reply cap, efforts, whether it reads images, and the prices, cache
  reads and writes included.
- **Baseten is a preset** of the OpenAI-compatible kind at
  `https://inference.baseten.co/v1` on chat completions with a bearer key, which
  is how its Model APIs are documented, and the catalogue describes its models
  at `inference.baseten.co`. Nothing else about it is special: its efforts are
  whatever models.dev publishes, and its models list (`GET /models`) is asked
  with the key like any other.
- The snapshot was regenerated (`go generate ./internal/catalogue`): it adds
  both providers, and refreshes the others' entries to models.dev as of today
  (a handful of OpenRouter prices moved).
- Not built: reading a model's efforts or image support from Anthropic's model
  list (`capabilities`), which it reports and the catalogue also publishes;
  Opus 4.5's effort with a thinking budget; Anthropic through Bedrock or
  Vertex, which sign requests differently.

## Amendment 2026-10-06: servers are keyed by name, not by address

A `[servers.NAME]` is known by its name. Two can share an address, which is
how an API-key server and a ChatGPT plan server both sit at
`https://api.openai.com/v1`; `settings.provider.write` no longer refuses a
second server at an address that has one, only a name already in use is
changed rather than added. A request without `name` still finds the server by
its address when exactly one is there, and is refused, naming them, when
several are.

- A model on a server names it (`server`), and everything keyed by that
  server follows the name: `Config.SameModel`, `Tuned` and `Efforts` compare
  the server name when both models have one (provider and address only for an
  inline model), the probe cache and the window-size cache include it, and
  `settings.control.write` keeps the server the main model names when the
  address is given again.
- The console's Add a provider form takes a name and shows the server by it;
  a second provider can be added at an address in use.
- Not changed: the catalogue still describes a model by its address's host,
  so both servers at OpenAI publish the same prices and efforts. The log's
  `server` name (ADR 2) was already the name, and still is.
