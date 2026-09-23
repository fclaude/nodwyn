# Local browser view

The U3 session desk supports conversations and session lifecycle controls. Run a daemon, then attach the
browser bridge in another terminal:

```sh
nibb daemon
nibb ui                         # opens the local browser page
nibb ui --read-only             # disable conversation and lifecycle controls
nibb ui --socket /absolute/private/sock --no-open
```

`nibb ui` connects to an existing daemon. It prints a loopback URL and a random
pairing code; paste that code into the page. The code is never put in a URL or
session log. Keep it in the terminal rather than sharing it. The browser receives
an HttpOnly, SameSite=Strict session cookie; no credential is written to browser
local/session storage. Restarting the bridge creates a new port and code.

The desk shows saved and open sessions, their repository and last activity,
recorded conversations, streamed text, collapsed thinking, tool arguments and
results, guard decisions, and model usage. It reads a fixed log prefix in pages
before applying live events, deduplicating durable sequence numbers. Refresh
reloads that prefix. A reconnect never retries an operation. Browser reload
retains pairing through the session cookie; select a session again to inspect it.
Untrusted text is rendered as literal text, never HTML or executable Markdown.

Create a session with an existing absolute repository directory, resume a saved
session, and send messages from the composer. Abort cancels the current turn or
pending prompt. Close ends the session and preserves its transcript; it is disabled
while known busy, but another client can start work concurrently, so closing can
still interrupt work. Drafts remain separately in memory for each session in this
tab and disappear on page reload. A failed reply may follow successful delivery:
inspect the transcript before sending again. No operation is automatically retried.

`--read-only` retains the original view-only mode. The server filters advertised
methods and enforces this restriction independently of the browser controls.
Neither mode can evaluate Scheme, answer prompts, submit secrets, or create
bindings or approvals; these methods are rejected by the bridge.
Pending confirmations are surfaced with a notice directing the user to the
terminal client. Closing a browser tab or stopping `nibb ui` does not close
sessions in the separately running daemon. Stopping the daemon still closes its
sessions normally. The session list is a snapshot; use Refresh to update it.

The bridge binds only `127.0.0.1` on a random port. Every request checks its exact
Host; pairing and WebSocket upgrades also require the exact browser origin.
Cross-site fetches and framing are refused. Neither the optional general TCP RPC
transport nor a remotely accessible UI is enabled. The existing Unix socket
remains private, and kernel peer identity still identifies the bridge process.

Implementation is deliberately small: embedded HTML/CSS/plain JavaScript,
`internal/frontend/web` for the HTTP/WebSocket boundary, and the existing RPC
client for session data. There is no JavaScript build system, new core facade,
second history store or policy evaluator. A pinned WebSocket library avoids a
homegrown frame parser; its license is embedded in `nibb licenses`.

## Verification and remaining U3 work

Tests cover pairing, missing/wrong credentials, origin/Host rejection, read-only
method enforcement, filtered capability discovery, browser lifecycle/message
operations, durable history after resume, live daemon events and session survival
after browser detach.
Browser checks use isolated synthetic sessions and local fake services, covering
literal HTML-like text, paging, refresh, collapsed thinking/tool details, reload,
disconnect notices and narrow layouts. These are acceptance fixtures, not claims
of production usage.

Browser acceptance also covers creation, message errors, independent drafts,
close/resume, and aborting a pending confirmation without executing its operation.
U3's approval/diff panels, masked credential replies, plan/budget views and policy
inspectors remain next.
U4 background runs, notifications and optional general TCP transport remain
separate roadmap work.
