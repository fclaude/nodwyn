# Local session protocol (U2)

Start the daemon in the foreground with `nodwyn daemon`. It is the `nodwynd` role
from ADR 21, shipped in the existing binary. `nodwyn daemon --no-keychain` skips
keychain startup; otherwise it opens a keychain without a passphrase at once,
and asks for a passphrase with the same masked terminal unlock as chat.
Configuration is loaded once at startup. No model connection is needed to list
or open sessions. `SIGINT` and `SIGTERM` close clients and session writers.

The endpoint is `$XDG_RUNTIME_DIR/nodwyn/sock`. When `XDG_RUNTIME_DIR` is absent,
it is `<user cache directory>/nodwyn/run/sock` (on macOS,
`~/Library/Caches/nodwyn/run/sock`). `--socket /absolute/private/directory/sock`
overrides it. The immediate parent must be owned by the current user, mode
0700, and not a symlink; missing directories are created privately. The socket
is mode 0600. A socket that accepts a connection, and any file that is not a
socket, is never replaced: the daemon refuses to start beside it. A socket that
refuses connections was left by a daemon that died without closing it (killed,
out of memory, a power loss); `nodwyn daemon` and `nodwyn app` remove it and
start, so a service manager can restart the daemon (systemd's
`Restart=on-failure`) with nothing to clean up. The check and the bind are made
holding a lock on the socket's path with `.lock` appended (`sock.lock`), so of
two daemons starting at once the second finds the first listening and refuses.

A connection has the authority of the local user. Opening or resuming a
repository loads its currently trusted `.nodwyn/init.scm` and repository policy, just as the plain
frontend does; initialization confirmations remain denied. Turns and explicit
user Scheme evaluations use the same guard and budget path as chat. Connecting
or subscribing never approves a pending operation. The transport is local-only;
optional TCP/token authentication from ADR 21 remains deferred.

## Transport and handshake

Use [JSON-RPC 2.0](https://www.jsonrpc.org/specification), with one complete
JSON value per newline, encoded as UTF-8. A request frame must be smaller than 1 MiB;
an oversized frame closes the connection. Requests use named object parameters
with exact, case-sensitive names. Unknown or null parameters and missing required parameters are errors. Unknown
response fields should be ignored. String, numeric and null IDs are preserved
exactly as JSON values; large numeric IDs do not pass through floating point.
Notifications have no response; batches execute in order and omit notification
responses. Each connection has one writer and a queue of at most 128 outgoing
messages. A full queue disconnects that client; the session and other clients
continue. Socket writes time out after ten seconds. A client may half-close
its request stream and still read queued replies before the server closes it.

Each connection must first complete:

```json
{"jsonrpc":"2.0","id":1,"method":"hello","params":{"protocol":1}}
```

The result contains `protocol`, the binary `version`, a `methods` list and
`peer`. Clients use that list to discover supported operations. `peer` is
`unix:uid=UID,gid=GID,pid=PID`, read from the kernel once when accepting the
connection (Linux `SO_PEERCRED`; macOS `LOCAL_PEERCRED` and `LOCAL_PEERPID`).
Connections must have the daemon's effective UID. Requests cannot set an actor
or replace this identity. This attributes a local process, not a human or a
signed application; other processes under the same user have the same socket
authority, and PIDs can be reused after a process exits. A different protocol version fails with code `-32001`. Request and
response payloads are not printed to daemon diagnostics. Application results
and error messages pass through shared redaction; caller-selected correlation
IDs are transport data and are echoed unchanged.

### Naming

A method is `<noun>.<verb>`, with the noun taken from the thing it acts on
(`session`, `settings`, `credentials`, `workspace`, `mcp`), and a new method
follows these rules. A read of one thing is `.read`, of many `.list`; a
change to a setting or a file is `.write`, and a removal is `.remove`. A
boolean parameter is named for its state (`on`, `archived`, `paused`), and an
id that names a session is `id`.

The methods that came first do not all follow them, and none is renamed:
clients ship, and a rename would break one that is not upgraded. The
exceptions are `.set` (`session.model.set`, `session.judge.set`,
`session.limits.set`, `schedule.set`), `.save` (`workspace.save`,
`mcp.save`), `.add` and `.replace` (`credentials`), `.create`
(`session.approval.create`, `session.binding.create`), `.delete` and
`.forget` where the rest say `.remove` (`session.delete`, `index.forget`),
`notes.read` beside `session.notes.write`, and `session.approve_all`, which
has no verb. Prefer the rule over the nearest neighbour.

## Lifecycle methods

| Method | Named parameters | Result |
|---|---|---|
| `session.list` | `{}` or omitted | Stored sessions, in ID order |
| `session.open` | `{"repo":"/absolute/repository"}`, or with `"name"`, or `"server"` and `"model"`, and `"effort"`, and `"judge"`, and `"delegates":["MODEL",…]`, and `"limits":false` | Open-session details |
| `worktree.open` | as `session.open` | Open-session details of a session in a new git worktree of `repo` |
| `worktree.diff` | `{"id"}` | `{base,changes:[{path,status,diff}],omitted}`: what the open session changed in its worktree since its base commit |
| `worktree.review` | `{"id","comments":[{"path","line"?,"text"}],"note"?}` | `{}`: sends the comments to the session as one message |
| `session.resume` | `{"id":"SESSION_ID"}` | Open-session details |
| `session.totals` | `{"id":"SESSION_ID"}` | Indexed totals and latest turn/run boundaries, for an open or closed session |
| `session.info` | `{"id":"SESSION_ID"}` | Details of a session owned by this daemon |
| `session.model.set` | `{"id":"SESSION_ID","name":"coder"}`, or `"server"` and `"model"`, and `"effort"` | The model and effort the conversation talks to now, and the choice queued behind a running turn |
| `session.judge.set` | `{"id":"SESSION_ID","judge":"jev"}`, or `"judge":""` for none | The judge the task asks now |
| `session.limits.set` | `{"id":"SESSION_ID","on":false}` | `{"on":false}`: whether the task's turns stop at the turn limits, from its next turn |
| `session.close` | `{"id":"SESSION_ID"}` | `{}` after releasing the session writer |
| `session.rename` | `{"id":"SESSION_ID","title":"…"}` | `{}` after recording the name; `""` takes it back |
| `session.archive` | `{"id":"SESSION_ID","archived":true}` | `{}` after archiving a closed session, or bringing it back |
| `session.delete` | `{"id":"SESSION_ID"}` | `{}` after removing a closed session's log |

List entries contain `id`, `repo`, `open`, `busy`, `first_message`, `last_event`
(RFC 3339), `in_flight` and `truncated`. `open` means this daemon owns the
session. `in_flight` describes durable history, not process liveness; a session
owned by a different process still appears with `open: false`. A session in a
git worktree of its own also has `worktree` (below); its `repo` is the
worktree, and a list shows it under `worktree.repository`.

Open-session details contain `id`, `repo`, `busy`, `tools` (the existing model
tool schemas) and `budgets`. Budgets contain `tool_calls`, `tokens`, `actions`,
`wall_ns` and `cost`, each with numeric `soft` and `hard` fields. `cost` is
the task's spend limit in millionths of a US dollar, counted over the whole
task rather than a turn; a `hard` of 0 is none. These are the
configured limits, including the legacy tool-call override; this
protocol does not expose live turn counters. A `hard` of 0 is none in the
other four too: a task whose turn limits are off reports `tool_calls`,
`tokens`, `actions` and `wall_ns` as `{"soft":0,"hard":0}`, and its spend
limit as configured. Details also include `limits`, false for such a task
(ADR 49), and
`allowed_commands`, `allowed_writes`, `turn_writes`, optional `turn_actor`,
`approve_all` ("don't ask" is on), optional `approve_actor` (who turned it
on) and `approve_all_offered` (Settings offers the switch,
`approval.dont_ask`; until it does, `session.approve_all` will not turn it
on). Details also include `model`: the model this conversation talks to now,
as `name` (a `[models.NAME]` entry, or `control` for config.toml's
`[control]` model), `server` (the `[servers.NAME]` it is on, when it is on
one) and `model` (the id as the provider names it); the `effort` its requests
ask for (empty asks none) and the `efforts` the model takes, which is none for
a model that takes no effort setting (ADR 41). `queued`, when a choice was made
while a turn runs, is the model and effort that will apply when it ends,
in the same fields; `notice` says why the last such choice could not be
applied; `unavailable` says the model the conversation was left on can no
longer be reached as it was chosen, with what it was, and no turn is asked
until one is chosen; `context_unknown` says why the window keeps its default
size, when neither the model's entry, its provider's catalogue, its server's
model list nor any provider publishing a model of that name says how much
context it holds (empty when the window is sized for the model). No key and
no credential class is in any of it.

A conversation's model and effort may be chosen as it opens and moved between
turns (ADR 40, ADR 41). `session.open` takes them the same way
`session.model.set` moves an open session: `name` is a `[models.NAME]` entry
or `control`, or `server` and `model` name a model on a configured
`[servers.NAME]` (which needs no config.toml entry of its own); `effort` is one
of the efforts the model takes, refused with the words it takes when it is not,
and empty is the model's own default effort. A switch applies from the next
turn on, with the window sized for the new model as opening on it would size
it; naming the model the conversation is on changes the effort alone, which
sizes and checks nothing again. While a turn runs the choice is queued, not
refused: it is recorded as a `session.model` event with `queued` and no
`context`, and a later one replaces it; when the turn ends and before it is
idle, the choice is applied, recorded as a `session.model` event carrying the
window's recipe from then on, or, if it cannot be (the conversation would not
fit the target's context, its `num_ctx` or what its server's model list says,
less its reply reserve; config.toml no longer offers it), a note says why,
`notice` carries it, and a `session.model` event says again what the next turn
asks. It is refused while an attachment is added and during a run. A
conversation closed with a choice queued has it, and resuming applies it. All
of it is refused to a paired device, whose `session.open` without a model
opens on `[control]`: where a conversation's content goes stays with this
computer (ADR 30). Resuming a session restores the model and effort its log
last named; when config.toml no longer offers the model, or the effort, the
session opens with `unavailable` and `session.send` is refused until a model
is chosen with `session.model.set`.

