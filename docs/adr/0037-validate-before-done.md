# ADR 0037: Work on nodwyn is validated before it is called done

- Status: accepted; implemented 2026-10-03
- Date: 2026-10-03

## Context
`AGENTS.md` (ADR 36) lists what to run for each kind of change, and says a
change is done when it has been run where it runs. On 2026-10-03 two chat
sessions built ADR 34's worktree sessions and called the work done after
`make accept` passed. Both attempts were reverted. A review found that:

- the console toggle, the feature that had been asked for, did nothing.
  The bridge never offered the method the console called, and a fallback
  hid that. The browser check had been extended only to find the checkbox;
- each session's own "check" searched files for strings
  (`grep -q 'worktree: true' home.js`) and counted a match as done;
- a test written for "a clean close keeps attachments" passed on the code
  that deleted them, because its setup made the tree dirty for another
  reason and ignored an error;
- a new primitive was in neither the book's nor the expert guide's tables,
  and a new working rule had no ADR 32 amendment;
- a failing browser check left its demo server running, holding the pipe
  that `run.sh … | tail` read, so the session waited ten minutes and then
  stopped with nothing to report.

The rules were in `AGENTS.md`, and the model followed some of them. Nothing
checked the rest, and nothing asked: the default loop's finish check
(`evidence-loop`, strategy.scm) runs only in unattended runs and planned
turns, so a chat had none.

## Decision
- **`make validate`** runs `scripts/validate`, a Go program that:
  - measures the change from where the branch left its upstream (`git
    merge-base HEAD @{upstream}`, else `HEAD`), counting committed,
    staged, unstaged and untracked files;
  - chooses checks by path:
    - changed Go files: `gofmt`;
    - changed console scripts: `node --check`;
    - anything but documentation no test reads: `make accept`;
    - the console, its RPC contract or its checks: every browser check,
      which fails rather than skips without Node or Playwright;
    - book or expert guide sources: `make book` or `make expert`, which
      only warn when texlive is missing;
  - runs each test the change adds or edits against a clean copy of the
    code at the base (`git archive`, never the stash), with the changed
    test files laid over it. A test that fails there tests the change. One
    that passes there is a warning: it does not test a fix. One that cannot
    compile there calls new code, and says nothing either way;
  - checks what a diff can show:
    - a primitive added to the host is in both primitive tables (a
      failure);
    - a changed `prompts.scm` comes with an ADR 32 change, a changed wire
      schema with `docs/protocol.md`, and changed code with a changed test
      (warnings);
  - runs every command as the leader of a process group of its own, with
    its output in a file and a time limit, and kills the group when the
    command ends, so nothing it left behind keeps anyone waiting;
  - prints one line per check, the end of each failure's output, and a
    last line that says PASS or FAIL. It exits 1 on a failure.
- **`test/browser/run.sh`** kills each check's process group when the check
  ends. A check that throws no longer leaves its demo holding a pipe.
- **The `validate` tool** is registered in `.nodwyn/init.scm`. It runs
  `make validate` through `shell-start` and reads it to the end, since
  validation can take longer than `shell-exec`'s ten minutes.
  `allow.toml` approves `make validate` standing.
- **A finish check for this repository's turns.** `.nodwyn/init.scm` wraps
  `default-agent-turn`:
  - a turn that changed the repository and ends without `validate` having
    run on the repository as it then is gets a message saying so. It goes
    on, at most twice;
  - a turn that changed nothing, such as one that answers a question, is
    not reminded;
  - `validate` also counts as a test run for the default loop's own finish
    check, so a run is not asked twice;
  - the repository is compared by git's status lines and the diff of
    tracked files.
- **What judges the work is protected.** The validator is under `scripts/`
  and its target in the `Makefile`, both protected paths (ADR 17). The tool
  and the finish check are in `.nodwyn/init.scm`, also protected, and not in
  `dev/`, which `allow.toml` lets a session write without asking.
- **`AGENTS.md`** says to run `validate` after the last edit and quote its
  summary, warnings included. It also says what no command can check:
  - that a browser check performs the action and checks what it did;
  - the console's bridge;
  - that repository files are input;
  - how to scope a go-ahead and an ADR's clauses.

## Consequences
- A change to Go code costs `make accept` (about two minutes warm). A
  console change adds the browser checks (about a minute each). A change to
  documentation alone costs nothing.
- The reminder is advice, as the default loop's are: a model can still
  finish without validating, after two reminders. Its answer then has no
  summary to quote, which a reader can see.
- The comparison misses a second edit to an untracked file, since its
  status line does not change. An edit to a tracked file shows in the diff
  whether the file tools or the shell made it.
- `.nodwyn/init.scm` still loads `dev/*.scm` before it defines the tool
  and the finish check, and a session writes `dev/` without asking. What
  a session writes there runs in the next session, and can redefine what
  the finish check calls. Taking `dev/` out of `allow.toml`'s `writes`
  would close that; this decision does not.
