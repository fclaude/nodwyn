# ADR 0013: Promotion

- Status: accepted
- Date: 2026-09-20

## Context
A candidate must prove itself before it becomes the tool used to build the
next one.

## Decision
`scripts/promote.sh` runs `make accept` (lint, unit tests, exfil suite,
structural lints) and `scripts/selfcheck.sh` (the candidate completes a
scripted session: read a file, register a dev tool, run
`go test ./internal/log`, replay its own log). Only then: `nodwyn-stable` →
`nodwyn-stable.prev`, candidate → `nodwyn-stable`. The candidate's version is
the commit it was built from, so the tree must be clean (amended 2026-09-30;
it was a git tag `v0.N-<phase>`).

## Consequences
- Promotion is the only path to `nodwyn-stable`; copying a binary by hand is a
  process violation worth a friction.md line.

## Amendment — 2026-09-30 (tags)
Promotion no longer tags. `make promote` takes no argument; the stable
binary's version (`nodwyn-stable version`) is the commit's 12-character id,
and `git log` says what that commit holds. A phase name added nothing the
commit does not, and a tag to invent stood between a green tree and a
promotion. The tags `v0.1-bootstrap` to `v0.42-console` stay as history.

## Amendment — 2026-09-30 (the model turn)
The selfcheck's model turn goes to a fake model, `test/fakes/ollama/serve`,
started by the selfcheck on loopback, and the candidate runs on a temporary
config and session store with no keychain. Building nodwyn must cost
nothing and depend on nothing outside the checkout: the turn had gone to
whatever `[control]` named, which meant a paid model, a passphrase typed at
every promotion, or a particular machine being reachable. What the turn
proves is unchanged — the candidate's model client, window, log and replay
work end to end — and what it cannot prove, that a real model still
understands the prompts, was never a promotion gate: `scripts/smoke.sh`
remains the manual check against a real model.

## Amendment — 2026-10-06 (project trust)
Since project trust (ADR 45), `.nodwyn/init.scm`, which registers the dev
tools the selfcheck runs `go-test` through, loads only once its revision is
accepted, and the selfcheck's temporary config trusts nothing: every
promotion failed with `unbound variable: go-test`. The selfcheck now reviews
the repository with `nodwyn trust --repo .` and accepts the revision it
shows, in that temporary config, so the user's own trust record is neither
read nor written. This trusts no file the person has not: promote refuses a
dirty tree, so the files accepted are the committed ones the candidate was
built from. It also proves the candidate's trust review and acceptance work
end to end.

## Amendment 2026-10-16 (the browser checks, and WebKit)

Promotion ran `make accept` only, which does not run the console's
JavaScript: the sixteen browser checks (`test/browser`) ran when a session
called validate (ADR 37), so a commit made without validate could leave the
console blank and become the person's daily `nodwyn-stable`. `make browser`
(`bash test/browser/run.sh`) is now a step of `scripts/promote.sh`, between
accept and the build; it fails, saying how to install it, when Node or
Playwright's Chromium is missing. It is not part of `make accept`, which
the Gitea workflow and the GitHub mirror also run, because their runners are
Alpine containers on which a Playwright Chromium was never shown to run
(an unverified step there would be red or skipped for the wrong reason); the
workflow is unchanged.

The checks run Chromium, and `nodwyn app` opens WebKit (WebKitGTK, WKWebView),
whose layout and CSS support differ. `BROWSER=webkit` (`bash test/browser/run.sh`
or `make browser`) runs `screens`, `approve` and `tasks` in Playwright's
WebKit, with screenshots under `out/webkit/`. It is a separate download, so it
is in neither validate nor promote: where it is not installed the run says so
and runs nothing (exit 0). It was run on no machine for this amendment, since
WebKit was not installed on the one used: only the missing-WebKit path was run,
and the WebKit launch itself is untried. The other checks stay Chromium-only (`remote` maps a host name
with a Chromium flag).