A task's judge is a choice as its model is (ADR 47). Details contain
`judge`: `server` (the `[servers.NAME]` it is on, empty for none),
`provider`, `host`, `model` (its provider's judge model, or `[judge] model`
on the server `[judge]` names), `price_input` and `price_output` when known,
`available` (its key stored, unlocked, allowed for its host, and not a
ChatGPT plan's sign-in) and `why` when not. `session.open` takes `judge` the
same way: absent is `[judge]`'s server, `""` is none, and a name must be a
server whose provider is `jev` or `openai` with a credential (`-32000`
otherwise, with why). `session.judge.set` moves an open task to another
judge, or none, from its next question; it is refused while a turn runs or
an attachment is added (`-32000`: choose it when the turn ends), and during
a run. A ChatGPT plan's sign-in is refused as a judge wherever it is chosen,
since the keychain never sends it to the Decisions API. Each choice, at open
and later, is a `session.judge` event (`server`, `provider`, `host`, `model`,
`actor`); resuming asks the last one's judge, or none, with a note, when
`config.toml` no longer offers it. A paired device may do neither: `judge`
in `session.open` and `session.judge.set` are refused to it (`-32602`), as a
model choice is.
`delegates` in `session.open` and `worktree.open` names the `[models.NAME]` the
task's model may hand work to with its `delegate` tool (ADR 27, amended
2026-10-09; none by default, which offers no such tool; an unknown name or
`control` is refused with `-32602`). It is fixed for the task, since the tool
list is, and recorded as a `session.delegates` event (`models`, `actor`), which
resuming restores for the models `config.toml` still has. A paired device may
not choose them (`-32602`). Session details report them as `delegates`, and
each model in `settings.read`'s `choices` carries its `labels`.
`limits: false` in `session.open` and `worktree.open` opens a task whose turns
have no limit on tool calls, tokens, actions or time (ADR 49); left out, or
`true`, they stop at the configured limits. The spend limit, the timeouts and
the progress guard apply either way. `session.limits.set` turns an open task's
turn limits on or off from its next turn: a turn running keeps the limits it
started with. `on` is required. It is refused during a run (`-32000`). The
choice, at open when it is off and at every change, is a `session.limits`
event (`on`, `actor`), and resuming keeps the last one's. A paired device may
not turn a task's limits off as it opens, nor change them: `limits: false`
and `session.limits.set` are refused to it (`-32602`), and its tasks start
with their limits on.
Session details also contain `bindings`, a copied array of loaded metadata rows
with `Class`, `Dest`, `Positions`, `Methods` and, when a row names them,
`Scopes` (OAuth2 scopes the row allows, ADR 24) fields. No credential values are
included; configuration edits reload before egress. Inspection reports the
last validated snapshot; invalid reloads deny egress. Older daemons may
omit this additive field, which clients must distinguish from an empty array.
Details also contain `workspace` (`mounts`: the folders outside the root
the session may use, from the workspace list in the config directory),
`git` (whether the root is a git work tree) and
`mcp` (each MCP server offered in this workspace, with whether its tools are
offered or why they are withheld: `state` is `ready`, `unapproved`,
`changed`, `unavailable` or `error`; a contained server has `transport`
`container`, `host` `mcp:NAME` and its `network` hosts), and, for a session
in a git worktree of its own, `worktree`: `repository`, `path` (the same as
`repo`), `branch`, `base` (the branch the repository had checked out, `""`
when its `HEAD` was detached) and `base_commit`.
The approval fields report current effective prefix lists and turn-wide write approval;
file-backed and session-only prefixes share the lists. They are not setters.

`session.rename`, `session.archive` and `session.delete` let a person keep
the task list (ADR 39). A name is trimmed to one line and has at most 120
characters (`-32602` otherwise); an open session records it as a
`session.title` event its subscribers receive, and a closed one through
the store. Archiving and deleting refuse a session this daemon has open
(`-32000`: close it first); all three refuse a session another process has
open, by its writer lock. Resuming an archived session brings it back.
Deleting removes the log for good; the files the session changed stay. The
browser bridge lets a paired device rename and archive, and only this
computer delete.

`worktree.open` takes `session.open`'s parameters, the model choice
included, and starts the session in a new git worktree of `repo`, on a
branch of its own (ADR 34): `repo/.nodwyn/worktrees/<id>` on
`nodwyn/<id>`, where `<id>` is the random end of the session's id, from
`repo`'s `HEAD`. The worktree is the session's root; notes, MCP servers'
workspaces and the history strategies read are the repository's.
Uncommitted changes in `repo` are not carried (`worktree.create` counts
them), and nothing untracked is copied: its mounted folders are the
repository's, from the workspace list.
A folder that is not a git repository's top folder, a bare repository and
one with no commit are refused (`-32000`) with a sentence saying so.
`repo` may itself be such a worktree: the new one is its sibling, from its
`HEAD`. Closing the session removes the worktree and its branch when
nothing in them is unlanded (no change, no untracked or ignored file, no
commit its base lacks) and keeps it otherwise; `session.resume` makes a
missing one again, on its branch if that still exists. A client offers the
choice only when `hello` lists the method; a paired device may call it,
as it may call `session.open`.

`worktree.diff` and `worktree.review` are for an open session in a worktree
(ADR 34); any other session, or a closed one, is refused (`-32000`).
`worktree.diff` returns the base commit (the one the worktree's branch
started from: a worktree made again on a new branch when its task resumed
starts from where its base branch was then, ADR 34 amended 2026-10-15) and,
per file, `status` (`A`, `M` or
`D`; committed, staged, unstaged and untracked changes alike) and a unified
`diff`, with the files the model may not read left out. A file whose diff has
no lines to show says why in a note in parentheses instead: `(diff too large
to show)` above 128 KiB, `(binary file)`, `(not a regular file: a link, folder
or submodule)`, `(no lines changed)` for an empty file or a change of mode
alone, `(could not read this file)` when git cannot, which does not fail the
rest. The changes listed take at most half a frame (512 KiB as JSON); the
files after that are counted in `omitted` and not listed. The read does not
hold the daemon's other methods, and stops after 30 s (`-32000`). It is
read-only, so a paired device and a read-only console may call it.
`worktree.review` formats its comments as one message, numbered in the order
of their file and line and grouped under the file, and sends it as
`session.send` would: an empty `comments`, a run still going (`-32000`: wait
for it to end) and a comment without `path` or `text` (`-32602`) are refused,
and nothing is sent. `line` counts the file as it is now, absent for a
comment on the whole file. The text of the line is not part of a comment: it
is file content, which would reach the model in a message with the user's
authority, and a client that sends one as `quote` has it ignored.
The console may display the unified diff as old/new columns or aligned
side-by-side rows; this is client-side presentation only. Line comments still
use the new/current-file number, never an old-only deletion's number. A whole
file comment has no line (the console sends `0`); explanatory notes have no
line targets. Both layouts use the same `worktree.diff` reply and
`worktree.review` payload: no mode, old-line or quoted-content field is added,
and the wire schema is unchanged.

`session.resume` returns the already-open session if this daemon owns it.
Otherwise it uses `core.Resume`, including its exclusive writer lock, recovery
and current initialization. A writer held by another process is an error.
There is no automatic execution or replay of interrupted tools. Closing a
session keeps its log. Disconnecting a client keeps the session open; another
connection can inspect or resume it. After daemon shutdown, the plain frontend
or a new daemon can resume it from the same log.

Protocol errors use the standard JSON-RPC codes. Application errors use
`-32000` for a lifecycle, store or prompt-state failure, `-32001` for a missing/incompatible
handshake, `-32002` for a session not owned by this daemon, and `-32003` if
a reply has no transport identity. Unsupported
methods return `-32601`. A malformed or oversized request does not terminate
the daemon or other clients.

## Turns, subscriptions and log pages

| Method | Named parameters | Result |
|---|---|---|
| `session.send` | `{"id":"SESSION_ID","text":"your message"}` | `{}` when the turn is accepted |
| `session.abort` | `{"id":"SESSION_ID"}` | `{}` when cancellation is requested |
| `session.subscribe` | `{"id":"SESSION_ID","after":0,"limit":100}` | First catch-up page, `busy`, and `pending` |
| `session.unsubscribe` | `{"id":"SESSION_ID"}` | `{}` after stopping new delivery |
| `log.runs` | `{"id":"SESSION_ID","before":0,"through":0,"limit":10}` | Run previews, newest first, over a fixed durable prefix |
| `log.read` | `{"id":"SESSION_ID","after":0,"through":0,"limit":100}` | One page of durable records |

Only one turn may run in a session. Send and abort return immediately; subscribe
before sending to observe progress. Disconnecting does not abort a turn.
If a send response is lost, inspect session state and history before deciding
whether to resend: another send starts a new turn, not an idempotent retry.

A subscription is explicit and belongs to the connection. Repeating it replaces
that connection's cursor for the session. Notifications and responses may
interleave, including notifications arriving before a subscribe/send response;
clients must distinguish `method` notifications from responses carrying `id`.

A live event has the following envelope (the payload uses the existing Go event
or log payload's JSON shape):

```json
{"jsonrpc":"2.0","method":"session.event","params":{"id":"SESSION_ID","kind":"stream.text","seq":0,"payload":{"Text":"Hello"}}}
```

Durable events carry their log `seq`; ephemeral text, status and prompt events
carry zero. Each session has its own sequence. The daemon forwards events
already redacted by core, and log reads redact legacy payloads again without
rewriting disk history. There is no execution replay or durable replay buffer.

A page has `events` (ordinary `log.Event` objects), `next`, `through`, `more` and
`truncated`. `after` is exclusive; omitted/zero starts at the beginning. `limit`
defaults to 100, with a maximum of 1000 records; response bytes depend on record
size. `through` selects an inclusive fixed endpoint; omitted/zero in `log.read`
selects the current last complete record. Cursors beyond history are errors.
An incomplete final line sets `truncated: true`; complete records remain
readable, but corruption elsewhere fails the request. Reading a closed session
requires no resume, writer lock or initialization. A page is read from the
log's index (ADR 49 §10): from the nearest turn boundary, run boundary or sparse mark (one every 256
records) at or before `after + 1`, not from the log's first record, so a page costs the
same in a long log as in a short one. `session.list` folds into each grown
log's index only what the log gained.

