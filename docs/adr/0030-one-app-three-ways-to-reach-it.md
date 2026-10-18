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

## Amendment 2026-10-16: behind a TLS proxy, a large message, the launch ticket

- **A console behind a proxy that ends TLS is reached by `--name
  https://NAME`.** `tailscale serve` serves https and forwards to the console
  over http, so the browser's origin is `https://NAME` while the console
  expected `http://NAME`, and every pairing and socket was refused as an
  invalid origin. A name written `https://NAME` is answered with an https
  origin, a Secure device cookie and a `wss` socket in its content policy; the
  console itself still speaks http, and the other names keep the scheme of the
  listener. A refused origin is answered with the one expected.
- **A message above the daemon's frame limit is answered, not fatal.** The
  socket's read limit was the daemon's 1 MiB, and the websocket library closes
  the connection at the limit, so one pasted log ended the console's connection
  and every call waiting on it. The limit is now 4 MiB, and a request of 1 MiB
  or more is answered with an error that says so. Not built: a length check
  in the composers.
- **The launch ticket stays on the browser's command line.** It is single use
  and good for a minute, but another user of the computer can read it in the
  process list and spend it first. Handing it over through a private file (a
  page that redirects with the ticket in its fragment) was considered and not
  built: a sandboxed browser (a snap or flatpak one) may not read the user's
  runtime directory, and the console would then fail to open without saying
  why. It is documented instead, with `--no-open` and the pairing code (never
  on a command line) as the way on a shared computer.

## Amendment 2026-10-17: the console comes back by itself

A console reached from a laptop or a phone over a tailnet, with nodwyn
always on a server, loses its connection often: the device sleeps, changes
network, or a proxy (`tailscale serve`) closes a connection that carried
nothing for a while. A dropped socket waited on a **Reconnect** button, and
the bridge sent nothing to keep an idle one open. On loopback this rarely
mattered.

- **The console connects again by itself**, after a wait that doubles from
  a second up to thirty, and no sooner than every five seconds while the
  daemon is not running (1013). Coming back reloads the desk and the open
  task from the log, as the button did, and no operation is sent again: a
  request waiting when the connection dropped fails, saying so. **Reconnect
  now** skips the wait.
- **The page shown again, or the network back, tries at once**, and a
  connection that looks live then is asked to answer `hello` within ten
  seconds, since after a sleep a socket can look open with nobody at the
  other end. It is not asked while a request waits, because the bridge
  relays one request at a time and the answer would wait behind it.
- **A browser the console does not take stops trying.** A revoked device's
  open socket is closed with 1008, as before. A socket refused before it
  opened tells a browser nothing of why, and the same refusal comes from a
  device revoked while it was away, from this computer's browser after
  `nodwyn ui` restarted (its cookie holds the stopped run's session), or
  from no console at all, so the page asks `GET /paired`, which answers
  whether the console takes the cookies the request carries and nothing
  else; the Host and fetch-site checks of every request apply to it. Not
  taken, the page pairs again; unreachable, or a proxy's 5xx, it tries
  again.
- **The bridge pings an open console every 30 seconds** and closes one that
  has not answered within a minute, longer than a relayed call holds up the
  reading of its pong. A proxy keeps a connection that carries pings open,
  and a page that slept gives back its daemon connection.

Not built: a page reconnected to a console of a newer version keeps its old
code until it is reloaded; project notes being edited and a worktree
review's unsent comments are lost on a reconnect, as on a reload; the TUI's
daemon mode still reconnects only on Ctrl+R. Evidence: the `reconnect`
browser check, and `TestPairedSaysWhetherTheConsoleTakesThisBrowser` and
`TestASilentConsoleIsPingedAndClosed`.

## Amendment 2026-10-17: a page reconnected to an upgrade says so

On an always-on computer, `make promote` swaps the binary and the console
and the daemon start again from it. A page that comes back by itself (the
amendment above) keeps the code it loaded, against a bridge and a daemon
that may now answer otherwise, until someone reloads it.

- **The page compares the daemon's version.** `hello` returns it; the page
  keeps the first it saw, and when a reconnect's `hello` reports another, a
  banner says nodwyn was restarted as that version and offers **Reload**. A
  reconnect to the same version (a restart, a sleep, another network) shows
  nothing, and one back to the first version hides the banner.
- **It never reloads by itself.** A reconnect keeps each task's reply draft
  and the New task message, which live only in this tab's memory, and a
  reload loses them. The banner says so, and the reader reloads once they
  are sent.
- **Drafts are not carried across that reload.** Handing them over in
  `sessionStorage`, which is per tab and survives a reload, was weighed and
  not built. ADR 21 keeps drafts in tab memory and `browser.md` says a
  reload loses them; the console's storage holds choices (the theme, the
  last folder, folded folders), never what a task says. A draft holds
  whatever was pasted into it, a secret meant for a masked reply included,
  and Chromium keeps session storage in the profile on disk to restore a
  session, where removing the item later does not scrub it. Since the page
  waits for the reader, keeping the rule costs only that wait.
