# ADR 0014: `.nodwyn/init.scm` convention

- Status: accepted
- Date: 2026-09-20

## Context
This repo needs dev tools (`go-test`, `git-diff`, …) that nodwyn's model can
call, and they must cross the guard like everything else.

## Decision
On start, after `prelude/`, nodwyn loads `./.nodwyn/init.scm` if present. This
repo's `init.scm` loads `dev/*.scm`, which register tools composed from host
primitives via `register-tool`. Nothing in `dev/` bypasses the guard.

## Consequences
- Loading happens before the first message (ADR 9).
- A project can ship its own tools the same way.
- A project can replace the agent loop the same way: the default
  `agent-turn` is installed before `init.scm` loads (ADR 26).

## Amendment 2026-10-05: grep-repo reads Perl-compatible expressions and says when nothing matched

This repository's `grep-repo` (`dev/repo.scm`) ran `git grep -e`, a basic
expression, where `a|b` is the three characters `a|b`, and answered no
match with `exit 1` alone. In the local sessions of 2026-09-30 to 10-05 all
248 of its searches with an alternation came back empty, 222 of which match
this tree with `-P`, and models took the silence for an answer: "the
console uses no ARIA roles" (20261004T043621Z-4f1e4886), and a "verified by
grep" claim about `docs/browser.md` that reached main
(20261004T053047Z-1b4c67dc). It now runs `git grep -P`, the syntax the
models write and `index-grep` reads (`a|b`, `\b`, `\d`, `(?i)`), and when
nothing matches says so, naming the pattern and the files, and that only
tracked files were searched.

It stays on tracked files. It runs through `shell-exec`, so `--untracked`
would search files the denylist keeps from `fs-read`, a `.env` not yet
ignored say, which `index-grep` skips; the no-match line names
`index-grep` for files not yet added. TestGrepRepoReadsAlternationsAndSaysWhenNothingMatched
(scripts/validate) runs it on a fixture with this repository's `dev/`.

## Amendment 2026-10-06: security review and project identity

[ADR 45](0045-project-trust-and-security-boundaries.md) supersedes the earlier
behaviour where it differs: repository startup and approval files need an external
user trust record; executable identity is resolved, credential transport is a user
setting with secure defaults, keychain decoding and persistence are bounded, and
project paths resolve symlinks consistently. Its compatibility rules apply to
existing sessions, workspaces, notes and worktrees.
