# ADR 0030: One app, three ways to reach it — desktop, browser, phone

- Status: **accepted**; implemented as D1–D9, 2026-09-30 to 2026-10-01
- Date: 2026-09-30

## Context
ADR 21's design canvas (2026-09-30) reorganizes the console around tasks: a
home page, a task view with the approval inline, a Connections page and a
phone view. Two requirements sit outside that canvas:

1. **Remote work stays a browser.** The maintainer runs nodwyn on one machine
   and answers from another (or a phone). Today `nodwyn ui` binds 127.0.0.1
   only, and every browser tab answers as the bridge process: a phone cannot
   be told apart from the desk.
2. **A standalone app for people who will never run a daemon.** nodwyn is to
   be shared with family. They launch an app; they do not open a terminal,
   read a pairing code, unlock a keychain at a TTY prompt or edit
   `config.toml`. Today all four are required.

## Decision

### 1. One process shape: daemon + bridge, owned or attached
`nodwyn app` is the whole product in one process:

- if a daemon already answers on the default socket, **attach** to it (the
  maintainer's setup; closing the app leaves sessions running);
- otherwise **own** one: `rpc.Serve` in-process on a private socket, so the
  wire contract, peer identity and every guard path are exactly those of
  `nodwyn daemon`. Closing the app closes its sessions; their logs remain and
  resume later, as after any daemon shutdown.

No second code path: the app is a daemon, a bridge and a window glued
together in `cmd/nodwyn/app.go`.

### 2. The window
With the `webview` build tag (cgo; WebKit on macOS, WebKitGTK on Linux,
WebView2 on Windows) `nodwyn app` opens a native window on the bridge's
loopback URL. Without it — the default, cgo-free build — it opens the
default browser. `make app` builds `Nodwyn.app` for macOS with the tag. A
native shell is a packaging decision, not an architecture (ADR 21 U3).

### 3. Pairing without a terminal: a one-time launch ticket
The launcher that started the bridge mints a **launch ticket** — random,
single use, valid for 60 seconds — and puts it in the URL *fragment*
(`#ticket=…`), which browsers never send to a server. The page posts it to
`/pair` and removes it from the address bar; the bridge exchanges it for
the usual HttpOnly SameSite=Strict cookie and forgets it. The pairing code
stays for anything that did not launch the page (another browser, a
phone). Tickets and codes never enter a session log.

### 4. Remote devices are named, and answers name them
`nodwyn ui --listen ADDR` binds a non-loopback address (a tailnet address is
the intended use). It prints a warning naming what a connection can do.
Pairing a remote browser requires the code **and a device name**; the
bridge issues a device token (stored hashed in `devices.toml`, mode 0600,
revocable with `nodwyn devices`), so a phone stays paired across restarts.

The bridge forwards the device name in `hello` as `via`. The daemon records
the actor as the kernel identity **followed by** the claim:
`unix:uid=501,gid=20,pid=4242 via web:phone`. The claim cannot replace the
kernel identity — any same-user process could make it — but it keeps
`guard.decision` honest about which screen said yes.

**Remote devices get answers, not policy.** Over a non-loopback listener
the bridge refuses settings, keychain, policy-file, binding-file and
workspace-registry writes. Messages, approvals (one-time and scoped, as the
core allows), aborts and reads are available. The phone view is the task
view at phone width, not a separate client.

TLS is optional (`--tls-cert`, `--tls-key`); on a tailnet WireGuard already
encrypts. Without TLS on a non-loopback address the warning says so.

### 5. What a family member must never need
- **Keychain:** a daemon may start with the keychain locked. New methods
  `keychain.status`, `keychain.create` and `keychain.unlock` (local bridges
  only) open it later; sessions opened afterwards receive it. The
  passphrase travels like a `session.secret` reply: typed into a password
  field, never logged, never echoed.
- **Model configuration:** `settings.read` / `settings.models.write` read
  and replace the `[control]` model (and `[models.*]`) in `config.toml`
  with the loader's own validation, atomically, local bridges only. New
  sessions use the new configuration; open sessions keep theirs.
- **Folders:** workspaces (ADR 22 W1) are registered by name. In the app a
  native folder chooser supplies the path; a browser types it.

### 6. The console converges on the canvas
The browser client is rebuilt around tasks: Home, Task, Connections,
Workspaces, Settings, with the design's tokens, embedded fonts (OFL) and a
dark palette. Everything the U3 inspectors did remains reachable from a
task's details. Views never invent state: budgets are counted from the
log, plans are the recorded plan, "changes" are recorded writes, and a
view that has no source says so rather than guessing.

## Consequences
- The protocol grows additive methods (`attention.list`, `keychain.*`,
  `settings.*`, `workspace.*`, `session.notes.*`, `models.status`) and an
  optional `via` on `hello`. The schema drift test covers them.
- A second trust boundary exists: a paired remote device. Its tokens are
  hashed at rest and revocable; it cannot widen policy files.
- The app build needs cgo and a platform webview; the default binary does
  not, and CI keeps building without the tag.
- An unsigned `Nodwyn.app` needs a right-click → Open on first launch;
  signing is a distribution step outside this ADR.

## Review amendments — 2026-09-30

- **A device is remote wherever it connects from.** Any request carrying a
  device token gets a remote device's methods, even from loopback with the
  console's own Host (a proxy that preserves Host could otherwise pass as
  local). Only this machine's pairing grants local powers.
