# nodwyn as an app

`nodwyn app` is the whole product in one program (ADR 30): a window with the
console, and — when no daemon is already running — the daemon itself, in the
same process. Nobody has to start `nodwyn daemon`, copy a pairing code or unlock
a keychain at a terminal.

```sh
nodwyn app              # a window (webview build) or the default browser
nodwyn app --browser    # the default browser even in the webview build
nodwyn app --no-open    # print the launch address instead of opening it
```

It also takes `--socket PATH` (the daemon socket to attach to or serve on),
and the remote-device flags of `nodwyn ui`: `--listen ADDR`, `--name
HOST:PORT` and `--tls-cert`/`--tls-key` (browser.md, "Other devices").

- **A daemon is running** (your usual `nodwyn daemon`): the app attaches to it.
  Closing the app leaves its tasks running, as closing `nodwyn ui` does.
- **No daemon is running**: the app serves tasks itself on the standard
  socket, so `nodwyn tui` or `nodwyn ui` can attach to it meanwhile. `nodwyn
  tui` and line mode (`nodwyn chat`) do the same when they find no daemon
  running ([the TUI](tui.md), [the clients](clients.md)).
  Closing the app closes those tasks; their conversations are kept and resume
  later like after any daemon shutdown.

The page the app opens pairs itself with a one-time launch ticket carried in
the address's fragment (never sent to a server, removed from the address bar
at once, valid for a minute). The browser is started with that address, so
another user of the same computer can read the ticket in the process list for
the moment it starts and spend it first; on a computer other people log in to,
pair with the code the app prints to its log instead (`nodwyn ui --no-open`).
Another browser on the same computer can pair with that code too.

## First run

