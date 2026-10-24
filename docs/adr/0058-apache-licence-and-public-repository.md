# ADR 0058: Apache 2.0, and a public repository on GitHub

- Status: accepted; implemented 2026-10-23. Amends 42 (releases move to
  GitHub).
- Date: 2026-10-23

## Context

nodwyn was built in a private repository on a private Gitea, with CI on a
self-hosted runner and releases and the books reachable only from there.
`docs/maintenance-review.md` left the licence as "a separate release
decision". Without a licence, published code is readable and not usable: nobody
may copy, build on or redistribute it. The tree and its history also named
machines and paths that mean nothing to anyone else, and carried a 13 MB demo
binary and an old WebAssembly build that no longer have a use.

## Decision

1. **The licence is Apache 2.0.** `LICENSE` is the standard text and `NOTICE`
   names the copyright holder and the data and code that come from others.
   Apache 2.0 matches Brote's spirit (a permissive licence), carries an
   explicit patent grant, which suits a tool whose users hand it credentials,
   and lets people use the code at work without a lawyer. A contribution is
   licensed the same way by section 5 of the licence, so there is no
   contributor agreement. Source files carry no per-file header: one licence
   covers the repository, and 700 commits of headers would add noise nobody
   reads. A header goes in a file that is copied somewhere else.
2. **GitHub is the home.** `github.com/fclaude/nodwyn` is the module path in
   `go.mod`, so `go install` resolves there. CI (`ci.yml`) runs on hosted
   runners for every push to `main` and every pull request: `make accept`, the
   release builder's tests, `govulncheck`, the four portable builds and the
   browser checks. Releases are `release.yml` (amends 42). The Gitea workflows
   and the package dispatch are removed.
3. **The history is rewritten once, before it is published.** In a copy of the
   repository, not in place: private hostnames, personal home paths, the
   names of the owner's machines and the CI runner's label are replaced everywhere,
   in files and in commit messages; the demo binary and the WebAssembly builds
   are dropped; only `main` is kept, with no tags, so the first public
   `v0.1.0` is cut by `release.yml` from the cleaned history; and every
   commit is signed again with the maintainer's SSH key, because rewriting
   invalidates the signatures the PDF cleanup (ADR 42) gave them. The new
   signatures attest to the rewritten commits, not to the originals. Commit hashes cited in the docs name the
   rewritten commits. Authors and dates are untouched.
4. **The README is for a newcomer.** It says what nodwyn is, how to install
   it and how to start. The long reference that used to open it is
   `docs/reference.md`, and the package map is in the manual.
5. **Outsiders get the usual files:** `CONTRIBUTING.md` (AGENTS.md's rules,
   said to a contributor), `SECURITY.md` (private reports, and what the
   project does and does not promise about secrets), `CODE_OF_CONDUCT.md`
   (Contributor Covenant 2.1) and issue and pull request templates.
6. **The model catalogue carries models.dev's notice.** `models.json` is a
   snapshot of models.dev's data (MIT). Its licence text is in
   `internal/frontend/web/licenses/models-dev.txt`, and `nodwyn licenses`
   prints it.

## Consequences

Anyone can build, use and fork nodwyn, and can verify that a release is the
source it says. The old object IDs are gone: the private repository keeps the
originals, and nothing public refers to them. Release builds are now checked
on a runner nobody here controls, which finds what the self-hosted one hid,
and it costs a first run's worth of fixing. Books build from LaTeX on Ubuntu's
TeX packages, not Alpine's.

## Not built

- Per-file SPDX or licence headers (decision 1).
- Signed releases. Checksums detect damage and not a
  malicious publisher. Build provenance attestation is the next step if people
  want it.
- A Windows build, a package for any distribution and a Homebrew tap.
- The workflows have not run on a hosted runner at the time of writing: the
  first run of `ci.yml` and of `release.yml` is their test.