- **The version is the daemon's; the page's code is the console's**
  (`nodwyn ui`). `make promote` starts both from one binary, whose version
  is its commit, so there one changes with the other. A console restarted
  from a new build in front of a daemon still on the old says nothing, and a
  new daemon behind an old console offers a reload that brings back the same
  code; neither is how nodwyn is upgraded. Two builds of one dirty checkout
  both report `dev-COMMIT-dirty` and are not told apart.

Not built: comparing the console's own build, which the bridge would have
to add to `hello` (a protocol change). Evidence: the `reconnect` browser
check, whose daemon comes back from a build reporting `check-next`: the
pages say so and keep the reply being written, Reload runs the new code
and writes nothing to the browser's storage, and the restart of `nodwyn ui`
before it, to the same daemon, shows no banner.

## Amendment 2026-10-17: a computer nobody sits at

The person runs nodwyn on a server nobody sits at and reaches it from a
laptop and a phone. Everything this computer alone may do (Settings, keys,
the keychain, a task's model, judge, helpers, limits and schedule, a key the
model asks for, deleting a task) took a terminal on the server, and so did
pairing: the code was printed on the console's standard error, which under
systemd is the journal, for as long as the console ran, and it paired any
number of devices. A run built from HEAD on 2026-10-17 (`nodwyn ui --name
https://NAME`, paired by a probe) showed that, and that a request to the
console's own loopback address with the code paired as this computer, with
every setup method: what an `ssh -L` forward delivers, and what any account
on the server can send. The person asked for no hand-made tunnel, nothing
that depends on one VPN, and no sign-in that lapses while a device is used.

