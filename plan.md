# nodwyn — Plan

Where things stand and what is next. What exists is in
[README.md](README.md) and [docs/manual.md](docs/manual.md); why it is shaped
that way is [SPEC.md](SPEC.md) and the [decision records](docs/adr/), whose
index says for each what is built and what is not. How it was built, from the
bootstrap through v0 and the tracks after it, is the book's part "How it was
built, and what is next" (`make book`) and the git history; the plan as it
stood until 2026-10-02 is `git show 2f4394f^:plan.md`.

## Where it stands (2026-10-17)

- **v0 is done** except the optional L3 (P8): the log as the source of
  truth, the guard at the only Scheme→Go exit, credentials as handles behind
  a binding table, L1, the plan gate with the judge, L2 taint, ingress
  redaction, the TUI and memory.
- **Since v0, to 2026-10-02:** the console, the app and paired devices
  (ADR 21, 30); workspaces, attachments, MCP servers and OAuth2 credentials
  (ADR 22–24); unattended and judged runs (ADR 18, 20); the loop as Scheme
  with the working habits that held up on the benchmark (ADR 26–29, 32); a
  chat as the one way to work (ADR 35); `AGENTS.md` in the system prompt
  (ADR 36).
- **Since 2026-10-02 (ADR 34, 37–51):** sessions in git worktrees, the
  console's default for a folder in a repository, with `git-land` to commit,
  rebase and push, a review of a worktree task's changes, and the same in
  the TUI (ADR 34); validating before calling work done (ADR 37); tasks
  named, archived and deleted (ADR 39); a conversation's model, and
  providers and efforts, chosen in the console (ADR 40, 41); Gitea releases,
  by a manual workflow run (ADR 42); the desktop window (ADR 44);
  user-owned project trust (ADR 45); the judge as a provider's, chosen per
  task (ADR 46, 47); an experimental terminal for the model (ADR 48);
  scheduled tasks (ADR 49); retrieval and task memory, by words (ADR 50,
  51).
- **On 2026-10-17, toward an assistant that is always on:** a daemon a
  crash killed starts again over the socket it left, so a service manager
  can restart it; the console installs on a phone as a web app and shows its
  notices there (ADR 52); the console reconnects by itself, and the TUI and
  line mode use the daemon (ADR 30); schedules run on rules said in the
  daemon's words, with their recent runs (ADR 49), and become workflows
  (ADR 53), whose first slice has the daemon start at login and say on the
  desktop when a task needs an answer. For a server nobody sits at (ADR
  30, amended): devices pair by invitation, with no code, and stay signed
  in while they are used, and the person signs in with a passkey, makes
  this computer's choices from any device and changes setup after a touch;
  the console can get its own certificate (`--acme`); the keychain can open
  at boot from a systemd credential (ADR 5); a provider's sign-in finishes
  from a pasted address (ADR 24); and a project's startup files are
  reviewed and trusted from a device signed in with the passkey (ADR 45).

## Next

Each item is open in the ADR named, which holds the design and says what of
it is built.

1. **Always on, from anywhere (ADR 30, 52).** nodwyn runs on a server that
   is always on, and the person uses it from whatever computer or phone is
   at hand. A review on 2026-10-17 found the daemon, the bridge and the
   scheduler fit that, and the trust model did not, since it assumed someone
   sits at the computer nodwyn runs on; ADR 30's amendment "a computer
   nobody sits at" answered that the same day. In order:
   1. *Try it where it is meant to run.* None of it has run on such a
      server yet. Not tried: a certificate from a real CA (`--acme` was
      tested against a fake one), the keychain's TPM-sealed credential (a
      system service; the user unit's form was tried), a phone adding the
      console to its Home Screen and showing its notice, and whether an
      iPhone's Home Screen app has to sign in again (ADR 52).
   2. *Notices while the app is closed (ADR 52, §6).* Web Push: the bridge
      sends the fixed notice, encrypted, through the push service of the
      phone's browser, with its keys in the keychain and a subscription per
      paired device. Sending to Apple's, Google's or Mozilla's push service
      is an egress decision, recorded first.
   3. *Several hosts in one window.* The design canvas of 2026-10-17 (a
      private claude.ai artifact,
      [link removed]) draws it: the
      tasks of this computer and of an always-on host side by side, grouped
      by host in the sidebar, Needs you across hosts, a Runs on picker that
      the folders and models follow, a Hosts page and Add a host. The
      proposed mechanism keeps the console's one origin: the bridge links to
      another nodwyn as a named device of it, keeps that device's token in
      the keychain, and relays a socket per host; the console holds one
      connection per host. The other host's rules still decide what the link
      may do.
   4. *A home host for the phone.* The other hosts dial out to the one that
      is always on, which relays a phone's requests to them while they are
      reachable, so a laptop needs no open port and a phone sees its tasks.
   5. *Running as a service.* The user service is built (`nodwyn startup
      on`, ADR 53), and the manual says to turn on lingering so it outlives
      a login; open are one `--socket` given to both sides, since a service
      and an SSH login may see different `XDG_RUNTIME_DIR`s, and a time zone
      for schedules other than the host's (`time.Local`, now named).

   Also found by the review, and open: session logs grow without bound,
   and open tasks without a schedule never close; `nodwyn trust` and
   `credential add --oauth2` on the command line still need the server.
