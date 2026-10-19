# ADR 0028: A scratch directory beside the repository

- Status: accepted
- Date: 2026-09-27

## Context
The repository pin (the original build plan's §3.3; see the index) refuses every path outside the repository,
which also refused `/tmp`. Models need somewhere for throwaway files, a
script that reproduces a bug or a note, and worked around the refusal:
they wrote such files into the repository, where they reach its diff
(for SWE-bench, the submitted patch), or wrote them through the shell.

## Decision
Each session gets a scratch directory, `$TMPDIR/nodwyn-scratch/<session-id>`,
created when the session starts or resumes and removed when it closes
cleanly.

- The repository pin takes it as a second root, for absolute paths only:
  relative paths stay relative to the repository. Symbolic links are
  resolved, so a link out of the scratch directory does not escape it.
- Writes inside it are approved as standing (`guard.ScratchWrites`): it is
  outside the repository and short-lived, so asking about each file
  protects nothing. Security confirms, such as content that looks like a
  credential, still ask. Such writes still count against the turn's
  budget of irreversible actions.
- `shell-exec` runs with `TMPDIR` set to it, and a command naming a path
  inside it is not flagged as naming a path outside the repository.
- `(scratch-dir)` returns it, and the default system prompt names it as the
  one place outside the repository the model may use.

## Consequences
- Throwaway files stay out of the repository and its diff.
- The credential-file denylist and the protected paths apply unchanged;
  everything else outside the repository and the scratch directory is still
  refused.
- A crashed session leaves its directory behind until the operating system
  clears `$TMPDIR` or the session is resumed and closed.

## Amendment: in the data directory, excepted from the denylist — 2026-10-02

Since 2026-09-27 the directory is made by `os.MkdirTemp` under nodwyn's
data directory, `<data>/scratch/nodwyn-scratch-<session-id>-<random>`,
mode 0700: a private parent and an unpredictable name, rather than a shared
name in `/tmp` that another user could create first. A resumed session gets
a new, empty directory. A session touches its own at every turn, and
directories untouched for a week, left by sessions that did not close, are
swept.

When the credential denylist took in the whole data directory
(2026-09-30), every file primitive refused the scratch directory, and only
the shell could write there. The denylist now excepts the session's own
scratch directory (`guard.PathDenylist.Except`) and nothing else in the
data directory. A path is excepted only when both what it names and what it
resolves to are inside, so a link out of the scratch directory is judged by
where it leads, and credential-shaped names are refused there as anywhere.
The guard, `index-grep`, `fs-find` and `doc-export`'s image vetting use the
same list.

The denylist also covers nodwyn's configuration and data directories where
the session found them (`XDG_CONFIG_HOME`, `XDG_DATA_HOME`), not only at
their default places under the home directory; the scratch directory moves
with the data directory and stays excepted.

## Amendment: code is not credential material; tests have a home of their own — 2026-10-02

The denylist's name globs (`credentials.*`, `id_*`, ...) also matched source
files, so a task about the console's credentials page could not read or
edit `credentials.js` or `credentials.go`: the model went through the shell,
which the person had to approve for the whole session, and `git-status`,
which left such files out, hid its changes from the evidence loop. A file
with a source or documentation extension (`.go`, `.js`, `.py`, `.md`, ...)
no longer matches a name glob, unless it is a dotfile (`.env.js`,
`.eslintrc.js` may hold a key); the protected directories still apply to it
and its contents still pass ingress redaction. `git-status` names every
changed file, since a name is no secret; `git-diff` leaves out the contents
of the files the denylist refuses, named exactly.

The scratch directory stays in the data directory, but it is commands'
`TMPDIR`, so when nodwyn ran its own tests every test repository was inside
a protected directory and refused, and commits failed because the scrubbed
environment has no SSH agent for the user's commit signing. The packages
whose tests build a guard or commit run in a home of their own
(`test/fakes/home`): `HOME` a new empty directory, the XDG directories and
git's global and system configuration unset, and a git identity.

## Amendment 2026-10-16: in the system's temporary directory, outside the protected ones

The scratch directory was in the data directory (above) so that no other user
could plant or share it. That placed it inside the directories the guard
protects, and the one thing in them the tools may use was the one thing a
program run from the shell could not: a test suite that builds temporary
repositories under `TMPDIR` was refused its own files ("path README.md is
under the protected directory .../.local/share/nodwyn"), and the model went
round it. On commit-bench's nine tasks (dsl20r, 2026-10-16, the review of
67a1f950, 855e90e7 and 7513f7eb) 5 to 17 calls of a run went on that: tests
that failed on the unchanged code because their temporary directory was in a
protected path, `HOME` and `TMPDIR` set to `/tmp` paths (refused, 13 of the
23 shell commands refused in the 26 runs named `/tmp`) or to a folder of the
repository; one run edited three unrelated existing tests to get round it.
(The review also counted scripts written to the scratch directory and run in
a second call. That is the working rules' own check loop, ADR 32, and
stays.)

Decision:

- **Each session's directory is made in the system's temporary directory**,
  `os.MkdirTemp(os.TempDir(), "nodwyn-scratch-<session>-")`: mode 0700, a name
  no one can guess, made by us (`MkdirTemp` makes the directory itself and
  tries another name when one exists, so a link or a directory another user
  made first is never taken). It is outside
  nodwyn's configuration and data directories wherever `XDG_CONFIG_HOME` and
  `XDG_DATA_HOME` put them, so a program run with it as `TMPDIR` is not
  refused its own files and needs no `HOME` or `TMPDIR` of its own. `HOME`
  stays the person's: it holds the Go and git caches and configuration the
  commands need, and the programs whose tests need one of their own already
  isolate it (`test/fakes/home`).
- **The exception to the denylist stays, and means what it meant.** It is by
  the directory the session made, not by where the system keeps temporary
  files: should `TMPDIR` be set inside the data directory, the scratch
  directory there is still the one place in it the tools may use, and the rest
  of the directory, its other temporary files included, stays denied
  (`TestScratchInsideTheDataDirectoryIsStillTheOnePlaceToUse`). Nothing the
  denylist protects is opened: `TestScratchIsOutsideTheProtectedDirectoriesWhichStayDenied`
  (the exfiltration suite) checks that the data and configuration directories
  are refused by path, through a link made in the scratch directory and by a
  program's default denylist, and that nothing in them reaches the log.
- **The sweep is of the shared directory, so it is careful.** It removes
  directories named for a scratch directory, a week untouched, that are this
  user's own and not links (a directory another user made, or a link to one,
  is left). It also clears the old place, `<data>/scratch`, for the sessions
  that left theirs there.
