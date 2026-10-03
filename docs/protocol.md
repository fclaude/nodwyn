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
is mode 0600. Existing paths are never replaced. After a crash, verify that no
daemon owns the endpoint before removing a stale socket and restarting;
`nodwyn app` does this itself, removing a socket nothing answers on.

A connection has the authority of the local user. Opening or resuming a
repository loads its current `.nodwyn/init.scm` and policy, just as the plain
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

## Lifecycle methods

| Method | Named parameters | Result |
|---|---|---|
| `session.list` | `{}` or omitted | Stored sessions, in ID order |
| `session.open` | `{"repo":"/absolute/repository"}` | Open-session details |
| `session.resume` | `{"id":"SESSION_ID"}` | Open-session details |
| `session.info` | `{"id":"SESSION_ID"}` | Details of a session owned by this daemon |
| `session.close` | `{"id":"SESSION_ID"}` | `{}` after releasing the session writer |

List entries contain `id`, `repo`, `open`, `busy`, `first_message`, `last_event`
(RFC 3339), `in_flight` and `truncated`. `open` means this daemon owns the
session. `in_flight` describes durable history, not process liveness; a session
owned by a different process still appears with `open: false`.

Open-session details contain `id`, `repo`, `busy`, `tools` (the existing model
tool schemas) and `budgets`. Budgets contain `tool_calls`, `tokens`, `actions`,
`wall_ns` and `cost`, each with numeric `soft` and `hard` fields. `cost` is
the task's spend limit in millionths of a US dollar, counted over the whole
task rather than a turn; a `hard` of 0 is none. These are the
configured limits, including the legacy tool-call override; this
protocol does not expose live turn counters. Details also include
`allowed_commands`, `allowed_writes`, `turn_writes`, optional `turn_actor`,
`approve_all` ("don't ask" is on) and optional `approve_actor` (who turned it
on).
Session details also contain `bindings`, a copied array of loaded metadata rows
with `Class`, `Dest`, `Positions`, `Methods` and, when a row names them,
`Scopes` (OAuth2 scopes the row allows, ADR 24) fields. No credential values are
included; configuration edits reload before egress. Inspection reports the
last validated snapshot; invalid reloads deny egress. Older daemons may
omit this additive field, which clients must distinguish from an empty array.
Details also contain `workspace` (the folder's kind and mounts, from
`.nodwyn/workspace.toml`), `git` (whether the root is a git work tree) and
`mcp` (each MCP server offered in this workspace, with whether its tools are
offered or why they are withheld: `state` is `ready`, `unapproved`,
`changed`, `unavailable` or `error`; a contained server has `transport`
`container`, `host` `mcp:NAME` and its `network` hosts).
The approval fields report current effective prefix lists and turn-wide write approval;
file-backed and session-only prefixes share the lists. They are not setters.

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
requires no resume, writer lock or initialization.

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
that file. `scope: "turn"` applies to ordinary writes until the turn ends.
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
refused to an actor relayed `via` a paired device, which may only turn it
off, and to a session whose confirmations are not asked of a person
(`--yolo`, a judge, `--no-confirm`, an unattended run). Each switch is an
`approval.change` with `kind: "all"`, `scope: "session"`, `prefix: "*"` and,
when it is turned off, `off: true`.

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
from the model's configured prices rather than the provider. A budget
checkpoint's `state` carries `cost`, `priced`, `unpriced` and
`cached_tokens` for the turn so far. All are optional and additive.

A `plan.step` event is the model marking step `step` of `of` of the turn's
recorded plan `done` or `skipped`, with its `note`; a later mark of the same
step replaces an earlier one. A step's number indexes the plan's numbered or
bulleted lines, or the whole plan when it is prose. A run's later turn
carries the run's plan: its `gate.plan` has `noul` `carried`, the same
`plan`, and `marks`, the `plan.step` marks of the run's earlier turns, the
latest per step in step order; the turn's own marks come after it, as
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

A `jev.req` event records one judge request: the tool, its origin, the
content level, the redacted payload's size and hash, the redactions, the
answer, and, when an answer came back, the `model` that answered and its
`usage` (`input_tokens`, `output_tokens`, and `cost`, an estimate from
`[jev]` prices; the API reports none). A budget checkpoint's state counts
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
| `session.notes.write` | `{"id":"SESSION_ID","text":"…"}` | `{operation_id}`; a `policy.result` event named `notes` reports the guarded save |
| `models.status` | `{}` | Each configured model: `name`, `provider`, `model`, `host`, `state` (`ready`, `missing`, `unreachable`, `unconfigured`), `detail` |
| `keychain.status` | `{}` | `{exists, unlocked, credentials, no_passphrase?}`; `no_passphrase` is set when the open keychain has none |
| `keychain.create` | `{"passphrase":"…"}` or `{"no_passphrase":true}` | Keychain status; tasks opened afterwards receive the keychain. A passphrase has at least 8 characters |
| `keychain.unlock` | `{"passphrase":"…"}` | Keychain status; tasks opened afterwards receive the keychain. `""` opens a keychain without a passphrase |
| `keychain.passphrase` | `{"current"?, "passphrase"?, "no_passphrase"?}` | Keychain status after setting, changing or (`no_passphrase`) removing the open keychain's passphrase; `current` is required when it has one, and a wrong one waits a second as `keychain.unlock` does |
| `settings.read` | `{}` | `{config_file, control, models, keychain, docs, values, judge}` |
| `settings.control.write` | `{"provider","url","model","api_key"?}` | Settings after replacing `[control]` in `config.toml` |
| `settings.docs.write` | `{"google_upload"}` | Settings after setting `[docs] google_upload` (`ask`, `allow` or `deny`) in `config.toml` |
| `settings.write` | `{"set": {"budget.tokens_hard": 20000000, "budget.cost_hard": null, …}}` | Settings after changing those settings in `config.toml` together |