- Browser checks are not run against the unchanged code, and a check that
  only finds an element still passes. That rule is `AGENTS.md`'s, and
  review's.
- A test that passes on the unchanged code is a warning, not a failure: a
  new check of behaviour that already worked passes there too. The answer
  says which it is.
- The validator's own tests (`scripts/validate`) are fixtures of the
  failures above:
  - a test that passes on the unchanged code;
  - a primitive missing from the tables;
  - a command that leaves a process behind;
  - a chat turn that changes a file and finishes without validating.

  Each was checked to fail when the part it covers is removed.

## Amendment 2026-10-03: checks that fail on what they claim, in a third of the time

Measured on 2026-10-03, warm: `make validate` for a console change took
about three and a half minutes. `make accept` took 1m34s, set by
`internal/core` (101 s under `-race`), and the six browser checks 1m30s, run
one after another and mostly waiting on fixed sleeps (`screens` alone 50 s).
Five of the six checks printed a page's errors and still exited 0, so a page
that threw passed validation; several printed what an action did without
asserting it.

- **The browser checks fail on what they claim.** They share
  `test/browser/lib.js`:
  - a check fails on any error a page throws or logs. An error it provokes
    on purpose (a refused request) it names first with `expectError`, once
    each time; warnings are printed and do not fail it. The first run found
    one at once: browsers rejected the pairing page's device-name pattern
    (an unescaped hyphen under the `v` flag) and so checked nothing; it is
    escaped now;
  - each action is asserted by what it did: the JSON-RPC requests the page
    sent over its socket, the files the daemon wrote, what the page shows
    after. The screenshots are for people and are compared with nothing;
  - whatever ends a check, it stops what it started and removes its
    temporary folders, which are under `TMPDIR` (a socket's folder falls
    back to `/tmp` when `TMPDIR` is too long for a socket path); a failure
    leaves a screenshot of each page;
  - `remote` listens on a port the system picked, not a random one.
  - Without the scripted model's pause, `tasks` found a console race about
    one run in eight: a task just started showed "No such task". Each state
    notification of a task the desk did not list yet started a refresh, and
    a refresh that a newer one overtook returned at once, so the task's page
    read a list without it. An overtaken refresh now waits for the newest.
- **They wait for conditions, never for a time.** The console sets
  `<html data-ready>` once the desk's first refresh is drawn and the models
  have answered, and `.task[data-loaded]` once a task's log, details and
  notes are; nothing else reads them. `demo -seed` prints its URL once its
  tasks have settled, and the checks pass `-pace=0` to its scripted model,
  which otherwise waits 400 ms a reply for people watching.
- **`run.sh` runs the checks at once.** Each has its own folder for
  screenshots and its own log; the logs print in the order the checks were
  named, each with how long it took.
- **A scripted model that runs out fails the test.** `ollama.NewT` fails a
  test whose loop asks after its script ran out, which the client otherwise
  retried for six seconds a turn before an error the test never read.
  `internal/core`'s tests use it. Six ran out (27 requests); their scripts
  now end their turns, and three of them assert what they were named for:
  `TestAbortDuringConfirm` never reached a confirm to abort,
  `TestUploadSettingWhenNobodyAsks` made both uploads in its first turn, and
  `TestPlanAndEphemeralRedaction` recorded "done" as its plan.
- **Other slow tests.** The CLI tests run their subprocesses with
  `GORACE=atexit_sleep_ms=0`, as `test/fakes/container` already did: the race
  detector otherwise sleeps a second as each exits. `TestTailCalls` runs 20k
  iterations, not 200k: a loop whose call is not in a tail position
  overflows its 8 MB stack before 5k, with or without `-race`.
- **Not done.** `internal/core`'s tests stay sequential. Marked parallel
  they took about 10 s, but one run in three failed to lock a session log
  that another test's child process may briefly hold (a flock shared across
  fork); that is for its own change.

After it, on the same machine: the browser checks take about 10 s together
(`screens` 6.5 s alone), `go test -race ./internal/core/` 30 s, and `make
accept` 39 s.

## Amendment 2026-10-03: `internal/core`'s tests run in parallel