- **Task roots are limited.** `session.open`, `run.start` and
  `workspace.save` refuse `/`, the home directory itself, and any folder that
  is inside or contains nodwyn's config or data directory or a credential
  directory the guard denies. The path denylist now resolves relative paths
  against the root and follows links; protected paths apply inside mounted
  folders; the innermost mount decides access and nested mounts are refused.
- **"Answers, not policy" is a limit on the console, not on authority.** A
  device that may approve `shell-exec` can run commands as the user, and
  security confirmations can be answered from it. Pair only devices you
  would hand a terminal; prefer documents workspaces for devices you share.
- **Without TLS the device cookie is not `Secure`,** and cookies are not
  scoped to a port: another plain-HTTP service on the same host name would
  receive it. Use a tailnet or `--tls-cert`, and no other HTTP service on
  that name.
- A secret prompt declined elsewhere left the prompt on other screens; every
  prompt now emits an ephemeral `prompt.closed` when it stops pending.
- **`nodwyn-stable` is the window build.** A desktop launcher runs
  `nodwyn-stable app` without a terminal, where the browser fallback reads as
  an app that did not start. `make promote` builds with the tag
  (`make build-webview`) on Linux and macOS and checks the installed binary's
  build tags; only a system without WebKitGTK gets the browser build, with a
  note. `make build` and CI stay cgo-free.

## Settings — 2026-10-02

Settings changes what a person sets in `config.toml` without a terminal,
not only the model: `settings.write {set}` changes named settings together,
from a fixed list the daemon keeps (how Home starts a task, the turn
budgets and spend limit, the loop's timeouts, the window, the main model's
tuning, the judge's content level, threshold, timeout and model, and the
container runtime). The list leaves out what decides where content goes or
what nodwyn may do: the model's and the judge's addresses (the model's has
its own form, with its key and binding), credentials, the egress proxy, and
`[policy]`. `config.Edit` makes the changes line by line, keeping the rest
of the file, and only the result is validated, as the loader would, under
the policy-file lock; a refused change leaves the file as it was.
`settings.read` reports each listed setting's value, default and whether the
file sets it, and whether a judge is available. Like the model, changes
apply to sessions opened afterwards; a session reports the budget it runs
with, not the daemon's current one. A paired remote device reads Settings
but cannot change them.

## Approvals — 2026-10-02

ADR 35 replaces "how Home starts a task" (`tasks.*`) on the settings list
with `[approval]`: whether writes in the folder ask, and who answers a
command (the judge, then the person; the person; or nobody). These decide
what a task does without asking, so they are chosen here, on the computer
nodwyn runs on, as "always allow" for uploads is: a device reads them and
cannot change them, and its tasks ask what they say. A device's console
starts only chats; `run.start` and `run.resume` with `auto_approve`,
`judge` or `unattended` stay refused to it.

