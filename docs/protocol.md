# Local session protocol (U2, turns and events)

Start the daemon in the foreground with `nibb daemon`. It is the `nibbd` role
from ADR 21, shipped in the existing binary. `nibb daemon --no-keychain` skips
keychain startup; otherwise it uses the same masked terminal unlock as chat.
Configuration is loaded once at startup. No model connection is needed to list
or open sessions. `SIGINT` and `SIGTERM` close clients and session writers.

The endpoint is `$XDG_RUNTIME_DIR/nibb/sock`. When `XDG_RUNTIME_DIR` is absent,
it is `<user cache directory>/nibb/run/sock` (on macOS,
`~/Library/Caches/nibb/run/sock`). `--socket /absolute/private/directory/sock`
overrides it. The immediate parent must be owned by the current user, mode
0700, and not a symlink; missing directories are created privately. The socket
is mode 0600. Existing paths are never replaced. After a crash, verify that no
daemon owns the endpoint before removing a stale socket and restarting.

A connection has the authority of the local user. Opening or resuming a
repository loads its current `.nibb/init.scm` and policy, just as the plain
frontend does; initialization confirmations remain denied. Turns use the same guard and budget path as chat. Approval and secret replies,
Scheme evaluation and TCP remain unavailable in this slice. A turn needing a
reply waits at its prompt until aborted or its hard wall deadline expires;
connecting or subscribing never approves it.

## Transport and handshake

Use [JSON-RPC 2.0](https://www.jsonrpc.org/specification), with one complete
JSON value per newline, encoded as UTF-8. A request frame must be smaller than 1 MiB;
an oversized frame closes the connection. Requests use named object parameters
with exact, case-sensitive names. Unknown parameters are errors. Unknown
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

The result contains `protocol`, the binary `version`, and a `methods` list.
Clients must use that list to discover supported operations while U2 is landing
in slices. A different protocol version fails with code `-32001`. Request and
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
tool schemas) and `budgets`. Budgets contain `tool_calls`, `tokens`, `actions`
and `wall_ns`, each with numeric `soft` and `hard` fields. These are the
configured per-turn limits, including the legacy tool-call override; this
slice does not expose live turn counters or approval inspection.

`session.resume` returns the already-open session if this daemon owns it.
Otherwise it uses `core.Resume`, including its exclusive writer lock, recovery
and current initialization. A writer held by another process is an error.
There is no automatic execution or replay of interrupted tools. Closing a
session keeps its log. Disconnecting a client keeps the session open; another
connection can inspect or resume it. After daemon shutdown, the plain frontend
or a new daemon can resume it from the same log.

Protocol errors use the standard JSON-RPC codes. Application errors use
`-32000` for a lifecycle/store failure, `-32001` for a missing/incompatible
handshake, and `-32002` for a session not owned by this daemon. Unsupported
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

## Ownership and remaining U2 work

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

The remaining U2 slice adds approval and secret replies, peer identity in
approval records, explicit standing-approval creation, generated event schemas
and a broader `session.info` view. No future method is registered as a stub.