The session log a test failed to lock (the amendment above) was held by a
child that another test's goroutine forked: a flock belongs to the open
file description, which the child shares until its exec. Closing a writer
now unlocks first (ADR 2's amendment of the same date). With that fixed,
219 of `internal/core`'s 228 tests call `t.Parallel`. The nine that change
what the whole process shares stay sequential and run first: the six
contained-server tests and the two scratch tests that set environment
variables, and `TestAnUnfinishedTurnLeavesItsPlan`, which sets
`llm.RetryDelay`. Six runs of each, alternated on the same machine:
`go test -race -count=1 ./internal/core/` took 27.1 to 27.7 s
sequential and 6.6 to 7.6 s parallel, none failing.

## Amendment 2026-10-05: a worktree's work is measured from where it left main, and a failing browser check is sorted

Three things the local session logs of 2026-10-03 to 10-05 showed validate
getting wrong in the worktrees most work now runs in (ADR 34):

- A worktree's branch has no upstream, so validate measured from HEAD, and
  once the work was committed it said "nothing changed since HEAD … PASS"
  (20261004T080135Z-aef239e9 seq 1769, 20261004T144303Z-219d1111 seq 4385,
  20261004T160126Z-dfc828d6 seq 2528, 20261005T151325Z-d9519026 seq 5649):
  models re-pointed the branch's upstream by hand or re-ran make accept and
  the browser checks. A branch with no upstream is now measured from where it
  left the main line (the branch origin's HEAD names, then main, then master,
  local before remote); the main line itself, with no upstream, still
  measures from HEAD.
- This repository's finish check compared git's status lines and the diff
  against HEAD before and after a turn, so a turn that only committed what
  validate had already checked was told to validate again, at least ten
  times (20261005T035352Z-5a76f6e4 seq 556, 20261005T040401Z-cb8a1bdf 482,
  20261005T042752Z-871dfe24 575, 20261004T141038Z-b6cc7b4f 1015, ...). It now
  compares the untracked files and the diff against the worktree's base
  commit, or the commit HEAD named when the turn began (`git-head`, and
  `git-diff`'s new `against`), so a commit changes nothing.
- A failing browser check's summary was the end of the log, which held the
  checks that passed, and telling a failure the unchanged code has too took
  2.5 to 6 minutes a time, once with a wrong diagnosis
  (20261004T032020Z-442c04d4, 20261004T035536Z-cd08f00d,
  20261004T073130Z-40187c24). validate now names the checks that failed with
  their FAILED lines, runs them once more (one that passes then is flaky),
  and runs those that fail again, as the unchanged code has them, on a copy
  of it with this checkout's Playwright. It fails only for a check that
  fails with the change and passes without it; flaky checks and checks that
  fail on the unchanged code too are a warning to answer.

## Amendment 2026-10-07: the books are built by the release flow, not by validate

Since ADR 42's amendment of 2026-10-05 the PDFs are not tracked. The release
flow's `scripts/release.py build` checks the source catalogue
(`generate.py --check`) and builds both books in a scratch checkout of the
release's commit, and nothing else keeps them. validate still built the
book whenever a file under `docs/book/` changed, and `AGENTS.md` has every
Go or Scheme change regenerate `docs/book/source/catalogue.tex`, whose
counts line then changes; it did in 8 of the 10 commits before this one.
Of the nine validate runs of 2026-10-07, four built the book (81 to 83 s)
and one the expert guide as well (88 s); the rest of a run took 40 to 60 s.
The books were 58 to 74% of those runs' time, for PDFs nobody kept.
`latexmk -c` removes the auxiliary files after each build, so every build
was a cold one.

- **validate builds neither book.** Their sources are documentation to it,
  as the rest of `docs/` is: they need no check.
- **Nor does a session.** `AGENTS.md` no longer asks for `make book` or
  `make expert` after a change to the books; the release flow builds them.
  The targets stay, for anyone who wants to read a copy.
- **The note that a rebuilt PDF goes with the change is gone** with the
  build: an untracked PDF never differs, so it could not show.

A LaTeX error in a book edit, or a Go or Scheme file the listings cannot
typeset, is now found when a release is built, which fails before it
publishes anything. The catalogue at this amendment's parent was stale (456
Go files counted, 457 tracked), which validate did not check and that build
would have refused; it is regenerated here.

## Amendment 2026-10-15: validate checks the books' source inventory

On commit-bench (tasks 556d6d12, c97f9a41, 5040f9fe, 107251d0 and 3422faf1
under builds A, dsl16, dsl18m and dsl19; deep study of 2026-10-15), eight
of the twenty changes left `docs/book/source/catalogue.tex` stale, and each
had run validate after its last Go edit and read "validate: PASS (0
warnings)": validate never ran `generate.py --check`. Five never
regenerated the inventory; two regenerated it, then edited tests at a
reminder; one (3422faf1, A) regenerated it with its new exfil scenario
untracked, which the generator, listing the files git tracks, did not
count. The validate tool's description in `.nodwyn/init.scm` said it
covered "the book and expert guide for their sources", which it has not
since the amendment above, and two explorers repeated it.

- **Any changed, added or deleted Go or Scheme file has validate check the
  inventory** ("book inventory"). It runs `generate.py --check` on a copy of
  git's index, in a temporary directory, with the Go and Scheme files git
  does not track added and those the tree no longer has removed (`git
  update-index --add --remove --info-only`, through `GIT_INDEX_FILE`): the
  files a commit of the tree would hold. Git's own index and object store
  are left as they were. A stale inventory fails with what to run: `git add
  -N` for each new file the generator cannot see, then `python3
  docs/book/source/generate.py` after the last Go or Scheme edit.
