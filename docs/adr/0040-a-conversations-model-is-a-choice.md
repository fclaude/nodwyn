# ADR 0040: A conversation's model is a choice

- Status: accepted; implemented 2026-10-03 (slice 1: the log, the session and the wire)
- Date: 2026-10-03

## Context
Configuring models already separates the key from the model: a server
(`[servers.NAME]`) holds one address and one key, `[models.NAME]` names a
model on it (with its context length, reply cap, sampling and prices), and
`[control]` is the model a session talks to unless a strategy asks another.
A provider such as OpenRouter offers hundreds of models at one address under
one key, and `models.offered` already lists them to the console without
sending the key.

What was fixed was the conversation: `Options.Config.Control` was read when
the session was built, and only Settings › Model (one form, one `[control]`
table) or a hand-edited `config.toml` could change it — for the *next*
session. A strategy could hand one turn to another model (`(model-turn
"NAME")`, ADR 27), but a person could not choose the model a conversation
talks to, and could not move one to another model as it went on. So "one
model per key" was true of the app, not of the storage.

## Decision
- **A conversation's model is a choice.** It is config.toml's `[control]`
  model, a `[models.NAME]` entry, or a model on a configured
  `[servers.NAME]` — the last needs no `[models.NAME]` block, so the models
  a server lists can be chosen without writing one line each. The chosen
  model answers `(model-turn)` from the next turn on; `(model-turn "NAME")`
  still asks a named model for one turn, unchanged.
- **The choice is the conversation's, so it is in its log.** `session.model`
  records the model id, where it is reached, the server and the registry
  name, who chose it, and the window's recipe from then on. It is written
  when a model is chosen for a conversation as it opens, and at each switch;
  `session.start` and `session.resume` record the model the session talks
  to when it opens, which is the chosen one. Resuming restores the last
  choice, so a conversation picks up where it was left; a choice
  config.toml no longer offers falls back to `[control]` and the log says
  so, rather than making the task unopenable.
- **Who writes it.** Only the daemon, or another nodwyn process holding the
  log's lock, writes a session log: it lives in nodwyn's data directory,
  never in a repository, so no stranger's clone can point a conversation at
  another model. The restore reads only what such a writer put there, and
  resolves it against config.toml again, so a recorded choice can reach
  nothing the person has not configured.
- **One key per server, unchanged.** A switch brings the model, never a key
  and never a binding: a model on another server is reached with that
  server's key, and every request checks its binding (`egress.check`) as
  before. No choice carries an address or a credential class, and a model
  whose key is in a keychain this task does not have is refused with what
  to do about it.
- **Between turns, never mid-request.** A switch is refused while a turn or
  an attachment is running: the turn under way has already asked its model,
  and an attachment was confirmed for the model it named.
- **Images are asked of the model that answers.** An attached image goes
  only to the host it was confirmed for (ADR 22 §3), and only to a model
  that takes images. Whether one does is asked of the model, not of the
  name `control`: a switch forgets what the previous model said, so on a
  server that offers both kinds (OpenRouter's shape, one host) an image is
  withheld from a text-only model, with the reason recorded.
- **The window is sized for the model that answers.** A switch sizes the
  water marks, the result cap and the ceiling as opening the conversation on
  that model would (`config.Window.Sized`): from its `num_ctx`, or from what
  the server's model list says of its context when config.toml does not set
  one, and the log says how in a `note`. `session.model` carries the recipe
  the requests after it are built by, as `session.resume` does, and
  `nodwyn replay --window` takes it from there, so every request still
  replays to its recorded hash. The tool catalog and the prompts stay the
  session's (ADR 9).
- **Refused when the target cannot hold the conversation.** The window built
  with the marks sized for the target must fit its context less the room its
  reply cap needs; user messages are never trimmed, so a conversation can
  outgrow any window. Otherwise the switch says so and says to compact
  first. A model whose context neither config.toml nor its server says is
  left to its server, and nothing is judged.
- **It is this computer's act.** A paired device opens a task on the
  `[control]` model; choosing another, at `session.open` or with
  `session.model.set`, is refused to it: another device answers and steers a
  task, but where its content goes stays with the computer nodwyn runs on
  (ADR 30). The daemon refuses it by the device's `via`, as it refuses an
  unwatched run, and the bridge does not offer `session.model.set` to a
  device at all.
- **Methods.** `session.model.set {id, name}` or `{id, server, model}`,
  answering with the model now; `session.open` takes the same fields, so a
  conversation can start on a chosen model; session details carry `model`
  (`name`, `server`, `model`), never a key.

## Consequences
- The console and the TUI can offer a picker (the model list already
  arrives through `models.offered`, `models.status` and now `session.info`).
  Slice 1 builds neither, nor a Settings editor for `[models.NAME]`: the
  wire, the log and the session are what exist.
- A conversation's model is no longer one value per machine, so a run's
  model is the conversation's: a run started on a switched session asks the
  switched model.
- A conversation moved to a model with a smaller context is trimmed by the
  window sized for it from the next turn, as one opened on that model would
  be. The request prefix changes at the switch, so the server's prompt cache
  starts again; so it would anyway, since the cache is the model's.
- The context gauges of the console and the TUI read the high water from
  `session.model` too, so they measure against the model that answers.
- Two models on one server are two choices over one key, so choosing
  between them changes nothing about consent, bindings or the keychain:
  what changed is which one answers.

## Amendment 2026-10-04: the picker, a queued choice, no fallback (ADR 41)

Slice 2 builds what slice 1 left: the console's picker and the Settings editor
for `[models.NAME]`; the TUI's command is still not built. Two clauses of the
Decision change:

- "Between turns, never mid-request" no longer refuses a choice made while a
  turn runs. It is queued, said so in the state and the log, and applied when
  the turn ends, before the next can start; one that cannot apply says why and
  leaves the conversation on the model it had. An attachment in progress and
  an active run still refuse.
- A recorded choice config.toml no longer offers no longer "falls back to
  `[control]`". The conversation opens, says what it was left on, and asks no
  turn until a model is chosen for it, so it is never answered by a model, or
  sent to a provider, nobody chose for it.

A choice also carries the effort its requests ask for (`session.model`
`effort`), and `session.model.set` with the model the conversation is on
changes the effort alone, without sizing or fitting anything again.
