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
  socket, so `nodwyn tui --socket …` or `nodwyn ui` can attach to it meanwhile.
  Closing the app closes those tasks; their conversations are kept and resume
  later like after any daemon shutdown.

The page the app opens pairs itself with a one-time launch ticket carried in
the address's fragment (never sent to a server, removed from the address bar
at once, valid for a minute). Another browser on the same computer can still
pair with the code the app prints to its log.

## First run

With no model configured (no `[control]` in `config.toml`), Home asks which model to use: an Ollama server (on this
computer or another one on the network) or any OpenAI-compatible server. Once
one is set up, Settings › Models adds more providers and models, and chooses
the default model and its effort (the manual, "Choosing models"). The
choice is written to the `[control]` table of `config.toml`; every other line
of an existing file is kept, and saving the same model on the same server
again keeps its hand-tuned settings (`num_ctx`, `temperature`, `seed`, prices, …). An API key is never written there: it goes into
the encrypted keychain, and a binding row allows it only for that server's
host, `POST`, as a bearer `Authorization` header. A model with a key must be
reached over HTTPS, unless it runs on this computer. A keychain is created from
Settings with a passphrase (at least 8 characters, not recoverable), or, if
you tick "Don't protect it with a passphrase", without one (not recommended:
see below).

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
same `nodwyn` binary built with the `webview` tag; started from Finder or the
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
3. On first launch they choose the model. For a model running on your machine,
   give its address on your network (for example `http://workshop:11434`, over
   Tailscale); nothing leaves their computer until they do.

Each person's tasks, keys and settings stay in their own account.

### Linux and other systems

```sh
make build-webview   # bin/nodwyn with the webview tag
```

On Linux it needs cgo and WebKitGTK (`libwebkit2gtk-4.1-dev` on Debian and
Ubuntu, `webkit2gtk4.1-devel` on Fedora); on Windows the tag needs WebView2.
Without the tag, `nodwyn app` opens the default browser instead and works the
same; started from a desktop launcher, with no terminal for its messages, it
has no window of its own and is easy to take for an app that did not start.

So `make promote` builds `nodwyn-stable` with the tag on Linux and macOS, and
refuses to install one whose `go version -m` does not show `-tags=webview`.
On a Linux system without WebKitGTK it builds without the tag and says so,
before the acceptance run and again at the end.

The webview package asks pkg-config for `webkit2gtk-4.0`, which current
distributions no longer ship; `webkit2gtk-4.1` has the same API, built on
libsoup 3. `scripts/webview-env.sh run` bridges the name for the length of
one build: when only 4.1 is installed, it writes a `webkit2gtk-4.0.pc` that
requires 4.1 into a temporary directory at the front of `PKG_CONFIG_PATH`. A
system that has 4.0 builds against 4.0. This keeps the module as published:
patching its cgo line would mean vendoring it, or keeping a fork, and
carrying the change through every update. To build by hand:

```sh
CGO_ENABLED=1 scripts/webview-env.sh run go build -tags webview ./cmd/nodwyn
```

## What the window adds

The page gets three functions from the window and nothing else: a native
folder chooser (for adding a folder on Home or Workspaces), system
notifications (the same generic "nodwyn needs your answer", opt-in in
Settings), and opening a link in the default browser. None of them reads or
writes a file the page names.
