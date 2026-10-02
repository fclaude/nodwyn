# ADR 0028: A scratch directory beside the repository

- Status: accepted
- Date: 2026-09-27

## Context
The repository pin (plan §3.3) refuses every path outside the repository,
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