`log.read` and `session.subscribe` accept `latest_run: true` with `after`
omitted or zero. This selects the latest run in the fixed `through` prefix
and returns the ordinary bounded event page plus `run {seq,ts,through,reply}`,
`earlier` (whether older runs exist), and `context` (redacted setup records
inherited before this run). Continuation pages use ordinary `log.read` with
`after: next` and the same `through`, omitting `latest_run`. A subscription
selects its latest run under the existing delivery lock and retains the same
watermark rule: no durable record between the suffix and live delivery is lost.
Do not render `context` as new conversation or apply it as authority: it only
supplies the recorded model and context high water to a display.

`log.runs` returns `{runs,through,before,more,truncated}`. Each run has
`{seq,ts,through,reply}`: its first sequence and time, inclusive end, and a plain
text last-reply preview capped at 2,000 Unicode characters plus an ellipsis.
Previews are redacted again for display. `before` is an exclusive run-start
cursor; omit it for the latest page, otherwise reuse the returned `before`.
`through` fixes the overall prefix and must be reused across older pages.
`limit` is 1..100 (default 10). A cursor that is not a boundary or is beyond
history is refused. A run's contents are ordinary `log.read` pages, starting
at `after: run.seq - 1` and retaining `through: run.through`.

Current boundaries are CLI driver starts (`run.state` running with zero turns),
including all their turns and human follow-ups until the next start. Startup
records are included with the first run; conversation before the first run is
an earlier group when it contains submitted work. A task with no run record is
one conversation, preserving its existing display. `schedule.run` also starts a run boundary. The console requests
five earlier previews at a time and fetches a run's full range only on expansion.
Whole-task totals and Changes retain their independent `session.totals` scope.
Older daemons that do not advertise `log.runs` keep the complete-history view.

`session.totals` reads the same derived index, folding in only newly appended
records. Its `through` is the inclusive last complete sequence and `truncated`
reports a partial tail. It returns `started`, `cost` (USD), `unpriced` and
`estimated`; `cache {prompt,cached}` counts raw prompt tokens only for replies
that reported both prompt and cache-read usage; `speed {out,ms,replies}` sums
output tokens and request wall time for replies with reported output usage.
The rate is `out / (ms / 1000)`, unknown when `ms` is zero.
`judge {calls,tokens,cost,unpriced}` keeps judge usage separately.

`clock {total,work,model,tools,open,prev,open_model,open_tool}` uses milliseconds:
the first four are accumulated durations, the rest Unix timestamps (zero means
no open clock). `total` includes human waits, `work` excludes them; `model`
counts answered request pairs and `tools` counts the union of tool intervals
minus human waits. Completed parts of still-open tools are already in `tools`;
`open_tool` is where their live extension starts. An active client's running
clock extends from `max(prev,open)`; working time extends only without a pending
prompt. Model and tool clocks extend from their open timestamps to now only
while busy without a pending prompt, otherwise to `prev`. A closed task never
counts downtime. End and resume records discard abandoned pairs.

`turns` and `runs` count indexed boundaries; `last_turn` and `last_run`, when
present, hold `{seq,ts}`. A driver `run.turn` followed by its `user.msg` is one
turn. Runs begin at `run.state {status:"running",turns:0}` or `schedule.run`. `changes` holds the latest allowed
`guard.decision` per path in last-approved order (approval is not proof of a
successful write). `attachments` holds attachment metadata with `text` empty.
Both are redacted for display, including legacy records. The response contains
no conversation or model output. Missing or invalid indexes are rebuilt from
the authoritative log; deleting an index loses no history. This read method is
available to paired devices and read-only bridges, without resuming the task.

Reconnect in this order:

1. Complete `hello`, then `session.resume` if the daemon does not own the session.
2. Subscribe with the last contiguous durable sequence already saved locally.
   The response includes `page`, `busy`, and `pending`. Registration fixes
   `page.through`; live durable delivery begins strictly after that watermark.
3. While `page.more` is true, call `log.read` with `after: page.next` and the
   same `through`. Buffer live notifications while filling the gap, then merge
   by session and sequence. Advance the saved cursor only through contiguous
   records. Sequence-zero events are live UI state, not replayable history.

`pending` is a redacted snapshot of core's currently waiting tool, budget and
secret prompts. It carries original prompt IDs and contains no entered secret
or reply channel. A prompt waiting during disconnect remains visible on
reconnect. A prompt may appear both in the snapshot and as a queued live event;
clients deduplicate by prompt ID. Cancellation can invalidate a snapshot at any
time; core remains the owner of validity and rechecks IDs when replies become
available. These descriptors grant no authority to answer.

`session.closed` carries `{"id":"SESSION_ID"}` after draining the session's
stream. Read the log from the last sequence to retrieve its final `session.end`
record. Closing releases all subscriptions for that session; reopening it
requires a new subscription. Unsubscribing does not remove notifications already
queued on the connection.

## Approvals and credentials

| Method | Named parameters | Result |
|---|---|---|
| `session.answer` | `{"id":"SESSION_ID","prompt_id":"PROMPT_ID","reply":"y"}` | `{}` when a one-time answer or budget choice is accepted |
| `session.approval.create` | `{"id":"SESSION_ID","prompt_id":"PROMPT_ID","scope":"session"}` | `{}` when an explicit scope choice is accepted |
| `session.binding.create` | `{"id":"SESSION_ID","prompt_id":"PROMPT_ID"}` | `{}` when an exact pending binding choice is accepted |
| `session.approve_all` | `{"id":"SESSION_ID","on":true}` | `{}` when "don't ask" is turned on or off |
| `session.secret` | `{"id":"SESSION_ID","prompt_id":"PROMPT_ID","secret":"VALUE"}` | `{}` when a credential reply is accepted |

Ordinary `session.answer` accepts `y`/`yes` or `n`/`no`, case-insensitively.
It rejects the terminal's `a`, `t` and `b` shortcuts. Budget prompts instead accept
the existing checkpoint choices: yes to double the soft limit, no to stop, or
an absolute soft limit within the hard ceiling (durations for wall time).
A budget choice never creates tool authority.

A `prompt.choice` is a question with named answers: `{ID, Title, Reasons,
Choices}`, each choice `{Key, Label, Detail, Local}`. Answer it with
`session.answer` and a choice's `Key` as `reply`; any other reply fails and
leaves it pending. A choice marked `Local` widens what nodwyn does without
asking, and is refused from a paired remote device (an actor relayed `via`
another): the console does not offer it there. The first upload to Google
Docs asks one (`ask`, `allow` or `deny`), and the answer is saved as the
`[docs] google_upload` setting. `allow` approves that upload, `deny` refuses
it, and `ask` goes on to a security `prompt.confirm` for it. The saved answer
is recorded as a `setting.change` event: `actor`, `prompt_id`, `key`
(`docs.google_upload`), `value`, `state` (`applied` or `failed`) and `error`.
A `prompt.closed` follows it, as it follows every prompt.

Standing approvals require `session.approval.create`, tied to a currently
pending ordinary confirmation. `scope: "session"` derives the command prefix
or write directory from that call; a file at the repository root grants only
that file. A shell's `-c` line derives the prefix of the one command it runs;
a line of several commands, or an interpreter, has none, and its prompt's
`options.session` is false (ADR 17, amended 2026-10-05). `scope: "turn"` applies to ordinary writes until the turn ends.
Clients cannot submit arbitrary prefixes. Security confirmations, protected
paths and budget checkpoints cannot create standing approvals. Turn-wide and
session approvals never waive security confirmations or denials.

