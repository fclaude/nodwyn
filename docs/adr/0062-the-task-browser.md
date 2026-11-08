# ADR 0062: The task's web browser, from Raulí, on the sites the person lists

- Status: accepted; first part implemented 2026-11-08
- Date: 2026-11-08
- Amends: ADR 7 (Brote v0.9.1, the release Raulí v0.2.0 requires too), ADR 23
  (the egress proxy also runs in nodwyn's process, for the browser)

## Context

A coding agent needs the web: a library's documentation, an issue, the
page of the app it is building. nodwyn had no way to read a page except
`curl` in the shell, which confirms every call and returns markup. A
contained MCP server running a browser would work with today's nodwyn
(ADR 23), but its rules and its keychain would be the server's own,
separate from nodwyn's guard and keychain, and the guard would see only
`mcp:NAME`, not the page an action is on.

[Raulí](https://github.com/fclaude/rauli) is a browser for agents by the
same author: it drives a headless Chromium over the DevTools Protocol and
offers its operations as tools (navigate, snapshot, click, type and the
rest). It was built with two interfaces through which every decision and
every secret reaches a tool, a Guard and a Vault, so that whoever embeds it
decides what the browser may do. Until 2026-11-08 all of it was under
`internal/`; its `browser` package now gives those types public names, and
nodwyn pins it as it pins Brote.

ADR 23 §1 refuses to run a third-party server as a process of nodwyn's on
this computer. Chromium is not that: it is a browser the person installed,
started by code nodwyn links and pins, as nodwyn runs git and pandoc. What
it loads is untrusted, and that is what the decisions below are about.

## Decision

1. **The browser is offered when config.toml lists sites.** `[browser]
   sites` is a list of hosts written as a contained server's network list
   (a host allows ports 443 and 80, `host:port` that port alone, no
   wildcard). Empty, the default, offers no browser. It is config.toml's,
   in the person's config directory, never a repository's: a stranger's
   clone cannot widen where its tasks may browse, which is why a project
   file does not hold it. A managed child app (ADR 56) gets no browser.

2. **Raulí's tools are primitives, `browser-NAME`.** Each crosses nodwyn's
   guard before Raulí runs it. Their destination is the browser
   (`browser`), which may send what it is given on to the listed hosts and
   nowhere else, so they are judged as a contained server's tools are
   (ADR 23 W3b): a literal key, or a value derived from a credential, in
   what they send is refused unless its class is bound for every listed
   host, and content from outside may go there, since the person listed
   them. Arguments that reach a page (an address, text typed, a value
   chosen) are egress payloads: `browser-navigate`, `browser-tabs`,
   `browser-type`, `browser-select`.

3. **An action on a page asks, every time.** `browser-click`,
   `browser-type`, `browser-press` and `browser-select` can pay, delete or
   send what was typed; they are irreversible, as an MCP tool that acts is,
   and the confirm names the page and the element (`button "Pay now"`) from
   the page's last snapshot. It is an ordinary confirm: `[approval]
   commands` decides who answers it (the judge by default). Reading a page,
   scrolling, going back and waiting ask nothing.

4. **Pages are data.** Every browser primitive's result is tagged
   `external:web`.

5. **Three layers keep the browser to the sites.** nodwyn implements
   Raulí's Guard: `CheckURL`, asked about every request a page makes (the
   document, scripts, images, frames, fetches), refuses an address that is
   not on the list before it is sent, and a page of its own cannot be a
   `data:` or `blob:` address. The request filter does not see WebSockets
   or a worker's requests, so the browser's only way out is a proxy in
   nodwyn's process, the one a contained server's egress runs
   (`internal/egress`), on a loopback port of its own: it lets through the
   listed hosts and ports and nothing else. Chromium is told to send
   loopback through it too (`--proxy-bypass-list=<-loopback>`), so a page
   cannot reach a local service that is not listed, and to send no WebRTC
   UDP outside it. A listed name that resolves to this computer or a
   link-local address is refused; a loopback name or address the person
   listed (`localhost:3000`) is reached. Anyone on this computer could use
   that proxy, and would reach only the listed hosts, which they can reach
   without it: it carries no key.

6. **The browser starts at the first call and closes with the task.** Its
   profile is new each time and deleted after, downloads are refused, and
   Raulí's reaper ends a browser whose nodwyn was killed.

7. **What it reached is in the log.** A refused request, and every
   connection through the proxy, made or refused, is a `browser.egress`
   event (method, host, port, never a path or a query), up to 1000 a task,
   and then how many more there were. Clients show a refused page request;
   the proxy's connections stay in the log, since they include the
   browser's own traffic.

8. **Later parts.** Screenshots, once a tool's result can carry a picture
   to the model; logins through nodwyn's keychain as Raulí's Vault, a
   credential's class bound to the origins it may be typed on; uploads,
   through the path checks; and a live view in the console with taking the
   tab over.

## Not built in the first part

- `screenshot` and `click_at` are not offered: a tool's result reaches the
  model as text only (images go with a person's message, ADR 22 §3).
- `credentials` and `fill_credential` are not offered: there is no Vault,
  so the browser has no login.
- `upload` (a file of this computer to a site), `eval` (code in the page,
  which can read its cookies) and `resize` are not offered.
- No live view, and no setting in the console: `[browser] sites` is edited
  in config.toml.
- The profile is always throwaway; a kept profile, with its cookies, is not
  offered.

## Accepted limits

- **A listed site can receive anything the model can read.** As for a
  contained server's network list, content from a file may be typed into a
  listed page or put in an address on a listed site; the confirm on typing
  shows it. List sites you would hand that content to.
- **A listed page can say anything.** What it says is tagged and is data,
  but the model reads it; the list limits where an obeyed instruction can
  take the browser, and the confirm limits what it can do there.
- **A name is checked as the proxy dials it.** A listed public name that
  resolves to a private address (10.0.0.0/8, 192.168.0.0/16) is reached; only
  loopback and link-local are refused for names, as for a contained server.
- **The browser's own traffic.** Google Chrome asks Google's hosts for
  updates and sign-in; the proxy refuses them and the log records each.
  Two flags cut that from 18 records to 14 or 15 in a test that loads one
  page.
- **The shell is not the browser.** A task that can run commands can start
  a browser of its own; that is a command, and its confirm is the shell's.

## Measured

With Google Chrome 157 on Linux, `TestBrowserReachesOnlyItsSites` loads a
listed page that asks an unlisted host for an image and opens a WebSocket
to another: the unlisted host got no request and the WebSocket's no
connection, and both were recorded as refused. Without
`--proxy-bypass-list=<-loopback>` the WebSocket's refusal is not recorded
and the test fails; with `CheckURL` allowing everything it fails too.
