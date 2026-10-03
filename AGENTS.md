# Working on nodwyn

nodwyn is a coding agent whose behaviour is Scheme on a Go host: `cmd/` the
binaries, `internal/` the host (guard, keychain, bindings, egress, the loop's
window, the JSON-RPC frontend and the console in
`internal/frontend/web/assets`), `internal/loop/*.scm` the default prompts and
loop, `docs/` the decisions (`docs/adr`), the Scheme reference
(`docs/scheme.md`), the protocol, the manual, the book and the expert guide,
`test/` the exfiltration suite, fakes, browser checks and session fixtures.

## Validating a change

A change is done when it has been run where it runs. A build, `gofmt` or
`node --check` shows code is well formed, not that it works; say in your
answer what you ran and what you did not.

- **Every Go change:** `go build ./...`, `gofmt -l internal cmd test` (prints
  nothing), `go vet ./...`, and the whole package of each file you changed:
  `go test ./internal/<pkg>/`.
- **Before calling it finished:** `make accept` (go vet, staticcheck, the
  boundary and ingress lints, `go test -race ./...`, the exfiltration suite).
  It is what promotion runs.
- **The console** (`internal/frontend/web/assets`: JS, CSS): Go's tests do not
  run it. Run its browser check, `bash test/browser/run.sh <check>` (`screens`,
  `approve`, `firstrun`, `remote`, `mcp`, `credentials`; screenshots land in
  `test/browser/out`), and extend the check to the states your change adds: a
  setting off, after a save, the new action, the page that links to it. If
  Chromium is missing: `cd test/browser && npx playwright install chromium`.
  To look by hand: `go run ./test/browser/demo -seed` prints a launch URL.
- **RPC types** (`internal/frontend/rpc`): `go generate ./internal/frontend/rpc`
  rewrites `docs/protocol.schema.json` (`TestWireSchemaMatchesContract` fails
  until you do); new methods and fields go in `docs/protocol.md`.
- **Scheme primitives:** each is documented in `docs/scheme.md`
  (`TestSessionPrimitivesAreDocumented`), and the primitive tables in
  `docs/book/parts/scheme.tex` and `docs/expert/parts/09-primitives.tex`
  list it.
- **Prompts and the default loop** (`internal/loop/*.scm`): the
  `internal/core` tests. The working rules were measured on a benchmark
  (ADR 32): a rule you add says, in a comment and an ADR 32 amendment, the
  failure it comes from.
- **Guard, denylist, bindings, keychain, egress, redaction:** `make exfil`.
  Never loosen a check to make a test pass.
- **The book and the expert guide:** `make book`, `make expert` (texlive). If
  they cannot build here, say so.

## Rules of the house

- Tests and checks never use a paid model, a real key or a passphrase: the
  fakes in `test/fakes` (ollama, jev, mcp, oauth, drive, container) stand in.
  `make smoke` talks to the real control plane; run it only when asked.
- Commit, push and `make promote` only when asked. A commit's subject is a
  sentence saying what is now true ("Browser checks exit when they are
  done"); its body says why, with the evidence. No AI attribution lines
  (Co-Authored-By, Generated with).
- A change carries everything that belongs with it: code, tests, the
  regenerated schema, and docs. A decision goes in `docs/adr`, as a new ADR
  listed in `docs/adr/README.md` or an `## Amendment YYYY-MM-DD: ...` section
  of the ADR it changes. Behaviour a person sees goes in `docs/manual.md` and
  wherever the book or the expert guide describe it.
- Add nothing the change does not use: no helper, field or export for a
  plan step that nothing calls or reads.
- Asked to propose first: propose and stop, and do not write the proposal
  into the repository. Told to go ahead without answers to your questions:
  choose, and name what you chose in your answer.
- A refusal from the guard or the denylist is the answer. Do not reach the
  same file another way, through the shell or a link.
- Never apply, pop or drop a stash you did not make. Stash your work to
  compare with the unchanged code only when a test fails.
- Comments are full sentences that say why, at the density of the code
  around them. Spelling is British, as in the code ("behaviour").

## The console's traps

- There is no build step: ES modules are served as they are. `h()` in
  `dom.js` builds every node; strings become text, never markup, since session
  content is untrusted (`docs/browser.md`).
- A render that throws leaves the tab blank, and module-level state
  (`let selected`, ...) outlives navigating away, so a broken state lasts until
  a reload. A local name that shadows a module function (`const detail`
  beside `function detail`) is valid syntax and fails only when called.
- `desk.settings` and the rest of the desk are read once per connection. A
  page that changes what another page shows refreshes it.

## When nodwyn runs its own tests

TMPDIR is then the session's scratch directory, inside nodwyn's protected data
directory, and commits may find no signing agent. Packages whose tests build a
guard or commit isolate HOME with `test/fakes/home`; a new such package does
the same in its `TestMain`. A test that fails on the unchanged code too is not
yours: say so rather than fix around it.