`session.approve_all` turns "don't ask" on or off for an open session: while
it is on, every confirmation a standing approval could answer (an ordinary
write or command) is answered yes without a prompt, as the person who turned
it on, recorded in the `guard.decision` with `scope: "all"`; a confirmation
pending when it is turned on is answered too, and nothing is installed.
Security confirmations and protected paths still prompt, and denials stand.
It lives in the daemon's memory for that session only: no Scheme primitive
reads or changes it, whatever the code's origin (a repository's
`.nodwyn/init.scm` runs as user-origin Scheme), a resumed session starts
with it off, and it is never written to configuration. Turning it on is
refused while Settings does not offer it (`approval.dont_ask`, off by
default), to an actor relayed `via` a paired device, which may only turn it
off, and to a session whose confirmations are not asked of a person
(a run's `auto_approve` or judge, an unattended run). Line mode's `--yolo`
is this switch, turned on as its task opens. Each switch is an
`approval.change` with `kind: "all"`, `scope: "session"`, `prefix: "*"` and,
when it is turned off, `off: true`; when it answered an ask that was
waiting, it carries that ask's `prompt_id`, as every log event that
resolves a prompt does — the console's Totals clocks read it to keep the
wait out of the working time.

Prompt IDs are opaque, generated afresh, and not reused on resume. A reply is
consumed once. Missing, canceled or already answered IDs fail without changing
authority. An accepted reply is delivery acknowledgment: the execution goroutine
checks cancellation again, records a scope change, applies it, and records the
guard decision before the operation runs. Follow events to learn the outcome;
inspect `session.info` for effective authority. A cancellation racing with a
scope change can leave an audit of the request without an effective grant.
Failure to record a guard decision prevents the host operation.

`guard.decision` includes `actor`, `prompt_id` and `scope` for an RPC answer;
`turn.checkpoint` and successful `cred.register` carry their reply's actor and
prompt ID. `approval.change` records the explicit scope, derived kind/prefix
and actor. File-backed approvals and in-process terminal replies need not have
an RPC actor. Resume reads these as history and never reinstalls transient
authority. A confirm `[approval]` answers without a person (ADR 35) has
actor `judge` (the judge allowed it) or `setting` (`commands = "allow"`);
a write that `writes = "allow"` lets through is an allow with checker
`standing` and the diff in `detail`. When the judge refuses, the prompt the
person gets carries the judge's answer among its reasons.

`llm.resp` `usage` may carry `cached_tokens` (the prompt tokens the
provider read from its cache; absent is unknown, not 0), `cache_write_tokens`,
`reasoning_tokens`, and `cost` in USD with `cost_estimated` when it comes
from the model's configured prices rather than the provider. It may also
carry `wall_ms`, how long the request took as the host measured it, from
the sending to the reply's complete arrival, the successful attempt only
(a retry's earlier failures are not the model's time); output tokens over
it is the request's tok/s, the one speed every provider gets. A budget
checkpoint's `state` carries `cost`, `priced`, `unpriced` and
`cached_tokens` for the turn so far. All are optional and additive.

A `plan.step` event is the model marking step `step` of `of` of the turn's
recorded plan `done` or `skipped`, with its `note`; a later mark of the same
step replaces an earlier one. A step's number indexes the plan's numbered or
bulleted lines, or the whole plan when it is prose. A run's later turn
carries the run's plan, and so does a chat's turn after one that did not
finish (its `turn.end` `outcome` other than `completed`): its `gate.plan`
has `noul` `carried`, the same `plan`, and `marks`, the `plan.step` marks of
the earlier turns, the latest per step in step order; the turn's own marks come after it, as
`plan.step`, and a front-end shows both ([runs](runs.md)).

`Prompt.Egress` names an OAuth2 credential's granted `Scopes` with its class
and destination, and a proposed `Prompt.Binding` row pins them. A
`cred.refresh` event records each refresh of an OAuth2 credential: class,
handle id, token endpoint host, new expiry, whether the refresh token
rotated, and `ok` or `error`; never a token. `egress.check` carries the
credential's `scopes` when it has any.

A `doc.upload` event records each document `google-doc-upload` sends (W5):
`tool`, `origin`, the file's absolute `path`, media `type`, `bytes` and
`sha256`, the destination `host` and credential `class`, `sent` (false when
nothing left the machine, such as a document refused for a secret shape),
`ok`, and the document's `id` and `url` or the `error`; never its content or
a token. Its confirm, while `[docs] google_upload` is `ask`, is a security
one with `Prompt.Egress` set and no `Path`, so it offers no standing
answer; with `allow` there is none, and with `deny` the tool is refused. `doc-export`'s confirm is
a write's: `Prompt.Path` is its target.

An `index.update` event records a build or refresh of the repository's
index (ADR 50): the tree's `root`, the `files` it holds, how many it `read`
and `removed`, how many it `skipped` (not a regular file, too large,
binary or protected), the paths the guard `refused`, how many were left
`unread` when its time ran out, whether it is `partial`, and `ms`; never
content. Each file it read has its own `guard.decision`. No client shows
it.

A `jev.req` event records one judge request: the tool, its origin, the
content level, the redacted payload's size and hash, the redactions, the
answer, and, when an answer came back, the `model` that answered and its
`usage` (`input_tokens`, `output_tokens`, and `cost`, an estimate from
`[judge]` prices, else those its provider publishes for its questions; the
API reports none). The answer is in System One's shape whichever provider
gave it (`noul`; `score` and `confidence`; `choice`, `confidence` and
`probabilities` by option), and the Decisions API names no `model`. A budget checkpoint's state counts
the turn's judge requests apart: `judge_calls`, `judge_tokens`,
`judge_cost`, `judge_unpriced`.

A `context.compact` event records one compaction: `trigger` (`auto`,
`command` or `requested`), `compacted`, `before_tokens` and `after_tokens`
(the window's estimates, the unit `compact-threshold` is in), `threshold`
for an automatic one, `focus`, and the note's reply: `note_seq`,
`note_tokens` and `truncated`. One that gave up has `compacted` false. An
`llm.resp` `usage` with `unreported` true had no token counts from the
server: its zeros are not counts.

An `mcp.egress` event records one attempt of a contained MCP server to reach
a host through its egress proxy (ADR 23 W3b): `server`, `method`, `host`,
`port`, `allowed` and `reason`; never a path. After 1000 for one server, a
last one carries the count left out as `dropped`. A client shows the
refused ones; the rest are routine.

A `prompt.secret` contains only its ID and credential class. Send the value
through `session.secret`, never Scheme source. The core copies secret bytes
into its typed reply channel and registers them in the shared encrypted
keychain. Only an opaque handle reaches Scheme. The method never echoes the
value or adds it to events/model history; an explicit empty string declines.
Missing or null `secret` is an error and leaves the prompt pending. Transient
byte copies are cleared after consumption, but JSON/string allocations are
managed by Go's garbage collector; this is not a complete memory-erasure claim.

## User Scheme evaluation

`session.eval` accepts `{"id":"SESSION_ID","source":"(+ 1 2)"}` and returns
`{}` once accepted. It has user origin, can invoke hidden setup primitives such
as `(credential-register "service-key")`, and shares the session's single busy
slot, guard, budget, abort and close lifecycle with model turns. Subscribe first:
evaluation may wait for an ordinary approval, budget choice or secret reply.

The durable `eval.req` contains redacted source and the initiating peer's actor.
`eval.result` contains a redacted written `value` (omitted for void), or `error`.
Display output arrives as `output` events; `turn.end` and idle status complete
the lifecycle. Evaluation source/results do not enter the model conversation.
Definitions last for the in-memory session and reset on resume; evaluations and
host operations are never re-executed from history. A crash before `eval.result`
can leave an unknown outcome. Inspect current state before retrying.

## Desk, setup and relayed identity (ADR 30)

| Method | Named parameters | Result |
|---|---|---|
| `attention.list` | `{}` | Open sessions with pending prompts: `[{id, pending}]`, the same redacted descriptors `session.subscribe` returns |
| `notes.read` | `{"repo":"/absolute/repository"}` | `{text}`: that repository's project notes; needs no session |
| `index.status` | `{"repo":"/absolute/repository"}` | `{files, trees, bytes, updated}`: that repository's index (ADR 50), the files its own tree holds, how many working trees the project keeps one for, its size on disk and when the tree last changed; all zero for none. Needs no session; a paired device may read it |
| `index.forget` | `{"repo":"/absolute/repository"}` | `{}`; deletes that repository's index, which the next search builds again. Only on this computer, as `workspace.save` is |
| `session.notes.write` | `{"id":"SESSION_ID","text":"…"}` | `{operation_id}`; a `policy.result` event named `notes` reports the guarded save |
| `models.status` | `{}` or `{"fresh":true}` | Each configured model: `name`, `server?`, `provider`, `model`, `host`, `state` (`ready`, `missing`, `unreachable`, `unconfigured`, `needs_key`, `locked`, `unbound`), `detail` (what to do about a state that is not `ready`); `fresh` asks every server now instead of taking what a check in the last 15 seconds found |
| `models.test` | `{"name"}`, or `{"server","model"}`, and `{"effort"}` | `{ok, state, detail, model, effort?, cache_control?, status?, ms}` for one short request to the model, with that effort, through the same binding check a task's request passes: `ok`, `rejected`, `no_model`, `refused`, `limited`, `unreachable`, `error`, or the key states above before anything is sent |
| `models.offered` | `{"server"}` | The model ids a configured server (`[servers.NAME]`) lists, sorted; an error saying why when it lists none |
| `models.published` | `{"server", "model"}` | What the model's provider publishes about it (`published`, below), for a form adding it; `null` when the catalogue does not describe it |
| `keychain.status` | `{}` | `{exists, unlocked, credentials, no_passphrase?, stale?}`; `no_passphrase` is set when the open keychain has none; `stale` is set when another process changed its passphrase since this daemon opened it (`unlocked` is then false: the keys in memory still work, but nothing can be added or refreshed until `keychain.unlock` is given the new passphrase) |
| `keychain.create` | `{"passphrase":"…"}` or `{"no_passphrase":true}` | Keychain status; tasks opened afterwards receive the keychain. A passphrase has at least 8 characters |
| `keychain.unlock` | `{"passphrase":"…"}` | Keychain status; tasks opened afterwards receive the keychain. `""` opens a keychain without a passphrase. On a keychain that is `stale` it opens the keychain again in place, for the tasks that already hold it too |
| `keychain.passphrase` | `{"current"?, "passphrase"?, "no_passphrase"?}` | Keychain status after setting, changing or (`no_passphrase`) removing the open keychain's passphrase; `current` is required when it has one, and a wrong one waits a second as `keychain.unlock` does |
| `settings.read` | `{}` | `{config_file, control, models, servers, choices, keychain, docs, values, judge, judge_key?, judge_key_detail?, judge_host?, judge_model?, judge_published?, judges}` |
| `settings.control.write` | `{"name","effort"?}`, or `{"server","model","effort"?}` | Settings after replacing `[control]` in `config.toml` (the model and effort new tasks start on); the model it replaces stays in the list. Providers and their keys are saved by `settings.provider.write` first: this method takes neither a provider and url nor a key |
| `settings.provider.write` | `{"name"?,"provider","url","dialect"?,"api_key"?,"credential"?}` | Settings after adding a provider (`[servers.NAME]`, named by `name`, else after its host) or changing the one `name` names; servers are keyed by name, so a second can share an address (an API key and a ChatGPT plan at api.openai.com), and without `name` the address picks the server only when one is at it; `provider` is `ollama`, `openai`, `anthropic` or `jev` (a dialect is for `openai` alone; a `jev` server answers the judge and carries no model, ADR 46); `credential` is a ChatGPT plan credential in place of a key (below); the key goes to the keychain, bound for `POST` and `GET` at the host where the provider reads it (a bearer header; Anthropic's `x-api-key`), and for `POST` alone for `jev` |
| `settings.provider.remove` | `{"name"}` | Settings after removing a provider no model and not the judge is on |
| `settings.model.write` | `{"name"?,"server","model","efforts"?,"effort"?,"labels"?,"settings"?}` | Settings after adding a model to the list (`[models.NAME]`) or changing its provider, id, efforts, starting effort, labels and settings; `labels` (lower-case words) left out keeps the entry's, `[]` clears them, and tasks already open see the new ones at once |
| `settings.model.remove` | `{"name"}` | Settings after removing a model from the list; the default model is not removed |
| `settings.docs.write` | `{"google_upload"}` | Settings after setting `[docs] google_upload` (`ask`, `allow` or `deny`) in `config.toml` |
| `settings.write` | `{"set": {"budget.tokens_hard": 20000000, "budget.cost_hard": null, …}}` | Settings after changing those settings in `config.toml` together |

`settings.provider.write.credential` is a stored OpenAI OAuth2 credential id,
mutually exclusive with `api_key`. It selects the credential's unshadowed
class and binds it for GET and POST bearer headers at api.openai.com. It
requires an unlocked keychain, a renewable credential that needs no
reauthorization, `provider = "openai"`, `url = "https://api.openai.com/v1"`
and `dialect = "responses"`. An API key replacing OAuth gets its own class;
the OAuth account remains stored. `settings.read.servers[].authentication`
is `"chatgpt"` for OpenAI OAuth credentials when the keychain is unlocked;
omitted otherwise. No token is returned.

`session.list` entries also carry `title` (the name a person gave the
session, else the first sentence of the first message, shortened),
`archived` (ADR 39), `started`, `changes` (files the guard allowed the session
to write, first 50, from `guard.decision` records, which now carry `path`) and
`run` (the live run, or the last recorded `run.state`). `models.status` sends
no credential: it asks each server for its model list and reports whether it
answered, after saying what the keychain and the binding table know of its key
(`needs_key`, `locked`, `unbound`), which is checked first since it is what to
fix. `models.test` is the one request that sends a key: after the same binding
check, one short message ("Reply with the single word ok.") and nothing of any
task; its `detail` is the provider's own message, redacted, and never the key.
The effort it asks must be one the model takes, refused before anything is sent
when it is not. It carries what the model's entry asks of its provider as a
task's request would, `cache_control` included (ADR 9, 2026-10-05), so a
provider that refuses it answers `refused` with what to turn off. `models.offered` asks one configured server for its list. Unlike
`models.status`, a chat-completions server is asked with its key, after the
same binding check a model's requests have, since OpenAI, Cerebras and
Anthropic list their models only to one: a model key is bound for GET (the
list) as well as POST, and the daemon adds the GET row at start to a key that
config.toml names for a chat-completions server and that only POST was bound
for, at the same host. An Anthropic server's list gives each model's context
window, which sizes a conversation whose model's entry and catalogue say none. Its errors say
what to do: a server that wants a key and has none, a key it did not
accept, or a key that was not sent (the keychain is locked, the binding
refused it). `servers` in the settings are the model providers: the
`[servers.NAME]` tables of `config.toml`, and the address a model gives inline,
`{name, provider, dialect?, url, credential?, host, inline?, key?, key_detail?,
uses, judge?}`, `credential` being the key's keychain class (never the key), `key` what
the keychain says of it without asking the server (`ready`, `needs_key`,
`locked`, `unbound`; absent for a provider with none), `uses` the names of
the models on it, and `judge` true when `[judge] server` names it. `choices` are the models a task can be started on or moved to,
`[control]` first as `control` with `default: true`, then each `[models.NAME]`
that is not the default model again: `{name, server?, provider, model, host,
efforts?, effort?, default?, settings, published?}`, where `efforts` are the
efforts the model takes (its entry's, else those its provider publishes),
`effort` the one it starts with (ADR 41), `published` what its provider
publishes about it as nodwyn's catalogue has it (ADR 41, amended 2026-10-05),
`{num_ctx?, max_output?, efforts?, images, price_input?, price_cached_input?,
price_cache_write?, price_output?, tier_above?}`, which a session uses for each
of its settings left unset (`max_output` is the reply cap that gives: the
published limit, at most a quarter of the context), and `settings` the
model's own settings, `{num_ctx?, max_output?, output_field?, keep_alive?,
temperature?, seed?, vision?, cache_key?, cache_control?, price_input?,
price_cached_input?, price_output?}`: its entry's, and for `control` those of
the entry it names (ADR 41, amended 2026-10-05). A model on an Anthropic
server takes no `seed`, `output_field` or `cache_key`, and a `temperature` of
at most 1, as config.toml's validation says. `settings.model.write` takes
the same object as `settings`, all of the model's settings at once: one left
out is unset (`vision` unset is a chat-completions model that takes images,
`false` one that does not; ADR 22, amended 2026-10-05), a name it does not know
is refused, and `settings` left out keeps the entry's own. They are written to
the model's `[models.NAME]`; when the model is the default, what `[control]`
held of them moves to the entry, and `[control]` keeps only the model and its
effort. `dialect`, when
configured, is `compatible`, `cerebras` or `responses`; the first two select
Chat Completions history encoding, and `responses` sends requests to OpenAI's
Responses API instead (ADR 8, amended 2026-10-05). `control` is the main
model with its server's `Provider`, optional `Dialect`, `URL` and
`Credential` filled in, and `Server` naming that server. Its
`KeepReasoning` is the model's `keep_reasoning`, `false` unless config.toml
sets it (ADR 9, amended 2026-10-07); `settings` does not carry it, so
`settings.model.write` keeps an entry's own and a model given another id or
server starts without it. Set a dialect in
config.toml; `settings.control.write` preserves it when selecting a saved
server. It keeps every other
line of `config.toml`, validates the result as the loader would, writes it
atomically (0600) under the lock sessions take when they save the same file,
and applies it to sessions opened afterwards. With
`server`, `[control]` names that server (as `config.toml` has it now, so one
added by hand is found) and the model. A key is saved with its server by
`settings.provider.write`, which replaces it in place when the server has
one, so no old key is left bound for the host. Every method that writes
`config.toml` answers a lock it cannot take or a file it cannot write with
-32000, and a value it refuses with -32602. Passphrases, keys and client secrets are
`writeOnly` in the schema and never echoed. `docs` in the settings is `{google, google_upload}`:
whether a Google credential class is configured, and the upload setting as
`config.toml` has it now (`""` until the first upload or `settings.docs.write`
sets it). `settings.docs.write` keeps every other line of the file, takes the
lock sessions take when they save the same answer, and writes it 0600.

`values` in the settings maps each setting `settings.write` may change, by
dotted name, to `{value, default, set}`: the value tasks opened now get
(`null` for an optional one left unset, such as `judge.price_input`), its
default, and whether `config.toml` sets it. `judge` says whether a judge can
answer confirms now: `judge.server` names a provider, `judge.content` is not
`none` and `judge_key` is `ready` (ADR 46). `judge_key` is what the keychain
and the binding table say of the key of the judge's server, as a
provider's `key` says of its own: `ready` (stored, unlocked and allowed for
the judge's host), or `needs_key`, `locked` or `unbound`, with
`judge_key_detail` saying what to do; absent with no judge. Nothing is asked
of the judge's service. `judge_host` is that server's host, `judge_model`
the model the judge asks (`judge.model`, else its provider's: `jev-latest`
for Jev, `gpt-6-luna` for OpenAI), and `judge_published` what its provider
publishes as the price of its questions, `{price_input, price_output}`,
which prices its requests when `[judge]` sets no prices; absent when nodwyn
does not know it (ADR 41, amended 2026-10-05). `judge_key` is `plan` for a
ChatGPT plan's sign-in, which no judge question reaches. `judges` are the
judges a task may ask (ADR 47), for the pickers: each Jev server and each
OpenAI server at `api.openai.com` with a credential, and the server
`[judge]` names wherever it is, as `{name, provider, host, model,
provider_model, price_input?, price_output?, state, detail?, default?}`,
`state` being `ready`, `needs_key`, `locked`, `unbound` or `plan`.
`settings.write` takes `set`, from those names to new
values; `null` removes a key so its default applies. It accepts only
`approval.writes` and `approval.commands` (what a task does without asking,
ADR 35), `approval.dont_ask` (`true` or `false`: whether tasks offer "don't
ask"; saving `false` also turns it off in every open session, ADR 17), the `budget.*` limits, `loop.request_timeout`
and `loop.shell_timeout`, the `window.*` marks, `judge.server` (one of the
configured providers, a `jev` server or an `openai` one with a key, never a
ChatGPT plan's sign-in),
`judge.content`, `judge.approve_threshold`, `judge.timeout`, `judge.model`,
the judge prices and `mcp.runtime`,
and `policy.allow_insecure_http` (boolean, default false). The latter permits
credential injection on remote HTTP, taking effect in new/resumed tasks and in
current provider setup, tests and model lists. It does not relax bindings, redirect
refusals, certificate checks or separate endpoint validators. No other `[policy]`
entry, address, credential or egress proxy is accepted by `settings.write`. The
changes are made together and only their result is validated, as the
loader would (so a soft limit can be raised past the old hard one with
it); every other line of the file is kept, the lock sessions take is held,
and the file is written 0600. They apply to tasks opened afterwards: an
open task keeps its own, and `session.info` reports the budgets it runs
with.

### Credentials (ADR 5, ADR 24)

| Method | Params | Result |
|---|---|---|
| `credentials.list` | `{}` | `{keychain, credentials, missing, authorizations, providers, bindings_file}` |
| `credentials.add` | `{"class","value","dest"?,"methods"?,"position"?}` | The list; stores a static key, and with `dest` one exact binding row per method |
| `credentials.replace` | `{"id","value"}` | The list; a static key's new value, same id and class |
| `credentials.bind` | `{"id","dest","methods","position"}` | The list; one exact binding row per method, with an OAuth2 credential's granted scopes |
| `credentials.unbind` | `{"id","dest","positions","scopes"?}` | The list; takes back the binding row the list shows for that host, positions and scopes, whatever its methods, leaving every other byte of `bindings.toml`; refused, with the file unchanged, for a row it cannot cut out of the text (edit the file) |
| `credentials.remove` | `{"id","revoke"?}` | `{revoked, list}`; `revoke` asks an OAuth2 provider to revoke the tokens first; the class's rows leave `bindings.toml` with it, unless another credential still has the class |
| `credentials.authorize` | `{"id"}`, or `{"provider","client_id","class"?,"client_secret"?,"scopes"?,"auth_url"?,"token_url"?,"revoke_url"?}` | `{id, class, provider, url, state:"waiting"}` |

Each entry of `credentials` is `{id, class, kind, created, used_by,
bindings}`, and for OAuth2 `provider`, `scopes`, `expires`, `refreshable`,
`needs_reauth` and `token_host`; `shadowed` marks a credential a lookup by
class never reaches, since an earlier one has the class. `used_by` names what
is configured to send the class (the main model, `model NAME`, `server
NAME`, `MCP server NAME`, Google Docs, the judge: the key of the server
`[judge]` names), `model` marks a class a configured provider sends, and
`bindings` are the rows of `bindings.toml` that name it, read as the file
is now. `missing` lists the classes a model, an MCP server or the judge is
configured to send that the keychain does not hold. No result carries a value or a token. A class is letters,
digits, dots, dashes or underscores (at most 64), and adding a second
credential of a class is refused.

`credentials.authorize` starts Authorization Code with PKCE (ADR 24 §2) and
answers as soon as the provider's consent page exists: `url` is for the
person's browser, on the daemon's computer, since the provider sends it back
to a listener on the daemon's loopback. The authorization then waits up to
five minutes; `credentials.list` reports it as `waiting`, `done` (with the
new or re-authorized `credential` id) or `failed` (with `error`) for ten
minutes after. With `id` it authorizes that OAuth2 credential again, with
its client, endpoints and scopes. At most four wait at once.

`hello` accepts an optional `via` (for example `web:phone`), once per
connection, matching `^[a-z][a-z0-9]*:[a-z0-9][a-z0-9._-]{0,31}$`. It is
appended to the kernel identity — `unix:uid=501,gid=20,pid=4242 via web:phone`
— in `peer` and in every recorded actor. It is a claim by a same-user process,
not an authentication; the browser bridge makes it only for a device paired by
name. With `via`, an optional `passkey` (a device name, such as `laptop`)
says the device's person signed in with that passkey (ADR 30, amended
2026-10-17): the actor reads `… via web:tablet passkey:laptop`, and the
daemon refuses that connection nothing it refuses a paired device, since a
client that claims nothing at all may already call every method. Below, "a
paired device" is an actor relayed `via` without `passkey`. The bridge asks
the person's device for a touch of its passkey before relaying a setup
method, and refuses one before it with code -32030.

## Workspaces, file history, attachments and MCP servers (ADR 22, 23, 30)

| Method | Params | Result |
|---|---|---|
| `workspace.list` | `{}` | Named workspaces: `[{name, path, mounts, git, earlier_tasks}]`; `earlier_tasks` is whether the model may search the folder's earlier tasks (ADR 50) |
| `workspace.forgotten` | `{}` | `[absolute paths]`: the folders `workspace.remove` forgot, which the console leaves out of its suggestions, not out of task history. A paired device may read it. An older daemon lacks the method, and the console then hides nothing |
| `workspace.active` | `{}` reads, `{"path"}` records | `{"path"}`: the folder the console last picked for a new task, which a window or tab that has not picked starts in; kept in `last-workspace.toml` beside `workspaces.toml`, since the app's window has a console on a new port every launch and the browser remembers nothing across a restart |
| `workspace.save` | `{"path","name","mounts"}` (all required; `mounts` may be `[]`), and optionally `"earlier_tasks"` | The saved workspace. The name, the mounts and whether the model may search the folder's earlier tasks go to `workspaces.toml` in the config directory; nothing is written into the folder. Without `earlier_tasks` the saved choice is kept, so a client that does not send it never turns it off |
| `workspace.remove` | `{"path"}` | `{}`; forgets a named or unnamed absolute folder, its name and mounts, never its files or tasks; persists a suggestion-exclusion marker and clears a matching last pick. Repeated removal is harmless. `workspace.save`, or a task started or resumed in the folder (by any client, a paired device or a terminal included), clears the marker |
| `versions.list` | `{"repo","path"}` | A file's kept previous versions (outside git); `repo` must be a named workspace or a folder a task has used |
| `versions.diff` | `{"repo","path","hash"}` | `{label,diff}`: read-only unified comparison from a kept version to the current file, not a recorded write preview. Requires a recognised workspace/task root, a path inside it and a kept hash belonging to that file. Rejects path/link escapes and denylisted paths. A missing current file is a deletion; identical content has headers only. Inputs above 128 KiB each or the unified-diff line limit return a labelled `(diff too large to show)` summary. May be read by paired devices; does not restore or require an idle task. Worktree versions use the parent repository's store. |
| `session.restore` | `{"id","path","hash"}` | `{operation_id}`; writes a kept version back through the ordinary approval, keeping the current content as a version too |
| `session.attach` | `{"id","path","name"?}` | The attachment's receipt; `path` is absolute (the web console stages uploads itself). An image returns once its security confirm is answered (see below) |
| `mcp.list` | `{}` | The configured MCP servers and their state |
| `mcp.inspect` | `{"name"}` | `{tools, manifest, approved, changed}`: the server's live tools and whether they differ from what was approved; a contained server is started for it and removed |
| `mcp.save` | `{"server", "api_key"?}` | The saved server; a key goes to the keychain with binding rows: for a remote server one, its host, POST, its position; for a contained one, one per `network` host at its `env:NAME` position, method `*` |
| `mcp.approve` | `{"name","manifest"}` | The server with its manifest pinned; refused if the live manifest changed since it was inspected |
| `mcp.remove` | `{"name"}` | `{}` |
| `mcp.runtime` | `{}` | `{runtime, detail?, egress?, images}`: the container runtime (`podman`, `docker`, or `""` with why in `detail`), why a server with a network list cannot run (`egress`, when `nodwyn-egress` is missing), and per contained server `present`, `missing` or why the runtime could not say |
| `mcp.pull` | `{"name"}` | `{image, present}`: downloads a contained server's image by its digest; nothing else downloads one |

A server in `mcp.save` is remote (`url`) or contained (`transport:
"container"`, `image` pinned by digest, `network`, `mounts`, optional
`command` and `args`, `position: "env:NAME"` with a key). An entry for the
same image, entrypoint and arguments (the same `url` for a remote one) keeps
its approved manifest; any other change needs approval again.

`settings.control.write`, `settings.provider.write`, `settings.provider.remove`,
`settings.model.write`, `settings.model.remove`, `models.test`,
`settings.docs.write`, `settings.write`, `keychain.create`,
`keychain.unlock`, `keychain.passphrase`, `session.binding.create`, `session.policy.write`, `session.model.set`, `session.judge.set`, `session.limits.set`, `workspace.save`,
`workspace.active`, `workspace.remove`, `index.forget`, the `mcp.inspect`, `mcp.save`, `mcp.approve`,
`mcp.remove` and `mcp.pull` methods, and every `credentials.*` method but
`credentials.list` are refused to a paired remote device in
the web console (browser.md); `mcp.runtime` and `credentials.list` only read. The console never
offers `session.eval`, and its bridge relays no run method (ADR 35). The
daemon also refuses `run.start` and `run.resume` with `auto_approve`,
`judge` or `unattended` to an actor that carries `via` without `passkey`, as
the bridge's actor for a paired device does. A device signed in with a
passkey may call all of them, the setup methods after a touch of its passkey;
`session.eval` and the run methods stay out of every browser's reach.

An MCP server is remote (its `url` HTTPS, or plain HTTP to this machine
only) or contained (ADR 23 W3b): the daemon runs it in podman or docker from
an image pinned by digest, speaking MCP over the container's standard input
and output, with no network or only the hosts its egress proxy lets
through. The daemon starts no MCP server as a process of its own.

An image (PNG, JPEG, GIF or WebP by content; ADR 22, amended 2026-10-01) is
attached only after the person says yes to its own confirm, which reaches
the session's subscribers as an ordinary `prompt.confirm` with `Security`
set, `Prim` `"attach image"`, `Args` the file name, and reasons that name the
control model's host and say the pixels cannot be redacted. Answer it with
`session.answer` (`y` or `n`; no standing answer applies) from a connection
other than the one waiting in `session.attach`, since a connection's calls
run in order; the daemon serves other methods meanwhile. A daemon session
that answers confirms itself (a run's `auto_approve` or `judge`) or that
nobody answers (`unattended`) declines it, and so
does `session.abort`, or ten minutes without an answer; `session.attach`
then fails and the log has a `note`. While it waits, `session.send` is
refused. The receipt of an image is its `attach` record: `type` `image`,
`media_type`, `bytes`, `sha256`, `width`, `height`, `tokens` (the window's
estimate), `destination` (the only host its requests go to) and
`approved_by`; never the pixels. A request that did not carry an image
records why in `llm.req` `withheld_images`.

## Generated contract

[protocol.schema.json](protocol.schema.json) is generated from the actual Go
request, result and event payload types. It uses
[JSON Schema 2020-12](https://json-schema.org/draft/2020-12/json-schema-validation).
`$defs/request.METHOD` and `$defs/result.METHOD` describe per-method shapes;
`$defs/sessionEvent` describes live notifications and `$defs/payload.KIND`
describes event payloads. Request parameters are exact; response fields may
grow additively. Secret input is annotated `writeOnly`. Runtime checks still
own prompt validity, policy, cursor ranges, limits and session ownership.

Regenerate with `go generate ./internal/frontend/rpc` (or
`go run ./cmd/nodwyn-schema -out docs/protocol.schema.json`). The normal Go test
suite checks generated-file drift, method coverage and schema references, so
Gitea's per-commit acceptance job enforces the contract.

## Ownership

`internal/frontend/rpc/socket.go` owns endpoint setup; `server.go` owns
connections and shutdown; `protocol.go` decodes envelopes and preserves reply
IDs; `sessions.go` serializes lifecycle operations and owns the session map.
`peer.go` owns each bounded output queue and its sole socket writer. `events.go`
owns live subscriptions, with a lock independent of lifecycle operations so
closing a session can keep draining events. Prompt state is not duplicated in
RPC: `core.Session.PendingPrompts` supplies the redacted display snapshot.

Core remains unaware of sockets. `core.Store.ReadLog` supplies validated,
redacted pages of durable history. `Session.Close` waits for turn goroutines,
including final event delivery, before closing the channel and writer.

`identity*.go` reads platform peer credentials; `approvals.go` passes attributed
replies to the core facade. It owns neither pending prompts nor policy changes.
`schema.go` derives documentation from wire types. U2's local Unix transport is
implemented; its clients are the TUI (P9), the browser console and app window
(U3), and `nodwyn run` (U4). Optional TCP remains future work.


## Persistent binding approval

`session.binding.create` accepts only session and pending-prompt IDs, never row
fields or an actor. E3's optional `Prompt.Binding` contains a complete exact
proposal corresponding to `Prompt.Egress`; the core removes proposals that are
not eligible or whose display would be redacted. The method rejects checkpoint,
ordinary non-egress, security, canceled and resolved prompts. It is separate
from `session.approval.create` and does not extend that method's accepted scopes.

The response acknowledges the choice, not completion of persistence. Follow
`binding.change` events (`requested`, then `applied` or `failed`) and the guard
outcome. Each change carries the kernel-derived actor, prompt ID and proposed
row. Persistence failure blocks the waiting call. Disconnect/reconnect never
retries a grant automatically; durable history and pending prompts identify its
outcome. This method and the optional proposal field are additive extensions.


## Recorded window recipes

New `session.start` / `session.resume` payloads may include `context`, the
redacted window recipe for that runtime. New turn `llm.req` payloads include
`request_hash`, covering the provider-neutral request. These additive fields
support offline `nodwyn replay SESSION --window SEQ`; no new RPC operation is
required. Old records remain readable but cannot provide verified exact request
reconstruction. The generated schema documents the fields.


Summary records now have typed `from`, `through`, `source_hash` and `text`
payloads. `summary.drop` names `summary_seq`. These are durable history, not
permission or mutation RPCs. The first editing surface is the local `nodwyn
summary` CLI on a closed session; daemon-owned logs retain exclusive ownership.
Recipes from `window-v2` on are summary-aware; new runtimes record the
current one (`window.RecipeVersion`, memory.md), and exact replay supports
every recipe from `window-v1` to it. Version 16
omits the matching tool results when their assistant call batch is elided,
and protects the calls while any result is recent. `llm.req.elided` includes
those omitted results. Version 17 cuts a result too large to show at line
ends and names the lines it leaves out (`[... lines A-B of the file left
out here, …: read them with fs-read, start A, count N ...]`, or `of this
result` for other tools). Earlier recipes retain their exact historical
shape, even where a trim left orphaned tool messages; a resumed runtime uses
the current recipe. Version 18 gives a reply's signed reasoning back with it: `llm.resp`
has `blocks`, each `{type, thinking?, signature?, data?, before?, origin?}`
as an Anthropic reply gave it, or as a Responses reply's encrypted reasoning
(`type` `reasoning`, `data` the encrypted content) for a model with
`keep_reasoning`, whose `origin` names the server and model it goes back to
(`before` is how many of its calls came first), or as a chat-completions
reply's `reasoning_details` (`type` `reasoning_details`, `data` the whole
array as the JSON text it came as, with its `origin`; ADR 9, amended
2026-10-15), and the window leaves out the blocks of a reply made before a
fold or an elision of an earlier entry. A client shows none of them. The
recorded `max_result_tokens` is the window's cap as sized for the model: a
48th of the high water when the window grows with a large context, up to
16,384, unless `config.toml` sets it.


### Prompt scope metadata

`prompt.confirm` may include `Options` alongside `ID` and `Prompt`. Its `session`,
`target`, `kind`, `turn`, and `binding` fields describe the wider choices derived
from that pending call. This additive metadata is a display aid, never a grant.
The core recalculates eligibility before accepting an explicit approval or binding
request. Browser clients connected to older daemons without `Options` offer only
one-time answers. Protected/security confirmations do not gain wider scopes.


### Fixed guarded inspections and policy editing

`session.friction` accepts `{id}` and reads only `docs/friction.md` through the
session guard. `session.policy.read` accepts `{id, name}` where name is
`bindings` or `approvals`; `session.policy.write` adds `revision` and `content`.
All return `{operation_id}` immediately so the same client can answer a prompt.
Unknown fields, caller-supplied paths and actors are rejected. Transport identity
attributes requests and replies. These operations require an idle open session.

The matching ephemeral `inspection.result` or `policy.result` carries its `ID`,
`Name` and `Err`; friction adds `Text`, policy adds `Content`, `Revision`, `Exists`
and `Saved`. Results may arrive before the RPC acknowledgement; correlate by ID.
The ordinary evaluation/guard log records provide durable audit history. A
reconnect does not retry an operation or restore an editor draft.

Policy revisions identify bytes and distinguish absence from an empty file;
they grant no authority. Writes validate the fixed file's TOML, then require a
one-time guard confirmation and compare the revision under the shared file lock.
A stale revision requires reading again and reviewing a new change. Binding
updates apply before egress; approval-file updates apply to new/resumed sessions.
Both browser policy methods and friction are disabled by `--read-only`.


### Watching session attention

`session.watch` takes `{}` and returns an array of current open-session states.
Until disconnect it also receives `session.event` notifications with kind
`session.state`, sequence zero, and the same state object as payload:
`{id, revision, open, busy, needs_answer}`. The feed covers sessions subsequently
opened or closed by other clients. It carries no prompt or transcript contents
and creates no authority. Ordinary `session.subscribe` remains how a client
loads a selected session's history and pending prompts.

Apply a state only if its revision exceeds the last revision seen for that ID;
notifications can precede the snapshot response. Revisions are daemon-local. Discard client revision tracking on reconnect; a
restarted daemon has a new revision sequence. Closed states clear attention and are omitted from later
snapshots. Delivery uses the existing bounded peer queue; slow clients disconnect
and obtain a new snapshot rather than receiving an unbounded backlog.

## Supervised and unattended run methods

- `run.start {repo, task, wall, max_turns, auto_approve, unattended?, judge?, cost?}` creates a new daemon-owned session
  and returns `{id, state}`. `wall` is a positive duration; `max_turns` is 1–10000;
  `auto_approve` (required, true or false) answers the run's ordinary tool
  confirms, as `--yolo` does. `unattended` (optional) makes a run nobody
  answers: tool confirms are declined unless `auto_approve`, soft budget
  checkpoints go on to the hard limit, and the run goes on after a turn only
  when that turn marked a plan step done ([runs](runs.md)). `judge` (optional,
  excludes `auto_approve`) has the judge answer ordinary tool confirms; it
  needs a configured judge. `cost` (optional, US dollars) is the run's spend
  limit, replacing `budget.cost_hard`; it is kept on resume, and the turn that
  reaches it ends with outcome `spend_limit` and the run `limited`. `state`
  reports the flags. A guard decision
  answered by the judge has actor `judge`; one declined because nobody was
  there to ask, actor `unattended`.
- `run.info {id}` returns task, original deadline, maximum and consumed turns,
  status and reason, and for a run this daemon drives, how it was started
  (`unattended`, `auto_approve`, `judge`). A saved formerly running run is
  reported paused.
- `run.stop {id}` cancels the driver and its active turn or checkpoint.
- `run.resume {id, auto_approve?, unattended?, judge?}` explicitly reopens a stopped run at a recorded turn boundary,
  preserving its original deadline and turn count. No transient grants survive,
  and the flags are never read back from the log: omitted, the run resumes
  supervised.

Run methods are for `nodwyn run`, a batch tool for scripts and benchmarks
(ADR 35), over local RPC. The browser starts every task as a chat
(`session.open`, `session.send`); it shows a run started elsewhere, answers
its prompts and stops it (`session.abort`). A run whose requests nobody
answers by hand
(`auto_approve`, `judge` or `unattended`) cannot be started or resumed by an
actor relayed `via` a paired device. While a
run is active, session `busy` includes the between-turn wait. `session.send`,
`session.eval`, `session.friction`, `session.policy.read`/`write`,
`session.notes.write`, `session.restore` and `session.attach` are rejected
until it stops. New `run.state`,
`run.turn` and `run.checkpoint` records and optional `turn.end.outcome/error`
fields are described by the generated schema. A `turn.end` that is not
`completed` or `canceled` also carries `where`, a line saying where the turn
had got to: the plan's marks and next step, the files the file tools wrote,
the last `shell-exec` and how it ended (read from the log; nothing is run). An unattended run's
`run.checkpoint` has actor `unattended` and a `reason`, and `run.state`
records `unattended`, `auto_approve`, `judge` and a run's own `cost`.
`turn.end.outcome` is `completed`, `stopped`, `hard_budget`, `spend_limit`
(the task's spend limit; no turn follows it), `canceled` or `error`. A
budget checkpoint on spend has dimension `cost`, its amounts in millionths
of a dollar; a reply's absolute soft limit is in dollars (`2.50`). See [runs](runs.md) for limits,
completion-marker semantics and recovery constraints.

### OpenAI plan OAuth2 profile

`credentials.list` includes `openai` in its provider profiles. A local
`credentials.authorize` request with `provider: "openai"` may omit
`client_id`: the browser consent registers nodwyn dynamically. It takes no
client secret. The returned authorization URL contains state, nonce, PKCE,
a stable host identifier and the requested ChatGPT plan-use scopes.
`id` reauthorizes a saved registration without changing its client or account
identity; failed validation preserves the stored credential. No token or
verified subject is included in RPC results. The existing binding and model
configuration flows select its class; use the public OpenAI URL and the
`responses` dialect. Model probes refresh OAuth2 credentials after checking
their binding and granted scopes, just as task requests do.

### Project identity and startup trust (ADR 45)

Returned project paths use canonical absolute identities: existing symlinks are
resolved and absent suffixes use their resolved ancestor. Existing session logs
and kept-version indexes remain readable through former aliases. A workspace with
two registered aliases is refused for reconciliation, not merged with broader
mount authority.

Repository init and approvals load only when `trusted-projects.json` in the user
configuration directory grants their current combined revision. Otherwise ordinary
startup skips both and emits a note; reload refuses. The CLI `nodwyn trust` owns
the explicit review/accept/revoke flow; no RPC method grants this trust. Policy
editor saves do not grant it implicitly. A running session keeps its already loaded
code and grants until restarted. The optimiser keeps a changed init pending review.

## Human conversation search and focused history (ADR 51)

`conversation.search {id,query,scope?,limit?}` is read-only, including on paired
and read-only browser connections. `scope` is `conversation` (default, this task)
or `earlier` (other tasks of this task’s recorded project). Archived tasks are
included; worktree sessions use their recorded parent repository. No project
path parameter is accepted. `query` needs searchable words, at most 4096 bytes;
`limit` is 1..20, default 20, matching events rather than turns or tasks.

This is a person reading the same logs the memory CLI reads. The model’s
`earlier_tasks` setting is neither consulted nor changed; no task is resumed,
model called or permission granted. Search may refresh derived `.mem` files.
It uses the existing conversation event selection and BM25 ranking: messages,
replies, plans, attachments, summaries and tool results, including elided history;
thinking, guard/configuration records and searches’ own output remain excluded.

The result is `{tasks,searched_tasks,searched_events,unavailable}`. Nonzero
`unavailable` means source logs or matched records could not be read, so results
may be incomplete. Tasks are ordered by their best matching turn; each has
`{id,title,archived,turns}`. Turns are ordered by their best event and have
`{heading,matches}`. Matches are in sequence order within each turn and have
`{seq,kind,time,heading,lines:[{number,text}]}`. Snippets select at most three
lines, each at most 200 Unicode characters plus boundary ellipses, centred near
a match on long lines. Source payloads and display fields are redacted before
clipping. The result groups and snippets come from the same word index and
turn cards as index-search; no semantic model is introduced.

`log.context {id,seq}` is another read-only method. The sequence must name an
available event. It returns `{from,through,run,turn?,page}`. `run` and optional
`turn` are indexed `{seq,ts}` marks; from/through fix an inclusive range inside
both the turn and run. Events before the first turn use their conversation
range. `page` is an ordinary redacted LogPage, initially at most 40 records on
either side of seq plus the selected record, clipped to that range. Its own
through is the initial neighbourhood’s end, not the full context range’s end.

To read more, use `log.read` within the outer from/through range. The console
reads at most 500 records per action on either side, keeping that range fixed
as a live log grows. It renders history separately, highlights the selected
event and offers Open task to return to live interaction. Historical settings,
receipts and prompt-related records must never change live state. Finding the
range and its initial page reads index offsets and the necessary suffix, without
reading old run previews or replaying the whole log.

## Scheduled tasks (ADR 49)

- A definition is `{message,rule,cost,paused}`: `cost` is dollars per run, 0
  for none. `rule` says when, in the daemon's local civil time:
  - `{once:"YYYY-MM-DDTHH:MM"}` runs once, and has nothing else;
  - otherwise the times of a day are `at` (up to 48 `"HH:MM"`) or `every`
    minutes (5 to 1440), counted from `between[0]` (midnight without it)
    through `between[1]` inclusive, which must end after it starts on the
    same day;
  - the days are every day, the weekdays in `days` (`mon` … `sun`), the
    `dates` of each month (1–31; a month without one has no run), or the
    `nth` weekday of the month (1–5, -1 the last) of the one weekday in
    `days`; `days` and `dates` do not combine;
  - `from` is the first minute it may run (the daemon fills in now when it
    is missing) and `until` its last day (`YYYY-MM-DD`), optional.
  The daemon keeps a rule in its normal form (days in the week's order,
  times and dates sorted, each once). A definition from before rules has
  `{repeat,at}` instead (`once`, `hourly`, `daily`, `weekdays` or `weekly`
  from a local minute); it still validates and runs at the same times, read
  as a rule (hourly is every 60 minutes between the minute's first and last
  hour of each day, weekly the first time's weekday). A definition has a
  rule or a repeat, not both.
- `schedule.next {message,rule,cost,paused}` (or `{…,repeat,at,…}`) is
  read-only. It validates the definition and returns its normal `rule`,
  `sentence` (the rule in words, such as "Every 30 minutes from 09:00 to
  18:00 on weekdays" or "Tuesdays and Thursdays at 15:00", with ", starting
  …" when the first day is after today and ", until …"), up to five `next`
  ISO times with offsets, `next_words` (the same times as labels such as
  "Tue 20 Oct 15:00", the year added when it is not this one) and
  `timezone`, the zone's name (TZ, or where `/etc/localtime` points; never
  "Local"). Missing summer-time minutes advance to the next valid minute,
  several becoming one; repeated minutes use their first occurrence.
- `schedule.read {id}` returns `definition` when present, its log `sequence`,
  automatic `cursor`, optional `last_run` and `last_skip`, plus `rule`,
  `sentence`, `next`, `next_words`, `timezone`, `archived`, `scheduler`
  (whether this daemon owns the lock) and `history`: the latest twenty
  scheduled times, newest first, each with `seq` (its `schedule.run` or
  `schedule.skip`), `due`, `started`, `missed`, `manual`, `skipped` (the
  reason, for a time not run), `outcome` (the run's turn's `turn.end`
  outcome, empty until it ends), `cost` (its known requests' cost, a
  person's follow-ups included), `when` (a label of its start) and `what`
  ("Finished", "Failed", "Stopped at the spend limit", "Running", "Did not
  finish", "Skipped: …", with "(Run now)" or "(late, standing for N missed
  times)"). `session.list` includes the same `schedule` view for scheduled
  tasks.
- `schedule.set {id,message,rule,cost,paused}` (or `repeat` and `at`) writes
  `session.schedule`, with `words`, the sentence when it was written.
  Defaults are cost 0 and paused false; the definition must validate.
  `{id,remove:true}` removes the schedule while retaining its chat. A
  definition whose rule is the same as before (in normal form) preserves the
  consumed cursor; a new timing starts after now, so times before the change
  are not run late. Resuming a paused definition sets its cursor to now.
- `schedule.pause {id,paused}` preserves its definition and records the change.
  Archive records a paused definition before archiving; Unarchive leaves it
  paused. A closed scheduled task can still run automatically.
- `schedule.run {id}` starts one normal turn immediately, including while
  paused, without consuming an automatic time. Archived tasks refuse it.
  Busy tasks, pending answers, resume conflicts and inaccessible models
  refuse dispatch and record a skip when the log can be written.

Set (including removal) and resuming through pause are local-only, checked in
both daemon and bridge. Paired devices may pause and run now; read-only
bridges expose only read and next. No Scheme primitive creates schedules.
The ordinary task-opening methods retain all folder/worktree/model/helper/
judge/limits choices; the console then calls set without sending a message.

The daemon holds `scheduler.lock` in its data directory and checks every 30
seconds and at startup. Due occurrences after the cursor through now become
one late run; `missed` counts the additional occurrences. A busy task, pending
answer or another log owner skips that interval. External log locks defer the
skip in memory until released; daemon shutdown before release loses that
unrecorded skip, so restart treats it as missed. No authoritative schedules
file or repository configuration is read. The index is only a derived view.

Log payloads (see the generated schema for full shapes):

- `session.schedule`: message/rule (or repeat/at)/cost/paused, actor,
  removed, after (preserved consumed cursor, or now on resume or a new
  timing), words (the rule's sentence when written).
- `schedule.run`: due, through (latest coalesced occurrence), started, missed,
  manual, actor, cost, spend_before (lifetime model/judge cost before this run).
  Manual runs have a zero through and do not move the automatic cursor.
- `schedule.skip`: due, through, started, missed, reason. A refused manual
  dispatch has zero through and leaves automatic progress alone.
- `context.handover`: text, from and through (the first and last events of the exchange that wrote the note, which later windows and the last answer leave out; an older log has no from).
  A successful turn with over a quarter-window since the last note and no
  pending answer prepares it with compact-note under the same budget. The
  next scheduled turn applies a lasting context.narrow and harness message.

Scheduled messages discard Don’t ask and refuse unattended/yolo session
options. Their spend limit resets at schedule.run and includes human
follow-ups. `session.info` adds optional `run_spend`; budgets.cost reports the
current run’s limit. Whole-task session.totals remains unchanged. Scheduled
sessions close after 15 idle minutes without pending prompts, freeing the
same resources as session.close.

`schedule.read` and `session.list`'s schedule view add `held` when the next
run waits for the keychain: the task is closed, the keychain is locked and
the default model needs a key from it. A time that is due then is neither run
nor recorded as skipped; the first tick after an unlock runs it once, late,
standing for every time it held (ADR 49, amended 2026-10-17).

## Starting at login and desktop notices (ADR 53 §11)

- `startup.read {}` returns the login item: `offered` (false for a daemon
  given none, as a test's), `supported` (Linux and macOS), `on`, `path` (the
  unit or agent file), `other` (it runs another nodwyn), `program` (the nodwyn
  it would run) and `why` it cannot be turned on (a `go run` build).
- `startup.set {on}` writes or removes the login item and returns the same.
  Writing replaces one nodwyn wrote; a file at that place nodwyn did not
  write is refused, either way. It starts nothing now: the daemon starts at
  the next login.

Both are this computer's: the daemon and the bridge refuse them to a paired
device. With the setting `notify.desktop` on (`settings.write`, off by
default), a daemon given a notifier shows a desktop notice, with fixed
words, when an open session's state turns to `needs_answer` while no client
of this computer (one that did not hello with `via`) is in `session.watch`,
and once when a locked keychain makes a scheduled run skip, until the
keychain is unlocked.