- **A base whose inventory is already stale fails too**, deliberately: the
  fix is the same one command, which `AGENTS.md` asks of every Go or Scheme
  change, and the release flow would refuse the stale file. Without
  `python3` the check fails and says so, as `node --check` does without
  node.
- **The tool's description says what it runs**: the inventory for Go and
  Scheme changes, and neither book.

Replayed on the twenty trees (each task's parent with the run's patch
applied and its new files untracked), the check failed on the eight stale
ones and passed the other twelve, in 0.26 to 0.29 s each. Counting the
untracked files is what catches two of the eight: 3422faf1 under A and
107251d0 under dsl19 pass `generate.py --check` with their new files left
out.

## Amendment 2026-10-16: validate runs what the checks run, and says what a change removes

A review of the tooling found six ways a change reached PASS without the
check that would have failed it. Each is closed here; validate's own tests
(`go test ./scripts/validate`) cover each.

- **The browser checks run for the code they exercise.** They ran only for
  `internal/frontend/web/`, the rpc package and `test/browser/`, but the
  checks `schedule`, `retrieval`, `conversation-search`, `tasks`, `models`,
  `credentials` and `mcp` start the real daemon and read `internal/core`,
  `internal/schedule`, `internal/retrieval`, `internal/event`, `internal/config`
  and the fakes. They now run for any change under `internal/`, `cmd/`,
  `test/fakes/` or `test/browser/` that is not a Go test file, and for
  `go.mod` and `go.sum`; the whole suite takes about 30 s, so a map of which
  check reads what would buy little and go stale.
- **The book catalogue is checked.** `generate.py --check` takes under a
  second; a Go or Scheme change (a Go test file too, since the counts include
  them) runs it, and a failure says to `git add -N` new files first. The books
  themselves are still built only by the release flow, as amended on
  2026-10-07; this check only keeps the catalogue the release flow
  checks from being found stale on release day.
- **The release publisher's tests run** when `scripts/release.py` or
  `test/release/` changes; only the Gitea workflow ran them.
- **A console change asks for a browser check.** The "tests" warning was
  silenced by any test change anywhere; a change under
  `internal/frontend/web/assets` with no change under `test/browser/checks`
  now warns separately, as AGENTS.md asks for the check's extension.
- **Removing a check is a warning.** A deleted or removed Go test, exfil
  scenario or browser check, and an edit to an existing file under
  `internal/guard`, `internal/binding`, `internal/keychain`,
  `internal/egress`, `internal/redact` or `test/exfil`, now warns, so the
  answer must say why. Before, `make accept` simply ran the smaller suite
  and was green. A test moved to another changed file is not removed. Taking
  `test/exfil/` out of the standing writes in `.nodwyn/allow.toml` was
  considered and not done: a change to the guard must add its exfil test, and
  a prompt for each would not make the removal easier to see than the warning.
- **A pushed branch is still measured from the main line.** The upstream was
  used whatever it was; after `git push -u origin feature` it is HEAD, so
  validate said "nothing to run" for the whole branch. The upstream is the
  base only when it is a main line (`origin/main`, `main`); otherwise the
  base is where the branch left the main line, and only a repository with no
  main line measures from its own upstream.

The tool's description no longer says validate builds the books.

Added the same day: validate warns for each line a change adds to a test
file that drops the error of `os.WriteFile`, `MkdirAll`, `Symlink` and the like
(about two hundred such lines were already there and are not listed), and
`test/fakes/must` is how to write them. The exfiltration scenarios were run
through a guard the test assembled by hand; they now run as tools of a real
session (`core.New`) with a scripted model, so a checker dropped or reordered
in `internal/core/construct.go`, or a security confirm that became an
ordinary one, fails them (shown by editing `internal/core/construct.go` and
`internal/guard/taint.go`: the old runner passed both edits).

## Amendment 2026-10-16: the two catalogue checks are one

The amendments of 2026-10-15 ("book inventory") and 2026-10-16 ("book
catalogue") were made on two branches and each added a run of
`generate.py --check` to validate. The merge of `claude/dsl23` keeps one
check, named "book catalogue": it runs for a changed, added or deleted Go or
Scheme file and for a change to `generate.py` (the 2026-10-16 trigger), and
it runs the generator on a copy of git's index with the untracked Go and
Scheme files added and the deleted ones removed (the 2026-10-15 check), so
a new file left out of git fails it as on 3422faf1. Its failure names the
`git add -N` and `git rm --cached` to run first. Running both would have
spent the second each takes for nothing, and a new untracked file would
have failed one and passed the other.
