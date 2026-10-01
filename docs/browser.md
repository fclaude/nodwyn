# Browser view

The console is organized around tasks (ADR 21's design canvas): **Home** starts
a task in a folder and lists what needs an answer, what is running and what
finished; a **task** shows its conversation with the approval inline, and
beside it the plan, this turn's budget, the changes, the project notes and the
workspace. The plan shows the model's progress through it: the steps it
marked done (with what it says shows it), the ones it skipped, and the next.
**Connections**, **Workspaces** and **Settings** hold the rest. The
U3 inspectors described below sit under a task's **Details**. For the app
window and first-run setup see [app.md](app.md).

## Tasks, folders and files

**Starting.** Home's composer starts a task in the chosen folder. **Add a
folder** names one and says what it holds — documents or code — and, in the
app window, opens the system's folder chooser. The kind is a label: every
kind has the same tools and the same guard (see
[ADR 22](adr/0022-cowork-workspaces-attachments-mcp.md), amended). The
Workspaces page edits a folder's name, kind and **mounted folders** (other
folders a task may read, or read and write, by absolute path). The name is
kept in `workspaces.toml` in the config directory; the kind and mounts in the
folder's `.nodwyn/workspace.toml` (a code folder with no mounts needs none).
They apply to tasks started afterwards.

**Attaching.** The paperclip in either composer attaches files to the next
message: text formats, `.docx`, `.xlsx`, `.pptx`, and PDF when `pdftotext`
is installed; images are refused for now. Each file is copied into the
folder's `.nodwyn/attachments/`, its text redacted and capped, and shown to the
model as data. The conversation shows a chip per file with how many
secret-like strings were hidden.

**Undo.** Where git is not the undo, nodwyn keeps a file's previous content
before changing it. A task's **Changes** list offers **History** per file;
**Restore** writes a kept version back through the ordinary approval (the
diff, then your answer), and keeps the current content too. From a terminal:
`nodwyn versions FILE` and `nodwyn undo FILE`.

**Tools from MCP servers.** Connections › Tools adds a remote server (HTTPS, or
plain HTTP to this computer only; a key goes to the keychain, bound to that host only), lists its tools for
review, and approves that exact tool list. For each tool you say what it does
— reads, changes things, or leave it undeclared — and whether it is offered;
nothing is offered until you choose. An undeclared tool asks every call and
offers no standing answer. If the server later changes its tool list, new
tasks withhold its tools until you review again.

## Other devices (ADR 30)

```sh
nodwyn ui --listen 100.101.102.103:7777          # a tailnet address
nodwyn ui --listen 100.101.102.103:7777 --name desk.tail1234.ts.net:7777
nodwyn ui --listen 0.0.0.0:7777 --name desk.local:7777 --tls-cert cert.pem --tls-key key.pem
nodwyn devices                                   # list paired devices
nodwyn devices revoke phone
```

A browser on another machine pairs with the code **and a device name**. It
gets a device cookie (HttpOnly, SameSite=Strict, Secure over TLS, 180 days);
`devices.toml` in the config directory keeps only the token's SHA-256, so the
file cannot pair anything. Revoking takes effect without a restart: new
requests are refused at once, and a console the device already has open is
closed before its next request, or within five seconds if it is only
watching. The console answers only to the names it listens on, so a wildcard
`--listen` (`0.0.0.0`, `::`) needs `--name` with the name browsers use. Every answer from a device is recorded as the bridge's
kernel identity followed by `via web:NAME`.

