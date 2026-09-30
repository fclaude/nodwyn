# nibb as an app

`nibb app` is the whole product in one program (ADR 30): a window with the
console, and — when no daemon is already running — the daemon itself, in the
same process. Nobody has to start `nibb daemon`, copy a pairing code or unlock
a keychain at a terminal.

```sh
nibb app              # a window (webview build) or the default browser
nibb app --browser    # the default browser even in the webview build
nibb app --no-open    # print the launch address instead of opening it
```

- **A daemon is running** (your usual `nibb daemon`): the app attaches to it.
  Closing the app leaves its tasks running, as closing `nibb ui` does.
- **No daemon is running**: the app serves tasks itself on the standard
  socket, so `nibb tui --socket …` or `nibb ui` can attach to it meanwhile.
  Closing the app closes those tasks; their conversations are kept and resume
  later like after any daemon shutdown.

The page the app opens pairs itself with a one-time launch ticket carried in
the address's fragment (never sent to a server, removed from the address bar
at once, valid for a minute). Another browser on the same computer can still
pair with the code the app prints to its log.

## First run

With no `config.toml`, Home asks which model to use: an Ollama server (on this
computer or another one on the network) or any OpenAI-compatible server. The
choice is written to the `[control]` table of `config.toml`; every other line
of an existing file is kept. An API key is never written there: it goes into
the encrypted keychain, and a binding row allows it only for that server's
host, `POST`, as a bearer `Authorization` header. A keychain is created from
Settings with a passphrase (at least 8 characters, not recoverable).

When a keychain exists, a new app starts with it locked; Home offers to unlock
it. Tasks opened after unlocking can use its keys; tasks opened before keep
running without them. Settings and keychain changes are available only to a
console on this computer, never to a paired remote device.

## The macOS app

```sh
make app          # bin/Nibb.app — universal (Apple silicon and Intel), ad-hoc signed
```

It needs Xcode's command line tools (for cgo and WebKit). The bundle is the
same `nibb` binary built with the `webview` tag; started from Finder or the
Dock it runs `nibb app`, takes the login shell's `PATH` (so tools installed
with Homebrew are found), and writes its diagnostics to
`~/.local/share/nibb/app.log`. Everything else — configuration, keychain,
sessions — lives where the command-line nibb keeps it (`nibb paths`), per user.

### Sharing it with family

1. `make app`, then zip `bin/Nibb.app` (Finder: Compress) and send it.
2. They move `Nibb.app` to Applications. The first time, macOS says it is from
   an unidentified developer: **right-click → Open → Open** (once). The app is
   signed only ad hoc; signing with a Developer ID and notarizing is what would
   remove that step.
3. On first launch they choose the model. For a model running on your machine,
   give its address on your network (for example `http://workshop:11434`, over
   Tailscale); nothing leaves their computer until they do.

Each person's tasks, keys and settings stay in their own account.

### Other systems

`go build -tags webview ./cmd/nibb` needs WebKitGTK on Linux (the
`libwebkit2gtk-4.1-dev` package) and WebView2 on Windows. Without the tag,
`nibb app` opens the default browser and works the same.

## What the window adds

The page gets three functions from the window and nothing else: a native
folder chooser (for Home's **Add a folder**), system notifications (the same
generic "nibb needs your answer", opt-in in Settings), and opening a link in
the default browser. None of them reads or writes a file the page names.
