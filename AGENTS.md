# Working on nodwyn

nodwyn is a coding agent whose behaviour is Scheme on a Go host: `cmd/` the
binaries, `internal/` the host (guard, keychain, bindings, egress, the loop's
window, the JSON-RPC frontend and the console in
`internal/frontend/web/assets`), `internal/loop/*.scm` the default prompts and
loop, `docs/` the decisions (`docs/adr`), the Scheme reference
(`docs/scheme.md`), the protocol, the manual, what each client can do
(`docs/clients.md`, read it before closing a gap between clients rather than
surveying the code), the book and the expert guide,
`test/` the exfiltration suite, fakes, browser checks and session fixtures.
The Scheme interpreter is not in this repository: it is the module
`github.com/fclaude/brote` (ADR 7), public, so a stock Go environment fetches
it; a change to the language (a form, a builtin, the reader,
the primitive table) is made in Brote, tested there, and then pinned here with
`go get github.com/fclaude/brote@<tag or commit>`.

## Validating a change

A change is done when it has been run where it runs. A build, `gofmt` or
`node --check` shows code is well formed, not that it works; say in your
answer what you ran and what you did not. A sentence in your answer saying
something works names the run or the check that showed it: what the code
you wrote should do is not evidence.

**Before calling work done, run `validate`** (the tool where it is offered;
otherwise `make validate` in a shell) after your last edit (ADR 37). It runs what the list below
asks for the files you changed (`gofmt`, `make accept`, the browser checks,
the books' source inventory), runs each Go test you added or changed
against the unchanged code, and checks the house rules a diff can show. Fix what fails, and quote
its summary in your answer, answering each warning: a test that passes on
the unchanged code is fixed or explained. A failure the unchanged code has
too is not yours: say so. Never change `scripts/validate`, the `Makefile`
or `.nodwyn/init.scm` to make it pass. While you work, the commands below
are faster.

- **Every Go change:** `go build ./...`, `gofmt -l internal cmd test` (prints
  nothing), `go vet ./...`, and the whole package of each file you changed:
  `go test ./internal/<pkg>/`. `make accept` (go vet, staticcheck, the
  boundary and ingress lints, `go test -race ./...`, the exfiltration
  suite) is what promotion runs.
- **Tests that test the change:** a test for a fix fails on the unchanged
  code and passes on yours; one that passes on both does not test the fix,
  whatever its name says. `validate` runs changed Go tests that way; for
  anything else, use a clean copy of `HEAD` (`mkdir -p "$TMPDIR/base" &&
  git archive HEAD | tar -x -C "$TMPDIR/base"`, copy the test in, run it
  there). A test that cannot compile against the unchanged code shows
  nothing either way: say so. Setup whose errors are ignored (an unchecked
  `os.WriteFile`) lets a test pass on a state that never existed: check it,
  or write it with `test/fakes/must` (`must.WriteFile(t, ...)`); validate
  warns for each such line a change adds.
- **The console** (`internal/frontend/web/assets`: JS, CSS): Go's tests do not
  run it. Run its browser check, `bash test/browser/run.sh <check>`: one
  per file in `test/browser/checks/`, each described in `test/browser/README.md`
  (`ls test/browser/checks`; with no name `run.sh` runs them all, which
  `make browser` does too, in about 30 s; screenshots land in
  `test/browser/out/<check>`),
  and extend the check to the states your change adds: a setting off, after
  a save, the new action, the page that links to it. The
  check performs the new action and checks what it did (the request sent,
  the state after it): one that only finds an element passes on a feature
  that does nothing. If Chromium is missing: `cd test/browser && npx
  playwright install chromium`. `nodwyn app` opens a WebKit window, which
  Chromium does not show the faults of: for a layout or CSS change also run
  `BROWSER=webkit bash test/browser/run.sh` (`screens`, `approve`, `tasks`)
  when Playwright's WebKit is installed (`npx playwright install webkit`); it
  says so and runs nothing when it is not.
  To look by hand: `go run ./test/browser/demo -seed` prints a launch URL.
- **The website** (`site/`, or a page the site converts: `docs/*.md`, `SPEC.md`,
  `plan.md`, the ADRs): `pip install -r site/requirements.txt && python3
  site/build.py`, which fails on a broken link. A change to how the console
  looks wants `scripts/screenshots.sh` run and `docs/images` committed, and a
  behaviour the guide in `site/content` describes wants the guide changed (ADR 59).
- **What a client can do** (the console, the TUI, line mode, a CLI
  command): its row in `docs/clients.md` changes in the same commit.
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
  failure it comes from. The loop has a lean profile (the defaults of the
  files) and a full one (`internal/loop/full.scm`, evaluated over them), and
  a session nobody approves runs the first (`docs/scheme.md` §9.8): a switch
  you add that is on by default is turned off in `full.scm` too, and a test
  of the loop says which profile it runs.
- **Guard, denylist, bindings, keychain, egress, redaction:** `make exfil`
  (its scenarios run in a real session, `test/exfil/scenarios/README.md`).
  Never loosen a check to make a test pass.
- **The book and the expert guide** are built by the release flow
  (`scripts/release.py build`, ADR 42), not by `validate` or a session.
  After changing tracked Go or Scheme files, regenerate their inventory with
  `python3 docs/book/source/generate.py` and check it with `--check`; its counts
  include test files even though the listings omit Go tests. It lists the
  files git tracks, so `git add -N` a new file first: `validate` counts it
  whether or not you did. Commit the sources and catalogue. Generated PDFs
  are ignored and published as GitHub release attachments (ADR 42); do not
  add them to Git.

A plan's `Docs:` steps name every file this list asks for the change.

## Rules of the house

- Tests and checks never use a paid model, a real key or a passphrase: the
  fakes in `test/fakes` (ollama, jev, mcp, oauth, drive, container) stand in.
  A scripted fake model is `ollama.NewT(t, ...)`, which fails the test if the
  loop asks after the script ran out. `make smoke` talks to the real control
  plane; run it only when asked.
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
  choose, and name what you chose in your answer. Told to go ahead after you
  offered several slices: build the first end to end, run it where it runs,
  and name the rest. Building from an ADR, each clause of its Decision
  becomes a plan step or a line in the amendment saying it was not built.
- A file in the repository that the host reads is written by whoever can
  commit to it, a stranger's clone included. Before such a file decides
  anything (which project a session belongs to, what it may reach), say who
  writes it and why that is safe, as ADR 17 does for `allow.toml`, and
  prefer what git or the session log already records.
- A refusal from the guard or the denylist is the answer. Do not reach the
  same file another way, through the shell or a link.
- Never apply, pop or drop a stash you did not make, and do not stash to
  compare with the unchanged code: use the clean copy of `HEAD` above. Every
  worktree of a repository shares one stash, so a pop can take another
  session's change.
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
- The console reaches the daemon through the bridge in
  `internal/frontend/web/server.go`. A method missing from `allowed()` is
  refused and removed from `hello`, so `conn.has` is false for it, and a
  fallback to another method hides the failure. A method the console calls
  is listed there, and only for this computer when it changes setup
  (ADR 30), but for the settings methods in `linkSettings`, which another
  nodwyn linked with the person's passkey is also granted unless the
  console was started with `--no-link-settings`, and not what `linkWidens`
  names (approvals, policy, the judge but its default) nor the providers (ADR 54, amended
  2026-10-23; the default model is granted). A link never holds a touch: `caller.touched` is false
  for it.

## When nodwyn runs its own tests

TMPDIR is then the session's scratch directory, a private directory in the
system's temporary directory (until 2026-10-16 it was inside nodwyn's protected
data directory, where every test repository was refused), and commits may find
no signing agent. Packages whose tests build a guard or commit isolate HOME
with `test/fakes/home`; a new such package does the same in its `TestMain`.
That also sets `NODWYN_PROFILE=full`, the loop their tests were written
against; a test of the lean profile sets it itself (`t.Setenv`), so it is not
parallel. A test that starts a session and kills it, or never closes it,
leaves its scratch directory in the temporary directory: give it a `TMPDIR` of
its own (`TestResumeAfterProcessKilled` does). A test that fails on the
unchanged code too is not yours: say so rather than fix around it.