`session.list` entries also carry `title` (the first sentence of the first
message, shortened), `started`, `changes` (files the guard allowed the session
to write, first 50, from `guard.decision` records, which now carry `path`) and
`run` (the live run, or the last recorded `run.state`). `models.status` sends
no credential: it asks each server for its model list and reports whether it
answered. `settings.control.write` keeps every other line of `config.toml`,
validates the result as the loader would, writes it atomically (0600), and
applies it to sessions opened afterwards. An `api_key` goes to the unlocked
keychain under a fresh class, with one binding row: that host, `POST`,
`header:Authorization:Bearer`. Passphrases, keys and client secrets are
`writeOnly` in the schema and never echoed. `docs` in the settings is `{google, google_upload}`:
whether a Google credential class is configured, and the upload setting as
`config.toml` has it now (`""` until the first upload or `settings.docs.write`
sets it). `settings.docs.write` keeps every other line of the file, takes the
lock sessions take when they save the same answer, and writes it 0600.

`values` in the settings maps each setting `settings.write` may change, by
dotted name, to `{value, default, set}`: the value tasks opened now get
(`null` for an optional one left unset, such as `control.temperature`), its
default, and whether `config.toml` sets it. `judge` says whether a judge can
answer confirms now: `jev.content` is not `none` and a `jev-key` is in the
unlocked keychain. `settings.write` takes `set`, from those names to new
values; `null` removes a key so its default applies. It accepts only
`approval.writes` and `approval.commands` (what a task does without asking,
ADR 35), the `budget.*` limits, `loop.request_timeout`
and `loop.shell_timeout`, the `window.*` marks, the `[control]` model's
tuning (`num_ctx`, `max_output`, `output_field`, `keep_alive`,
`temperature`, `seed`, `vision`, `cache_key` and the `price_*` entries), `jev.content`,
`jev.approve_threshold`, `jev.timeout`, `jev.model` and `mcp.runtime`;
never an address, a credential, the egress proxy or `[policy]`. The
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
| `credentials.remove` | `{"id","revoke"?}` | `{revoked, list}`; `revoke` asks an OAuth2 provider to revoke the tokens first |
| `credentials.authorize` | `{"id"}`, or `{"provider","client_id","class"?,"client_secret"?,"scopes"?,"auth_url"?,"token_url"?,"revoke_url"?}` | `{id, class, provider, url, state:"waiting"}` |

Each entry of `credentials` is `{id, class, kind, created, used_by,
bindings}`, and for OAuth2 `provider`, `scopes`, `expires`, `refreshable`,
`needs_reauth` and `token_host`; `shadowed` marks a credential a lookup by
class never reaches, since an earlier one has the class. `used_by` names what
is configured to send the class (the main model, `model NAME`, `MCP server
NAME`, Google Docs, the judge), and `bindings` are the rows of
`bindings.toml` that name it, read as the file is now. `missing` lists the
classes a model or an MCP server is configured to send that the keychain
does not hold. No result carries a value or a token. A class is letters,
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
name.

## Workspaces, file history, attachments and MCP servers (ADR 22, 23, 30)

| Method | Params | Result |
|---|---|---|
| `workspace.list` | `{}` | Named workspaces: `[{name, path, kind, mounts, git, error?}]` |
| `workspace.save` | `{"path","name","kind","mounts"}` (all required; `mounts` may be `[]`) | The saved workspace. The name goes to `workspaces.toml` in the config directory; a `code` folder with no mounts keeps no `.nodwyn/workspace.toml`, anything else is written there |
| `workspace.remove` | `{"path"}` | `{}`; forgets the name, never the folder |
| `versions.list` | `{"repo","path"}` | A file's kept previous versions (outside git); `repo` must be a named workspace or a folder a task has used |
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

`settings.control.write`, `settings.docs.write`, `settings.write`, `keychain.create`,
`keychain.unlock`, `keychain.passphrase`, `session.binding.create`, `session.policy.write`, `workspace.save`,
`workspace.remove`, the `mcp.inspect`, `mcp.save`, `mcp.approve`,
`mcp.remove` and `mcp.pull` methods, and every `credentials.*` method but
`credentials.list` are refused to a paired remote device in
the web console (browser.md); `mcp.runtime` and `credentials.list` only read. The console never
offers `session.eval`. It starts runs from Home with `run.start`, but a
paired remote device may start or resume only a run whose requests a person
answers: `run.start` and `run.resume` with `auto_approve`, `judge` or
`unattended` are refused to it, by the console and by the daemon (whose
actor for it carries `via`).

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
Recipes from `window-v2` on are summary-aware; new runtimes record `window-v13`,
and exact replay supports every recipe from `window-v1` to it.


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
fields are described by the generated schema. An unattended run's
`run.checkpoint` has actor `unattended` and a `reason`, and `run.state`
records `unattended`, `auto_approve`, `judge` and a run's own `cost`.
`turn.end.outcome` is `completed`, `stopped`, `hard_budget`, `spend_limit`
(the task's spend limit; no turn follows it), `canceled` or `error`. A
budget checkpoint on spend has dimension `cost`, its amounts in millionths
of a dollar; a reply's absolute soft limit is in dollars (`2.50`). See [runs](runs.md) for limits,
completion-marker semantics and recovery constraints.
