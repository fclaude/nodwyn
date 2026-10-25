# Contributing to nodwyn

Thanks for wanting to help. This page is what I'd tell you across a desk: how
to get the code running and what to check before you send a change. [AGENTS.md](AGENTS.md) has the same
rules in full, written for coding agents (which includes nodwyn itself), so
it's the place to look when this page is vague.

By sending a change you license it under the project's
[Apache 2.0 licence](LICENSE), as section 5 of that licence says. There's no
contributor agreement to sign.

## Before you start

For a bug, an issue with how to reproduce it is the best start. For anything
bigger than a fix, please open an issue first and say what you have in mind. The
design is held together by a few invariants (see [SPEC.md](SPEC.md)), and I'd
rather tell you early that a change cuts across one than after you've written it.
Security problems don't go in public issues: see [SECURITY.md](SECURITY.md).

## Building and testing

You need Go 1.26 or newer. Node and Playwright's Chromium are only for the
console's browser checks.

```sh
make build        # bin/nodwyn and bin/nodwyn-egress
make test         # go test -race ./...
make lint         # go vet, staticcheck, the boundary and ingress lints
make accept       # lint, tests and the exfiltration suite: what CI runs
make browser      # the console's checks in headless Chromium
make validate     # what your change needs, given the files it touched
```

None of it needs a paid model, a real key or a passphrase. The tests use the
fakes in `test/fakes` (an Ollama server, a judge, an MCP server, an OAuth
provider and so on), and a test that asks a scripted model for more than its
script holds fails. Please keep it that way.

`make validate` is the one to run last. It works out which of the checks above
your diff calls for, runs each Go test you added or changed against the
unchanged code too (a test that passes on both doesn't test your fix), and
points out the usual things a diff can show. Say in your pull request what it
printed, and what you didn't run.

## What a change carries

A change should come with everything that belongs to it:

- Tests. A fix has one that fails without it.
- For the console (`internal/frontend/web/assets`), its browser check in
  `test/browser/checks`, extended to the new state and doing the new action,
  since a check that only finds an element passes on a feature that does nothing.
- If you changed `internal/frontend/rpc`, run `go generate ./internal/frontend/rpc`
  to rewrite `docs/protocol.schema.json`, and describe new methods in
  `docs/protocol.md`.
- A new Scheme primitive is documented in `docs/scheme.md`, and listed in the
  primitive tables of `docs/book/parts/scheme.tex` and
  `docs/expert/parts/09-primitives.tex`.
- A change to the prompts or the default loop (`internal/loop/*.scm`) runs the
  `internal/core` tests. The loop has a lean and a full profile (AGENTS.md
  explains them), and a rule you add says in a comment which failure it comes
  from.
- The website is built from `site/` and the docs by `python3 site/build.py` (it
  needs `pip install -r site/requirements.txt`), which fails on a broken link.
  If you changed how the console looks, `scripts/screenshots.sh` takes the
  screenshots again.
- Behaviour a person sees goes in `docs/manual.md`, and what each client can do in
  `docs/clients.md`.
- A decision goes in `docs/adr`, as a new record listed in `docs/adr/README.md`
  or as an amendment to the one it changes.
- After you change tracked Go or Scheme files, run
  `python3 docs/book/source/generate.py` and commit the updated catalogue. It
  lists the files git tracks, so `git add -N` a new file first.

Please don't edit `scripts/validate`, the `Makefile` or `.nodwyn/init.scm` to make
`validate` pass. `make promote` and `nodwyn-stable` are how I install my own
builds, and you can ignore them.

Anything near the guard, the denylist, the bindings, the keychain, egress or
redaction needs `make exfil` to pass, and a check is never loosened to make a
test go green. A refusal from the guard is the answer; don't reach the same
file another way.

## Style

Comments are full sentences that say why, at about the density of the code
around them. Spelling is British ("behaviour"). Add nothing the change doesn't
use: no helper or field for a step that nothing calls yet.

A commit's subject is a sentence saying what is now true ("Browser checks exit
when they are done"). The body says why, with the evidence: what you ran and
what it showed.

## Changing the language

The Scheme interpreter lives in [Brote](https://github.com/fclaude/brote), not
here. A new form, builtin or reader change is made and tested there, and then
pinned in this repository's `go.mod`.