With no model configured (no `[control]` in `config.toml`), Home draws the
forms of Settings › Models, one after the other (ADR 41), and no task starts
until they are done. First the provider, from the presets Settings has
(Ollama, OpenAI, Anthropic, Baseten, OpenRouter, Cerebras, or another server),
started on Ollama, on this computer or another one on the network, which needs
no key. Then the model, on the provider just saved: the models it lists are
offered as buttons (a long list is typed to), and **Save and use it** saves
the model and makes it the default for new tasks. The efforts it takes (the
provider's own, as published, when it says), what it is for and the effort it
starts at wait under a closed **More** fold, as the provider's name does in
the provider form; neither form asks for anything that does not have to be
answered before a task works. Its context length, prices and the rest of its settings are
what its provider publishes, and Settings › Models changes them, adds more
providers and models, and chooses another default (the manual, "Choosing
models"). The provider is written to a `[servers.NAME]` table of `config.toml`,
the model to a `[models.NAME]` table and the choice to `[control]`; every other
line of an existing file is kept. An API key is never written there: it goes into
the encrypted keychain, and a binding row allows it only for that server's
host, `POST` (and `GET` for its model list), as a bearer `Authorization` header
(Anthropic's: its `x-api-key` header). A model with a key must be
reached over HTTPS, unless it runs on this computer. A provider that wants a key
is not saved until there is a keychain to hold it, so the provider form shows the
keychain form beside it: a passphrase (at least 8 characters, not recoverable),
or, if you tick "Don't protect it with a passphrase", none (not recommended:
see below). What was typed in the provider form is kept while the keychain is
made. Settings makes one too, and sets or changes its passphrase.

When a keychain with a passphrase exists, a new app starts with it locked;
Home offers to unlock it. While the model's own key is in a locked keychain,
no task can be opened, resumed or run: the app asks you to unlock first, so
no task starts without the key it needs. Tasks opened after unlocking can use
every key in it. A keychain without a passphrase is opened as the app starts,
and nothing asks. It is still an encrypted file, but its key comes from the
empty passphrase, so it protects nothing its file permissions (0600, your
user) do not: anyone who can read the file, or a backup of it, can use every
key in it. Settings says so while it has none, and sets, changes or removes
the passphrase (changing or removing one asks for the current one first).

Connections › Credentials manages the keys themselves: what the keychain
holds, where each may be sent and what uses it; adding a key and allowing it
for a host, replacing a rotated one, connecting an OAuth2 account, and
removing or revoking one ([browser.md](browser.md), "Credentials"). Settings,
keychain and credential changes are available only to a console on this
computer, never to a paired remote device.

## The macOS app

```sh
make app          # bin/Nodwyn.app — universal (Apple silicon and Intel), ad-hoc signed
```

It needs Xcode's command line tools (for cgo and WebKit). The bundle is the
same `nodwyn` binary built with the `webview` tag and Wails v3; started from Finder or the
Dock it runs `nodwyn app`, takes the login shell's `PATH` (so tools installed
with Homebrew are found), and writes its diagnostics to
`~/.local/share/nodwyn/app.log`. Everything else — configuration, keychain,
sessions — lives where the command-line nodwyn keeps it (`nodwyn paths`), per user.

### Sharing it with family

1. `make app`, then zip `bin/Nodwyn.app` (Finder: Compress) and send it.
2. They move `Nodwyn.app` to Applications. The first time, macOS says it is from
   an unidentified developer: **right-click → Open → Open** (once). The app is
   signed only ad hoc; signing with a Developer ID and notarizing is what would
   remove that step.
3. On first launch they choose a provider and a model (see First run). For a model
   running on your machine, give its address on your network (for example
   `http://workshop:11434`, over Tailscale); nothing leaves their computer until
   they do.

Each person's tasks, keys and settings stay in their own account.

### Linux and other systems

```sh
make build-webview   # bin/nodwyn with the webview tag
```

On Linux it needs cgo and WebKitGTK (`libwebkit2gtk-4.1-dev` on Debian and
Ubuntu, `webkit2gtk4.1-devel` on Fedora), or GTK4 with WebKitGTK 6.0.
Wails supports WebView2 on Windows, but Nodwyn's host still requires a Windows
port of its Unix process, locking and local IPC code; no Windows package is
published yet.
Without the tag, `nodwyn app` opens the default browser instead and works the
same; started from a desktop launcher, with no terminal for its messages, it
has no window of its own and is easy to take for an app that did not start.

So `make promote` builds `nodwyn-stable` with the tag on Linux and macOS, and
refuses to install one whose `go version -m` does not show `-tags=webview`.
On a Linux system without WebKitGTK it builds without the tag and says so,
before the acceptance run and again at the end.

`scripts/webview-env.sh tags` selects `webview` for GTK4 or macOS, and
`webview,gtk3` for GTK3. `make build-webview` uses that selection. Wails
uses the real WebKitGTK package; no pkg-config alias is needed.

The desktop dependency is pinned to Wails `v3.0.0-beta.28`. Version 3 is
still beta; its direct window URL API lets the app keep the existing
loopback console, pairing tickets, cookies, CSP and WebSocket origin checks.
We do not serve the console through Wails' asset server or expose task
methods as Wails services. ADR 44 records the choice.

To run the native window check in a desktop session:

```sh
NODWYN_TEST_DESKTOP=1 go test -tags "$(scripts/webview-env.sh tags)" ./cmd/nodwyn -run '^TestWailsWindowRoundTrip$' -count=1
```

## What the window adds

The page gets three functions from the window and nothing else: a native
folder chooser (for adding a folder on Home or Workspaces), system
notifications (the same generic "nodwyn needs your answer", opt-in in
Settings), and opening a link in the default browser. None of them reads or
writes a file the page names.

The native bridge accepts only the loopback window's origin and a random
per-window capability injected into its top frame. GTK reports a top-level
URI without a frame identity; the capability covers that missing metadata.
Other platforms' frame metadata is checked when present. Replies also check
the current origin so navigating elsewhere cannot receive a selected path.
Notifications carry only the fixed attention notice, and external links must
be HTTP(S). The browser version receives no desktop capability.
