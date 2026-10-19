# ADR 0054: Several hosts in one window

- Status: accepted; §1–§8 implemented 2026-10-18; §9 not built
- Date: 2026-10-18
- Amends: 30 (a link is a device of the other host), 52 (§6, several hosts
  in one app)

## Context

The person runs nodwyn on a laptop and on a server that is always on
(workshop), and wants both in one window: the laptop's tasks and workshop's side
by side, what waits for an answer on either, and a new task started on the
one that should run it. Today the console holds one socket, to the bridge
that served the page, which relays it to one daemon. Seeing workshop's tasks
takes workshop's own console in another tab, and the desktop window cannot open
one at all: it loads only this computer's loopback address
(`cmd/nodwyn/app_bridge.go`). A design canvas of 2026-10-17 (a private
claude.ai artifact, [link removed]) drew
the window: tasks grouped by host in the sidebar, Needs you across hosts, a
Runs on picker that the folders and models follow, a Hosts page and Add a
host.

What bounds the design: ADR 30's amendment "a computer nobody sits at" made
pairing an invitation and the person a passkey, and the person asked for no
hand-made tunnel, nothing that depends on one VPN, and no sign-in that
lapses while it is used. A console's cookie, its content policy and a
passkey all belong to its origin.

Weighed and not chosen:

- **The page talks to each host's console itself.** Each console refuses
  another origin, its cookie is `SameSite=Strict`, a passkey is bound to its
  host's name, and the window opens no address but loopback.
- **The bridge keeps the link's token in a file**, beside `devices.toml`.
  Simpler, but a token kept in clear is read by any command the person
  approved, and works from anywhere for 90 days. The keychain holds secrets,
  the daemon holds the keychain, and it sends a key only where a binding
  allows (ADR 6).
- **An approval with matching words**, as the canvas drew it. An invitation
  already proves that the other host's person let this computer in: 100
  bits, good once for ten minutes, and the way every device pairs.
- **A link that touches a passkey.** A page at this computer's address
  cannot ask for a passkey of another host's name (WebAuthn binds it to the
  RP ID), and a key the link made itself would prove only that it holds the
  keychain, which the token already proves.

## Decision

1. **A host is another nodwyn this computer is linked to.** Linking pairs
   this computer with that host's console as a device, named as the person
   chooses (`laptop`), with an invitation made there (`nodwyn devices
   invite`, or Settings › Keys & devices on one of its devices).
   `host.link {name, address, as, invite}` on this computer's daemon posts
   the invitation to the host's `/pair` with `host: true` and keeps the
   device cookie it gets back. A host's name is this computer's for it
   (`workshop`), in the form of a device's.
2. **The link's token is a credential in this computer's keychain,** of
   class `nodwyn-host-NAME`, whose value is the cookie
   (`nodwyn_device=…`), with binding rows for the host's name, GET and
   POST, at `header:Cookie`. Linking needs the keychain open. The daemon
   sends the cookie through the one authorizer a model's key goes through
   (`core.Keyed`), so it goes to that host's name and nowhere else, and a
   command may not take it away. `hosts.toml` in the config directory keeps
   each link's name, address and the name this computer has there. The
   daemon writes it, as the user, in the config directory (0700), which a
   task's file tools never reach; it holds nothing secret, and the one thing
   it decides, the address the daemon dials, is checked against the binding
   table, so an address changed there is sent no cookie.
3. **The daemon relays.** `host.connect {name}`, on a daemon connection of
   its own, dials the host's `/socket` with the cookie and the host's
   origin; after its reply, the connection carries the console's messages to
   the host and the host's back, one JSON message per line, until either end
   closes. A host that refuses the link (revoked there, or not used for 90
   days) is said apart from one that cannot be reached or whose token is
   locked away, and a host that closes the link says why (`host.closed`).
   An upload to a host's task goes through `host.attach`.
4. **The window keeps one origin and holds one connection per host:**
   `/socket` for this computer, `/socket?host=NAME` for a linked host, which
   the bridge relays through `host.connect`, and `/attach?host=NAME`
   likewise. A host's link is this computer's authority there, so this
   computer's browser and the person's device of this console use it, a
   device that only answers here does not, and only they link and unlink
   (ADR 30's tiers: using a link is the person's, linking is setup).
