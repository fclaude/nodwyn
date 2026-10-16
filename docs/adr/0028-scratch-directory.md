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
