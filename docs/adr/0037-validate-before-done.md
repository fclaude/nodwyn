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