## Amendment 2026-10-03: Batch provisional transcript updates

A live CPU profile of the Linux app found roughly 37% of sampled host
cycles in the GTK main loop, mostly repainting. The console rebuilt a
task's heading and all its detail panels for every streamed fragment,
although those fragments changed only the transcript. Each fragment also
read the transcript's layout before and after changing it.

Join adjacent fragments of the same kind and draw them on the next
animation frame, with one scroll check for the batch and no heading or
detail-panel refresh. Keep fragments for a detached task until it is shown
or another event arrives. Flush before non-stream events, including a
recorded reply, a question and session closure, so a background tab whose
animation frames are paused still processes them in order. Reloading or
disposing a view discards its pending frame and provisional fragments.

This changes display scheduling only. It neither drops durable events nor
changes the daemon, the event protocol or guard checks. The transcript
browser check measures DOM mutations for a fragment burst and exercises
scroll following, questions, final responses, switching, reload and
disposal; energy savings still require a separate power measurement.

## Amendment 2026-10-08: a device chooses no judge (ADR 47)

A task's judge is chosen as its model is, and for the same reason it stays
with this computer: the judge is shown the task's plan and commands. The
bridge lists `session.judge.set` only for this computer, and the daemon
refuses `judge` in `session.open` and `session.judge.set` from a paired
device.

## Amendment 2026-10-16: this computer's cookie is the run's session, not the code

A browser on this computer was paired with a cookie whose value was the
pairing code. Cookies are not scoped to a port, so every server on
127.0.0.1 that the same browser visits (a dev server a task started, a
repository's own) received the code, and the code pairs: from there a
client could take this computer's powers, or, with `--listen`, pair a device
for 180 days. The cookie now holds a random token made for each run of the
console and accepted only until it stops; the code only pairs.

This narrows the exposure but does not close it: the token still reaches
other loopback servers, which could use this console until it stops. Closing
it needs a host name of the console's own, such as a random `*.localhost`
name, which not every webview is known to resolve; that is not built. The
desktop window does not browse other sites and is not exposed.

## Amendment 2026-10-16: the console keeps its port

A browser keeps what the console remembers in it (the theme, thinking shown,
folders folded, a model chosen and not yet used) for the console's origin,
and the origin includes the port, which was new at every launch of `nodwyn
app` and `nodwyn ui`: all of it was lost each time. The loopback console now
listens on the port it had the last time when it is free, kept in the data
directory as `console-port`, and on a new one, kept for next time,
otherwise. A cookie from an earlier run holds that run's session token,
which the new run does not take, so reusing the port pairs nothing.

## Amendment 2026-10-16: slow work is done outside the lifecycle lock

Every method runs under one lock, and some held it for what only waits: a
revocation sent to a provider (up to 15 s), the key derivation of
`keychain.create`, `keychain.unlock` and `keychain.passphrase`, the wait for
an OpenAI consent page (its discovery document is fetched first), a session
winding down in `session.close` and `run.resume`, and the making of a session
in `run.start`, `run.resume` and a scheduled run, though `session.open` and
`session.resume` already let go for it. While one of them waited, every other
client's `session.answer` and `session.send` waited too.

- **One helper, `server.slow(id, work)`, lets go of the lock for the slow
  part**, as the model probes do, and every such method uses it. A task named
  by `id` is marked as being resumed meanwhile, so a second resume or a
  scheduled run of it waits for the first.
- **Closing a session leaves it out of the list first**, then winds it down
  outside the lock.
- **Serve closes the sessions beside the wait for the methods still running**,
  not after it, and the waits a method makes on the network (a model test, the
  models a server offers, an image pull) end with Serve's context. A method
  waiting inside a session, such as an image's confirm, ends when the session
  closes. A session a method makes after shutdown began is closed at once.
  Before, the daemon waited for the longest of them, 10 to 30 minutes.

Not changed: the `settings.*` methods still hold the lock while they wait for
`config.toml`'s own lock, which a session holds only for the moment of a save.
