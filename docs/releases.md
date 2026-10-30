# Downloadable releases

The [GitHub Releases page](https://github.com/fclaude/nodwyn/releases) has the
versioned releases, starting with `v0.1.0`. A release is made by hand: nothing
a push does publishes one. Each release names the full commit it was built
from.

Every release has nine files:

| File | What's in it |
| --- | --- |
| `Nodwyn-macos-universal.zip` | The macOS app, `Nodwyn.app`: Apple Silicon and Intel in one bundle, with its own window and icon, and the Linux egress proxies |
| `nodwyn-linux-amd64.tar.gz` | The Linux x86-64 executable and its egress proxy, with `LICENSE` and `NOTICE` |
| `nodwyn-linux-arm64.tar.gz` | The same for Linux ARM64 |
| `nodwyn-darwin-amd64.tar.gz` | The macOS Intel executable, and a Linux x86-64 egress proxy |
| `nodwyn-darwin-arm64.tar.gz` | The macOS Apple Silicon executable, and a Linux ARM64 egress proxy |
| `nodwyn.pdf` | The design book, built from the release's source |
| `expert.pdf` | The expert guide, built from the same source |
| `SOURCE.json` | The full commit, the Go version, the targets and what the build leaves out |
| `SHA256SUMS` | SHA-256 digests of the other eight files |

Download the archive for your computer and `SHA256SUMS`. Check the digest with
`sha256sum nodwyn-linux-amd64.tar.gz` on Linux, or
`shasum -a 256 nodwyn-darwin-arm64.tar.gz` on macOS, and compare it with the
matching line of `SHA256SUMS`. Extract the archive and keep its two
executables next to each other in a directory on your `PATH`. `nodwyn version`
prints the full commit. The checksums catch a damaged or mismatched download.
They don't prove who published the release.

On a Mac, the app is the one to install: unzip it, move `Nodwyn.app` to
Applications, and open it with a right-click and **Open** the first time, since
it is signed only ad hoc and not notarised. Its `Contents/MacOS/nodwyn` is the
whole command line too. Homebrew installs it, and links that `nodwyn` onto
your `PATH`, with `brew install --cask fclaude/fclaude-packages/nodwyn`.

The portable archives have cgo off, so their `nodwyn app` opens your browser;
there's no native window. The macOS binaries are unsigned and not notarised. CI runs the tests and
executes only the Linux amd64 build, and cross-compiles the other three. The
release build also runs the fake-model selfcheck on Linux amd64. The egress proxy is always a Linux binary because it runs inside
the MCP server's Linux container, so use a container image with the same
architecture as the archive.

## How a release is made

`ci.yml` runs on every push to `main` and every pull request: the acceptance
checks, the release builder's tests, `govulncheck`, the four portable builds,
the macOS app on a macOS runner (`scripts/app-macos.sh`, which also runs the
built app's `nodwyn version`) and the browser checks. It has no write
permission.

`release.yml` is started by hand from the Actions tab, on `main`, with a
version such as `v0.1.0`. It runs `ci.yml` first. A macOS runner then builds
the app with `scripts/app-macos.sh`, the full commit as what `nodwyn version`
prints and the release's number as the version Finder shows, and runs the
Apple Silicon half. The Linux runner builds the rest with
`scripts/release.py build`, which takes the app only whole, universal, built
from this commit and numbered as this release, which refuses tracked changes and an output
directory that isn't empty, archives the source commit into scratch, builds the
binaries with that commit as their version, runs the Linux amd64 selfcheck and
builds both books from their LaTeX sources. Archive ownership and timestamps are
fixed, and `SOURCE_DATE_EPOCH` comes from the commit. That doesn't promise
identical output across Go or TeX versions.

The workflow uploads the nine files to a draft release, downloads them again,
and runs `scripts/release.py verify`: the draft has those files and no others,
each matches `SHA256SUMS`, `SOURCE.json` names this commit and this version,
and the app passes the same check as at build.
Only then does it publish. If a release or a tag with that version already
exists, the run stops, so a version belongs to one commit for good. A run that
fails after it made the draft deletes that draft, and it never deletes a
published release.

To build the same files on your own Linux amd64 machine, use an empty directory:

```sh
python3 -B -m unittest discover -s test/release -v
scripts/app-macos.sh "$(git rev-parse HEAD)" 0.1.0    # on a Mac
python3 scripts/release.py build /tmp/nodwyn-release-assets --version v0.1.0 \
  --app bin/Nodwyn-macos-universal.zip
```

You need Go 1.26, Python 3.12 or newer, bash, make, latexmk and the TeX packages
both books use (including Latin Modern, Libertine and Inconsolata). The release
workflow installs `lmodern` explicitly so the design book's `lmodern.sty` is
available when recommended packages are disabled.

## Books

Git keeps the LaTeX sources and `docs/book/source/catalogue.tex`. The generated
PDFs are ignored. Download `nodwyn.pdf` and `expert.pdf` from a release, or run
`make book` and `make expert` to build ignored copies in `docs/book/` and
`docs/expert/`. After you change tracked Go or Scheme files, run
`python3 docs/book/source/generate.py` and commit the new catalogue; the release
builder's `--check` refuses a stale file inventory or line count, and
`make validate` refuses it first, counting new files git doesn't track yet.
