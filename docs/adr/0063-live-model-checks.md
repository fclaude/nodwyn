# ADR 0063: Live model checks on the private runner

- Status: accepted; implemented 2026-11-22
- Date: 2026-11-22

## Context

Every test in the repository uses fakes (`test/fakes`), so a test never
needs a key, never costs money and never depends on a model's mood. That
keeps the suite fast and repeatable, and it leaves a gap: nothing checks that
nodwyn works with a real model, whose tool calls, streams, refusals and
reasoning are what a person meets. `make smoke` covers two turns by hand.
The endpoint we would test against is on our own network, and its address
and key are not for the public repository's history or GitHub's runners.

## Decision

1. **A live suite in the repository, behind a build tag.** `test/live` holds
   Go tests with the `live` tag, so `make test`, `validate`, promotion and
   every ordinary `go` command leave it out. It builds nodwyn from the tree
   and drives the binary as a person or a script does: line mode with
   confirms declined, `nodwyn run`, a resume.
2. **It runs on the Gitea runner only.** `.gitea/workflows/live.yml` runs it
   nightly and by hand; GitHub reads `.github/workflows` and never this file.
   `make live` runs it anywhere else on purpose.
3. **The endpoint is the runner's.** Its address and key are Gitea Actions
   secrets, its provider, model and dialect variables; nothing in the
   repository names them, and the workflow does nothing while they are unset.
4. **Nothing names the endpoint in what the suite keeps.** nodwyn talks to a
   recording proxy on 127.0.0.1, so its configuration, logs and output hold
   only the loopback address; the key goes into a keychain made for the
   scenario, without a passphrase, in a home directory deleted with it; and
   what the suite prints, reports and keeps has the address, its host and
   the key replaced by placeholders.
5. **A scenario checks behaviour, not wording.** It reads the files, the
   session's log and what went through the proxy: a file read and its
   number given back, a failing test fixed, a declined command not run, a
   planted credential never sent, a run's limit held, a schedule and a
   workflow trigger each run as a message and as a Scheme program, by the
   daemon's own scheduler. Another model, or a better answer, does not fail
   it.
6. **A report shows drift.** Each run writes `report.md` and `report.json`
   (result, time, requests, tokens, how turns ended) and, for a failed
   scenario, its scrubbed session log, kept as the job's artifact.

## Consequences

The rule that tests use fakes stands for everything but this suite. A run
costs what the endpoint charges for some forty requests; the scenarios run
one at a time and stop at limits of their own. A model can still fail a
scenario for a reason nodwyn did not cause; the kept log says which. Not
built yet: worktree tasks and `git-land`, MCP tools, the console in a
browser, and the judge against a real Decisions endpoint.