2. **Worktrees (ADR 34).** `nodwyn worktree list` and `remove`; `--worktree`
   and `--worktree-base` for line mode and `nodwyn run`, with `worktree` on
   `run.start`; the repository behind a worktree made by hand (the common
   git directory), so notes, MCP workspaces and history key on it; commits
   ahead and the last push in the console, and Remove; the task-root refusal
   moved into core; and the exfil scenarios and the two-session race that
   prove it (WT5).
3. **Workflows (ADR 53).** The timetable, slice 0 (a login item, the
   daemon's desktop notices), the Workflows page and a daily limit, workflows
   that watch pull requests or a command, and the outbox (a review posted
   when the person says yes) are built, against fakes (no real forge yet);
   next, in its order: email, and the TUI and the CLI.
4. **Retrieval beyond words (ADR 51, 50).** The measurement of what words
   alone find, then vectors from a small model shipped inside nodwyn, fused
   with the words (R4), model-written summaries, and providers' embeddings
   chosen on this computer.
5. **Measure what changed since the benchmark run.** The working habits
   were adopted on one measurement: 86 of 121 SWE-bench Verified tasks
   (ADR 32). Since then both system prompts, the loop, compaction and the
   plan have changed without a run. Run the cohort on the current defaults,
   then the leave-one-out ablation of pil's parts that ADR 32 queued; a part
   that does not pay its way is a candidate for removal.
6. **L3 (P8, optional).** A calibrated classifier for the class-unknown
   residual L1 leaves, which feeds the decision with binding and never
   decides alone (SPEC.md). Until it exists the text layers are L1 alone.

Smaller gaps, in no order: `TestTheSchemeToolDoesNotGoRoundTheGuard`
(`internal/core`) fails on main when run on its own (2026-10-17), since its
probe under `/tmp` is made: an unattended lean session may now write to
`/tmp` (aaa0a76), so either the probe or that rule needs another look, and
`validate` passed past it; MCP's own authorization discovery and dynamic
client registration (ADR 24); Google Docs beyond creating one (ADR 22, W5);
`AGENTS.md` files below the root (ADR 36); whether syntax and lint checks
belong in `.nodwyn/allow.toml`, so that a `node --check` need not ask. Dated:
raise the Go base to 1.27 once Fedora 45 ships it (due 2026-10-20) and a 1.27
feature is worth having (ADR 33).

## How work lands

- [AGENTS.md](AGENTS.md) says how a change is validated here, and the house
  rules; nodwyn's sessions in this repository read it in their prompt.
- `make promote` runs `make accept` (lint, the race-enabled tests, the
  exfil suite) and the selfcheck against a fake model before it swaps
  `nodwyn-stable`, keeping the previous one as `nodwyn-stable.prev`
  (ADR 12, 13). Building and promoting cost nothing: no paid model, no key,
  no passphrase.
- The exfil suite (`test/exfil/`) is the most important artifact: each
  scenario's seeded canary must appear in none of the log, the context, the
  judge's payloads or stdout. It grows with every change to the egress
  surface.
- [docs/friction.md](docs/friction.md) records where nodwyn got in the way;
  it is reviewed at every promotion, and its open items become work here
  (ADR 15).
- A decision is an ADR or an amendment to one; SPEC.md is amended, never
  contradicted.

## Risks

| Risk | Mitigation |
|---|---|
| A broken stable strands development | Two binaries, the selfcheck before promotion, and `nodwyn-stable.prev` to roll back (ADR 12, 13) |
| `shell-exec` runs what was confirmed, and the guard sees only the command line | The scrubbed child environment, the argv trigger, the pin and denylist on arguments; a confirm names the command exactly. Permanent: worktrees narrow it for git, not in general |
| Confirms become a rubber stamp | Unbound egress is denied by default; a confirm names class, destination and scope; ADR 35 asks less often, so what asks matters; security confirms are never put to the judge |
| Prompt and loop changes help one model and repository and hurt others | Measure on the cohort (Next, 5); every rule names the failure it came from, so an unhelpful one can be found and removed |
| A cloned repository's `AGENTS.md` steers the model | It is text, not policy: the guard, the binding table and the confirms decide; a link out of the repository or to a denylisted file reads as nothing, and its text is redacted (ADR 36) |
| A secret leaks by an unexpected path (an error, a panic, a debug print) | Canary post-conditions in every exfil scenario; handles print opaque; the keychain's value type has no `String()` |
| Interpreter scope creep | The kernel is frozen (ADR 7); capabilities enter only as primitives through the guard |
| One developer | The order above; anything not on it waits for friction.md to ask for it |

Security review follow-through (2026-10-06): [ADR 45](docs/adr/0045-project-trust-and-security-boundaries.md)
implements external project trust, executable-identity approvals, the explicit
remote-HTTP credential setting, bounded keychain reads and durable publication,
and canonical project identity with existing-data handling. The behavioural
contract is [docs/security.md](docs/security.md). The proposed readability and
size programme is [docs/maintenance-review.md](docs/maintenance-review.md); it is
not an implemented redesign.
