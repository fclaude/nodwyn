# Install

nodwyn runs on Linux and macOS. The host is written in Go, and the agent's loop and prompts are Scheme. There's no Windows build yet.

## From a release

The [releases page](https://github.com/fclaude/nodwyn/releases) has an archive for each system: Linux or macOS, amd64 or arm64. Each archive holds `nodwyn`, a helper called `nodwyn-egress`, and the licence files. Download the archive and `SHA256SUMS`, check the digest, and put both programs next to each other on your `PATH`:

```sh
sha256sum nodwyn-linux-amd64.tar.gz        # compare with its line in SHA256SUMS
tar xzf nodwyn-linux-amd64.tar.gz
mkdir -p ~/.local/bin
install nodwyn nodwyn-egress ~/.local/bin/
nodwyn version
```

This example is for Linux amd64. Use your archive's name instead; on macOS, use `shasum -a 256` to compute its digest. Make sure the directory you install into is on your `PATH`. The checksum catches a damaged or mismatched download; it doesn't prove who published it.

The helper is only for MCP servers that run in containers, where it is the one way out to the network. It is always a Linux program, because it runs inside the container, so a macOS archive carries the Linux one for the same architecture.

On a Mac, install the app instead: each release has `Nodwyn-macos-universal.zip`, the app with its own window and icon for Apple silicon and Intel, and `brew install --cask fclaude/fclaude-packages/nodwyn` installs it and puts its `nodwyn` on your `PATH`. It is signed only ad hoc and not notarised, so the first launch needs a right-click and Open.

The portable archives have no native window. Their `nodwyn app` opens your browser instead, which is the same console. The macOS binaries aren't signed or notarised, so the first launch needs a right-click and Open.

## From source

You need Go 1.26 or newer.

```sh
git clone https://github.com/fclaude/nodwyn
cd nodwyn
make build          # bin/nodwyn and bin/nodwyn-egress
```

`make build-webview` builds the same program with its own window. On Linux that needs cgo and WebKitGTK (`libwebkit2gtk-4.1-dev` on Debian and Ubuntu, `webkit2gtk4.1-devel` on Fedora). On macOS, `make app` makes `bin/Nodwyn.app`. [The app page of the manual](../reference/app.html) has the details for each system.

`go install github.com/fclaude/nodwyn/cmd/nodwyn@latest` gets you the program alone, which is fine unless you run MCP servers in containers.

## Where things go

`nodwyn paths` prints where your settings and your tasks live. Settings and the encrypted keychain are under `~/.config/nodwyn`, and each task's log is under `~/.local/share/nodwyn/sessions`. A task's log is the task: resuming one reads its log.

Once it's installed, [the first ten minutes](first-run.html) connects a model.
