# Downloadable releases

[Gitea releases](http://git.example.com/fc/nodwyn/releases)
contain one `build-<12-character commit>` release for each successfully checked
push to `main`. The release names its full source commit. The repository is
private, so sign in to download. GitHub CI is manual-only; nothing publishes
releases to GitHub.

Each release has eight attachments:

| Attachment | Contents |
| --- | --- |
| `nodwyn-linux-amd64.tar.gz` | Linux x86-64 executable and Linux x86-64 egress proxy |
| `nodwyn-linux-arm64.tar.gz` | Linux ARM64 executable and Linux ARM64 egress proxy |
| `nodwyn-darwin-amd64.tar.gz` | macOS Intel executable and Linux x86-64 egress proxy |
| `nodwyn-darwin-arm64.tar.gz` | macOS Apple Silicon executable and Linux ARM64 egress proxy |
| `nodwyn.pdf` | The design book, built from the release's source |
| `expert.pdf` | The expert guide, built from the same source |
| `SOURCE.json` | Full commit, Go version, targets and build limitations |
| `SHA256SUMS` | SHA-256 digests of the other seven attachments |

Download the archive for your computer and `SHA256SUMS`. Check its digest with
`sha256sum nodwyn-linux-amd64.tar.gz` (Linux), or
`shasum -a 256 nodwyn-darwin-arm64.tar.gz` (macOS), and compare the full digest
with its entry in `SHA256SUMS`. Extract the archive and keep its two executables
beside one another in a directory on your `PATH`. `nodwyn version` prints the
full source commit. These checksums detect damaged or mismatched downloads;
they are not an independent signature of the release publisher.

The builds have cgo disabled. `nodwyn app` opens the browser, rather than a
native webview window. macOS binaries are unsigned and not notarised. Only
Linux amd64 is executed in CI, including the fake-model selfcheck; the other
three targets are cross-compiled. The egress proxy is always a Linux binary
because it runs inside the MCP server's Linux container. Use a container image
with the same architecture as the archive.

## How publication works

The read-only `check` job in `.gitea/workflows/ci.yml` runs acceptance tests,
publisher tests and all four portable builds. Only a successful push or manual
workflow run on `main` can start the `release` job. That job has the Gitea
job token's `contents: write` permission; the token is passed only to the
publisher step. Pull requests and other branches do not publish.

`scripts/release.py build DIRECTORY` refuses tracked changes and an occupied
output directory. It archives the source commit to scratch, builds binaries
with that commit as their version, runs the Linux amd64 selfcheck, and builds
both books from their LaTeX sources there. Generated PDFs are not tracked;
the builder does not reuse local copies or dirty the original checkout.
Archive ownership and timestamps are normalised; `SOURCE_DATE_EPOCH` comes
from the commit. This is not a promise of identical output across Go or TeX
versions, which can differ between runners.

The publisher creates a draft and uploads all eight attachments. It downloads
them again, checks their inventory, checksums and source commit, and only then
publishes. Failed uploads leave a draft, not an apparently complete release.
Requests do not follow redirects or send the token to another origin.

To retry a failed release, rerun the Gitea workflow for that commit. Matching
draft attachments are reused; differing attachments in that same unpublished
draft are replaced. A published release is never overwritten: a rerun verifies
its existing downloads. An unrelated release or unexpected attachment causes
a refusal. There is no release retention or deletion job.

For a local build on Linux amd64, use an empty output directory:

```sh
python3 -B -m unittest discover -s test/release -v
python3 scripts/release.py build /tmp/nodwyn-release-assets
```

The builder needs Go 1.26, Python 3.12 or newer, bash, make, latexmk and the TeX
packages used by both books (including Libertine and Inconsolata). The CI job
installs these on its Alpine runner. The publisher's inputs are `GITEA_URL`,
`GITEA_REPOSITORY`, `GITEA_TOKEN` and `RELEASE_COMMIT`; CI supplies them. Do not
put a token in a command line or commit it to the repository.

## Books and the history cleanup

Git stores the LaTeX sources and `docs/book/source/catalogue.tex`, not generated
PDFs. Download `nodwyn.pdf` and `expert.pdf` from a release, or run `make book`
and `make expert` to build ignored copies in `docs/book/` and `docs/expert/`.
After changing tracked Go or Scheme files, run
`python3 docs/book/source/generate.py` and commit the updated catalogue; the
release builder's `--check` refuses a stale file inventory or line count.

The authorised cleanup removes `docs/book/nibb.pdf`, `docs/book/nodwyn.pdf`
and `docs/expert/expert.pdf` throughout Gitea's branches and tags. It retains
every other file, commit metadata and parent relationship, including commits
that become empty. A full Git bundle and old-to-new commit map are retained
outside the repository before updating the remote with exact ref leases.
Release attachments are not deleted or replaced.

Rewritten commits have new IDs, and signatures on rewritten commits and tags
cannot authenticate those new IDs. Existing release names, binary versions
and `SOURCE.json` still record the original build commit; use the saved commit
map or backup bundle to trace that provenance. Their tag names now identify
the corresponding source without PDFs. New releases use the rewritten IDs.

Use a fresh Gitea clone for new work after the rewrite. Keep an old clone until
any unpushed work has been recovered, then transplant those changes onto the
new history; do not merge or push its old history back. Other local worktrees
and GitHub are not rewritten by the Gitea cleanup. Server disk reclamation
depends on Gitea's garbage collection, while fresh clones receive only the
reachable, cleaned history.

The permission model follows [Gitea's job-token documentation](https://docs.gitea.com/usage/actions/token-permissions/).