A device can read tasks, start and steer them, and answer approvals — one-time,
for this turn, or for the session, as the core offers; it can also restore a
kept file version (through the usual approval), save project notes, read the
policy files, and stop or resume a run. It cannot save a credential binding,
write a policy file, unlock or create the keychain, change the model, edit
workspaces, add, review, approve or remove MCP servers, evaluate Scheme, or
list and revoke devices: those stay with the computer nodwyn runs on. A request counts as remote when it comes from another address or names
this console by anything but its loopback address (a proxy such as
`tailscale serve` connects from loopback on a phone's behalf). Launch tickets
pair only local browsers. Without `--tls-cert`, traffic is unencrypted unless
the network encrypts it; a tailnet does.

# Local browser view

The U3 session desk supports conversations, prompt replies and session lifecycle
controls. Run a daemon, then attach the browser bridge in another terminal:

```sh
nodwyn daemon
nodwyn ui                         # opens the local browser page
nodwyn ui --read-only             # view only; no replies or lifecycle changes
nodwyn ui --socket /absolute/private/sock --no-open
```

`nodwyn ui` connects to an existing daemon and opens the page with a one-time
launch ticket in the address's fragment, so that browser pairs itself. It
also prints a random pairing code for another browser on this computer;
paste that code into its page. The code is never put in a URL or session log.
Keep it in the terminal rather than sharing it. The browser receives an
HttpOnly, SameSite=Strict session cookie; no credential is written to browser
local/session storage. Without `--listen`, restarting the bridge creates a new
port and code; with it, the port is yours and a device's cookie survives
restarts.

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
The interactive mode includes a pending-prompt panel with reasons, literal
arguments, egress scope, and the existing write diff (additions/deletions colored
without changing the diff algorithm). Allow once and Deny answer only that live
prompt. Eligible ordinary calls also offer the displayed command/write prefix for
this session, or ordinary writes for this turn. Security and protected-path
confirmations remain one-time only; no grant overrides a denial. The daemon sends
derived scope labels in optional prompt metadata and validates the actual pending
prompt again when an answer arrives. An older daemon without this metadata gets
only one-time browser buttons.

When an unbound call has a core-approved exact binding proposal, the panel shows
its credential class, host, method and injection position. Saving that row uses
the separate binding method, persists beyond the session, and permits the pending
call. That prompt-bound operation cannot substitute row fields. The separate policy-file
editor below proposes explicit file changes for another review. Resume never reinstates session/turn grants from their audit history.
Scheme evaluation remains unavailable from the browser.

Credential prompts use a password field and the existing shared keychain path.
Input is cleared on submit, session switch, refresh, or disconnect; it is never
saved as a draft or included in the transcript. JavaScript strings cannot be
reliably wiped from browser memory; the UI does not claim a secure memory arena.
Budget checkpoints show usage and unchanged hard ceilings, with continue, stop,
and explicit absolute soft-limit choices. More budget grants no tool authority.

Read-only mode displays the pending prompt but exposes no reply controls and
rejects replies at the server. Pending state is restored from the daemon on
reconnect; stale/replayed answers are rejected by core. Resolution in another
client clears the panel. Browser replies retain the Unix peer identity of the
bridge in audit receipts; individual tabs are not separate security principals.

Closing a browser tab or stopping `nodwyn ui` does not close sessions in the separately running daemon. Stopping the daemon still closes its
sessions normally. The session list's titles and history metadata are snapshots; use Refresh to
update them. Open/running/needs-answer state updates live across the daemon's
sessions, including sessions opened by another client. These updates do not
reload the selected conversation, replace an inspector or discard a draft.

The session view buttons open fixed snapshots:

- **Plan** shows the latest user message's recorded gate outcome, score and plan,
  and the steps the model marked since (`plan.step`), in order.
  A newer message without a recorded outcome never inherits an older plan.
- **Budget** separates current configured soft/hard limits from the latest turn's
  recorded checkpoints. Checkpoint usage is not live remaining allowance.
- **Log** filters durable records by kind prefix and shows the last 1–1000 matches.
- **Policy** shows the last validated bindings, current command/write prefixes and
  turn approval. It does not restore authority from recorded receipts. Separate
  buttons open the guarded policy-file editor.
- **Tools** shows the model's current tool catalog and input schemas.
- **Friction** reads `docs/friction.md` through the session interpreter and guard.
  It needs an idle open session in interactive mode. Custom checker prompts take
  priority; after answering, reopen Friction to see the completed result. Refresh
  view starts a new guarded read. Denial or cancellation returns an error, never
  the preceding read's contents. Text passes normal redaction and is rendered
  literally. This fixed-path operation cannot select arbitrary files.

Every inspector names its log watermark. Live transcript events keep arriving but
an open inspector stays fixed until Refresh view reloads history and metadata.
Current session metadata is sampled separately from the log prefix. Saved sessions
retain plan/log views; current limits, policy and tools require an open session.
Pending prompts take priority over inspector navigation. Read-only mode has the
same snapshot inspectors; it cannot start a friction read. It retains its server-side mutation restrictions.

Without `--listen` the bridge binds only `127.0.0.1` on a random port; with it,
the address you give, for paired devices (above). Every request checks its
exact Host; pairing and WebSocket upgrades also require the exact browser
origin. Cross-site fetches and framing are refused. There is no general TCP
RPC transport: the daemon's Unix socket remains private, and kernel peer
identity still identifies the bridge process.

Implementation is deliberately small: embedded HTML/CSS/plain JavaScript,
`internal/frontend/web` for the HTTP/WebSocket boundary, and the existing RPC
client for session data. There is no JavaScript build system, second history store or policy evaluator.
`app.js` is the entry point (pairing, router, shell); `connection.js` is the one
connection to the bridge; `desk.js` holds what every page reads (tasks, what
needs you); `sidebar.js`, `home.js`, `task.js`, `transcript.js` and `pages.js`
draw the views; `prompts.js` renders review choices; `setup.js` the first-run
model and keychain setup; `attach.js` attachments; `markdown.js` model replies;
`inspectors.js` fixed snapshots from the same redacted records; `policy.js`
policy drafts and review controls; `dom.js` the helpers that keep every string
text. A pinned WebSocket library avoids a
homegrown frame parser; its license is embedded in `nodwyn licenses`.

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
Prompt tests also cover attributed one-time approval/denial, stale and forged
answers, canary credential isolation, and checkpoint hard ceilings. Browser checks
exercise pending snapshots, cross-client resolution, masked input clearing,
read-only prompt display, and valid/invalid checkpoint choices.
Scope tests additionally cover temporary grant lifetime, protected/security
confirmations, exact persistent binding and host injection, forged destinations,
and clean authority after resume.
Inspector browser checks cover paged log filters, older-plan exclusion, fixed
snapshot refresh, current/saved session distinctions and prompt priority.
The friction view is covered through core, RPC and the browser WebSocket, with
allow/deny/custom-confirm/cancellation, concurrent-operation rejection and canary
redaction. Browser checks also cover prompt priority, cached result reopening,
explicit guarded refresh and denial replacing the previous result.
The policy editor completes the current U3 scope. Its two targets are shared
`bindings.toml` and repository `.nodwyn/allow.toml`. Reads and writes use fixed host
primitives through the interpreter and guard, with no arbitrary path or browser
Scheme evaluation. The editor is unavailable through read-only bridges.

Each save validates TOML and requires a one-time diff confirmation. Session/turn
approvals cannot waive it. A revision comparison under the shared policy lock
rejects stale edits; a new read and review are required. Publication is atomic
and mode 0600. Cooperative writers use the sibling `.lock`; an unrelated editor
can still ignore that advisory lock. Files are capped at 64 KiB, final symlinks
and non-regular files are refused, and repository approval paths cannot escape
through parent symlinks. Recognized secrets are refused instead of round-tripping
redacted text as editable policy.

Bindings apply before the next egress operation. Repository approval-file changes
apply to new or resumed sessions; existing sessions retain their current grants.
Global `config.toml` settings and transient grants are not edited by this view.
Drafts stay only in the selected tab/session and clear on switching, refresh or
disconnect. After answering a prompt, reopen Policy to see the result. Successful
saves require reloading before another edit; failures retain the draft. A failed
connection never retries a save automatically.

Tests cover per-save confirmation, denial/cancellation, stale revisions,
caller-supplied paths/actors, environment-name hijacking, model-origin rejection,
known-secret containment, bounded reads and filesystem escape checks. Browser
acceptance covers creation, persistence, validation errors, stale saves and draft
retention after a failed write.
Supervised background runs and cross-session notifications are described below.
Optional general TCP transport remains separate roadmap work.


## Cross-session attention (U4)

The sidebar marks every open session needing a confirmation, credential reply or
budget answer. Select that session to review its normal pending-prompt panel.
The lightweight `session.watch` feed contains only IDs, state flags and revisions;
it does not subscribe the browser to other sessions' transcripts or prompt text.
Late snapshots cannot replace a newer state. Closed/resolved sessions clear their
attention indicator, and disconnect clears the live-state claim.

Desktop notifications are optional. In Settings › Desktop notifications, **Turn
on for this tab** asks this browser for permission; the choice is held only in
this tab. In the app window the system's notifications are used instead, with
no browser permission to grant. When the page
is hidden, a new needs-answer transition can produce a generic notification.
It never includes task text, prompt arguments, diffs, credential classes or
values. Clicking it opens the corresponding session for review. Resolution
closes the notification where supported; reconnect snapshots do not re-notify old
prompts. Blocked or unsupported browser notifications leave sidebar attention
available. Browser/OS notification delivery still depends on their permission
and focus settings.

RPC/WebSocket tests cover content-free snapshots and live transitions. Browser
acceptance verifies another session's prompt and resolution without disturbing a
draft, and notification mocks verify opt-in, generic text, revision ordering,
deduplication and reconnect behavior. Native OS notification delivery was not
exercised by those mocks. The supervised run driver is now available through `nodwyn run`; the browser
receives its ordinary checkpoint prompts and Abort stops the whole run. See
[runs](runs.md). Unattended continuation remains the next U4 slice.