5. **The other host's rules decide what the link may do.** It is a device
   there: it reads, starts tasks, answers and steers, recorded as `via
   web:laptop`. It is the person's, and makes that host's choices (a task's
   model, judge, helpers, limits and schedule, a key the model asks for,
   unlocking its keychain), when its invitation was made by a device of
   that host signed in with the person's passkey, after a touch, and it
   linked at an address where a browser could use one (https, or a
   `localhost` name), so what the person types through it does not cross the
   network in clear: the link then carries that passkey's name, and loses
   those choices when the passkey is removed, as a device signed in with it
   does. One made by `nodwyn devices invite` or the host's own console,
   which vouch with no passkey, makes a link that answers. A link never changes the host's setup, since it cannot
   touch a passkey: the host's bridge offers it no setup method, and the
   Hosts page opens the host's own console for that. The host's Settings ›
   Keys & devices says the device is another nodwyn.
6. **Unlinking forgets the token, its binding rows and the line in
   `hosts.toml`,** having first asked the host to forget this computer
   (`devices.leave`, which any device may ask for itself), and says when the
   host could not be told.
7. **What the window shows.** With a linked host, the sidebar groups tasks
   by host, each with its state (ready, connecting, not reachable, refused,
   keys locked), under one Needs you for every host; Home's Runs on chooses
   the host a new task starts on, and its folders, models and what it may
   choose follow that host; the lists below it are every host's, each task
   naming its host; a task's page names its host; and Hosts lists this
   computer and each link, with its state, address, the name this computer
   has there and what it may do there, Add a host, Unlink, and a link to the
   host's own console. With no link the window is as it was. Settings,
   Connections, Workspaces and Workflows are this computer's.
8. **Notices are per host:** a linked host's task that asks shows the same
   fixed notice, and opening it opens that task on that host.
9. **Not built:** a home host that a phone reaches every host through
   (plan.md, Next 1.4); the TUI and line mode, which see this computer
   only; a linked host's Settings in this window.

## Evidence

- `internal/frontend/web`: `TestALinkedHostIsReachedThroughThisComputersDaemon`
  runs two daemons and two consoles. A link vouched for by the person's
  passkey is recorded by the host's daemon as `via web:laptop
  passkey:phone`, is offered the person's choices and no setup, opens a
  task and sends it a file there; one from the host's terminal answers
  only. The token is in the keychain and bound to the host's name alone;
  an address changed in `hosts.toml` is sent no cookie; the host's
  revocation closes the open socket with 1008 and refuses the next;
  unlinking a revoked link says the host could not be told, and one the
  host still takes is told (`devices.leave`), each forgetting the token,
  its rows and its line. `TestOnlyThePersonUsesThisComputersLinks` places
  the methods in the bridge's tiers.
- The `hosts` browser check, two daemons and two consoles on one computer:
  Add a host, the sidebar by host, Runs on, a task started on the host with
  a file, Needs you across hosts and an answer recorded there as the
  laptop's, a notice that names its host and opens its task there, the
  phone's width, a revocation, and Unlink.

Not verified: two computers, https between them, or a host behind a proxy
that ends TLS; the vouched link in a browser (the check's host listens on
plain http, where a link answers); and the host's Settings › Keys & devices
saying a device is another nodwyn, which `nodwyn devices` and the Go test
show.

## Consequences

- The daemon's protocol gains `host.list`, `host.link`, `host.unlink`,
  `host.connect` and `host.attach`; the console's bridge gains
  `devices.leave`, `host` on `/pair` and on a device in `devices.list`.
- Both hosts need this version: an older console refuses `host` on `/pair`
  as an unknown field, which Add a host shows as its answer.
- A link is as strong as the keychain it is kept in: without a passphrase,
  the keychain's file is as safe as its permissions (ADR 5), and whoever
  reads it can act on the host as this computer until the host revokes it.
- Every request for a linked host passes this computer's daemon, so the
  window shows a host only while this computer's daemon runs.