- **The prompt says so.** The scratch rule (prompts.scm) says that commands
  run with `TMPDIR` set to it, so a test suite puts its temporary files there
  without being told, and not to set `TMPDIR` or `HOME` elsewhere or use
  `/tmp` (ADR 32, amended 2026-10-16).

What the 23 refused shell commands of the runs were, besides `/tmp` paths:
the standing approval's reasons (an absolute path in a line a standing prefix
covers, 2; a flag it has not reviewed, such as `go test -fuzz` and
`git log --date`, 3) and a secret on the command line (3). `$TMPDIR` in a
command, an inline `VAR=value command` and `export VAR=value` were never
refused: they are an ordinary confirm at most, which an unattended run
answers, and a person is asked for only where no standing approval covers the
line. Those rules are ADR 17's and stay. One refusal now says what to write:
an absolute path inside the repository (the system prompt gives its absolute
path, and `cd <repository> && go test` was refused for good in an unattended
run) is refused as before, and the reason adds that a command already runs in
the repository root, so the path is written relative to it
(`TestAbsoluteRepositoryPathRefusalSaysToWriteItRelative`).

Consequences:

- A scratch directory is in a directory other users can write to, though
  not into this one. A system whose temporary directory forbids running
  programs from it (a `noexec` `/tmp`) needs a `TMPDIR` for nodwyn that does
  not, as `go test` does for anyone.
- A `TMPDIR` set inside the repository puts the scratch directory there, and
  its files in the repository's diff; the data directory was a safe default
  for that, the system's temporary directory is the operator's to choose.
- Tests that start sessions leave nothing behind when they close them; a test
  that kills a child session (`TestResumeAfterProcessKilled`) gives it a
  `TMPDIR` that goes with the test.

## Amendment 2026-10-18: a sandbox session keeps its scratch directory (ADR 54)

In sandbox mode what a session's commands start in the background keeps
running after the session, with `TMPDIR` set to its scratch directory, so the
directory is not removed when the session closes: a server whose temporary
files went there would fail without it. The machine is thrown away with it.
Every other session removes its directory as above.