1. **A console other machines reach takes no pairing code.** Started with
   `--listen` or `--name`, it prints none and takes none, from another
   machine or from its own loopback address. This computer pairs with its
   launch ticket, which `nodwyn ui` now mints whenever its listener is on
   loopback, `--name` or not; a device pairs with an **invitation**: twenty
   characters, good once and for ten minutes, kept in `devices.toml` as its
   SHA-256. `nodwyn devices invite` prints one to the terminal that ran it,
   never to the console's log; a paired device makes one in Settings › Keys &
   devices, shown with a link that carries it in the fragment. A console on
   loopback alone (`nodwyn ui` without those flags, `nodwyn app`'s window)
   keeps its code: its loopback address is this computer, and a forward to it
   takes an account that already reaches the daemon's socket.
2. **The person is a passkey.** Over https, or at a `localhost` name, an
   invitation from the terminal, from this computer or from a device whose
   person touched their passkey also makes a passkey on the device that uses
   it (WebAuthn, attestation `none`, user verification required); any
   browser then signs in with that passkey, synced or through a phone's QR
   code, with no invitation. A device paired without one becomes the
   person's by signing in with one in Settings. The bridge checks each
   ceremony with the standard library alone (`passkey.go`): the challenge it
   issued, once; the origin, and not from a frame; the RP ID's hash; presence
   and verification; and an assertion's signature with the registered key,
   which the page sent as `getPublicKey()` gives it, so nothing is CBOR. A
   plain-http console has no passkeys, since browsers allow them only in a
   secure context: its devices answer, and setup stays where keys do not
   cross the network in clear.
3. **What the person's device may do.** All a paired device may; the choices
   this computer alone made (a task's model, judge and helpers, its turn
   limits, its schedule, a key the model asks for, unlocking the keychain,
   the folder last picked); and, after a **touch** of the passkey, setup:
   Settings, providers and models and their tests, credentials and OAuth
   sign-in, the keychain's creation and passphrase, MCP servers, workspaces,
   forgetting an index, policy files and bindings, devices and passkeys, and
   deleting a task. A touch lasts 15 minutes after the device's last setup
   action, in the bridge's memory, so a sitting asks once. A setup request
   before one is refused with code -32030; the console asks for the passkey
   in a dialog (a browser starts a passkey prompt only from a click) and sends
   the request again, which never reached the daemon. A device paired by a
   plain invitation may do what a paired device always could, and invite
   another such device. `session.eval` and runs nobody answers stay the
   terminal's.
4. **Who did it.** For the person's device the bridge's daemon connection
   says `hello {via, passkey}`, and the actor is `… via web:laptop
   passkey:laptop`; the daemon lets it make this computer's choices. It does
   not check the claim: a same-user client that claims nothing has every
   method already, so the claim raises nobody. The bridge enforces the touch,
   as it enforced a device's limits before. Setup writes are recorded in no
   log, so the bridge writes a line to its standard error (`Options.Audit`)
   for each sign-in, invitation, touch and setup action of a device, with
   the method and the names, never a value.
5. **A device stays signed in while it is used.** Its cookie is set again,
   for 90 days, whenever its console loads or connects, and `Devices.Check`
   forgets a device not seen for 90 days. It used to be set once, for 180 days
   from pairing however often the device was used, and nothing on the server
   expired it. Sign-ins are kept in `devices.toml`, so a restart or an upgrade
   signs nobody out; a restart forgets touches.
6. **A passkey can be removed,** a lost phone's: it signs nothing in again,
   and the devices signed in with it only answer. The last one stays, since
   without it only the terminal could let the person in again.
7. **A renewed certificate is served without a restart:** `--tls-cert` and
   `--tls-key` are read again when either file changes, so certbot, acme.sh or
   a local CA renew a console's certificate in place.
8. **A schedule waits for the keychain** instead of skipping its time (ADR
   49, amended 2026-10-17), so a server that restarts at night runs the night's
   schedules once someone unlocks it from a phone.

Network identity was considered and not chosen: a VPN's own (Tailscale's
`whois` and app capabilities) is not everyone's, and it names a machine,
not a person, so any process on an enrolled laptop, a coding agent or a
package's install script, would speak for it; the Origin check binds only
browsers. A VPN stays an extra layer in front of the console.

Not built: pasting an OAuth provider's redirect address into the console (a
sign-in to a provider still needs a browser on the computer nodwyn runs on,
ADR 24); a console that installs as an app (a manifest); built-in ACME
certificates (`--acme`); unlocking the keychain at boot from a systemd
credential sealed to a TPM; reviewing project trust in the console (`nodwyn
trust` stays the terminal's); a notification for a schedule that waits for
the keychain (Home says it).

Checks: `internal/frontend/web` (`TestAPasskeyPairsThePersonsDevices` with a
software authenticator, the wrong origin, RP ID, unverified person,
unregistered and forged keys, a removed passkey and an address refused;
`TestThePersonsDeviceTouchesItsPasskeyForSetup` over the socket and a real
daemon; `TestASharedConsolePairsThisComputerOnlyByTicket`;
`TestRemoteDevicesPairByInvitationAndCannotWidenPolicy`;
`TestADeviceInUseStaysSignedIn`; `TestAnInvitationIsGoodOnceAndExpires`),
`internal/frontend/rpc` (`TestAPasskeyDeviceMakesThisComputersChoices`),
`cmd/nodwyn` (`TestARenewedCertificateIsServed`), and the `remote` browser
check with Chromium's virtual authenticator.
