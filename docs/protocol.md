# Local session protocol (U2, lifecycle slice)

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
frontend does; initialization confirmations remain denied. This slice exposes
no turn execution, Scheme evaluation, approval, secret-reply or TCP method.

## Transport and handshake

Use [JSON-RPC 2.0](https://www.jsonrpc.org/specification), with one complete
JSON value per newline, encoded as UTF-8. A frame must be smaller than 1 MiB;
an oversized frame closes the connection. Requests use named object parameters
with exact, case-sensitive names. Unknown parameters are errors. Unknown
response fields should be ignored. String, numeric and null IDs are preserved
exactly as JSON values; large numeric IDs do not pass through floating point.
Notifications have no response; batches execute in order and omit notification
responses. Slow response writes time out after ten seconds.

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

## Ownership and remaining U2 work

`internal/frontend/rpc/socket.go` owns endpoint setup; `server.go` owns
connections and shutdown; `protocol.go` decodes envelopes and preserves reply
IDs; `sessions.go` serializes lifecycle operations and owns the session map.
`core` remains unaware of sockets. Initialization events are drained without a
replay buffer; durable records remain in the log.

The next slice adds send/abort, event notifications, `log.read` and reconnect
catch-up. The final U2 slice adds approval and secret replies, peer identity in
approval records, and explicit standing-approval creation. Generated full event
schemas and the broader `session.info` view belong to those slices. No future
method is registered as a stub in this version.
