# nodwyn — Plan

Where things stand and what is next. What exists is in
[README.md](README.md) and [docs/manual.md](docs/manual.md); why it is shaped
that way is [SPEC.md](SPEC.md) and the [decision records](docs/adr/), whose
index says for each what is built and what is not. How it was built, from the
bootstrap through v0 and the tracks after it, is the book's part "How it was
built, and what is next" (`make book`) and the git history; the plan as it
stood until 2026-10-02 is `git show af6c36f:plan.md`.

## Where it stands (2026-10-16)

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

## Next

Each item is open in the ADR named, which holds the design and says what of
it is built.

1. **Worktrees (ADR 34).** `nodwyn worktree list` and `remove`; `--worktree`
   and `--worktree-base` for line mode and `nodwyn run`, with `worktree` on
   `run.start`; the repository behind a worktree made by hand (the common
   git directory), so notes, MCP workspaces and history key on it; commits
   ahead and the last push in the console, and Remove; the task-root refusal
   moved into core; and the exfil scenarios and the two-session race that
   prove it (WT5).
2. **Scheduled tasks' login item (ADR 49).** A scheduler runs while the
   daemon does; nothing starts the daemon at login, so a schedule waits for
   someone to open nodwyn.
3. **Retrieval beyond words (ADR 51, 50).** The measurement of what words
   alone find, then vectors from a small model shipped inside nodwyn, fused
   with the words (R4), model-written summaries, and providers' embeddings
   chosen on this computer.
4. **Measure what changed since the benchmark run.** The working habits
   were adopted on one measurement: 86 of 121 SWE-bench Verified tasks
   (ADR 32). Since then both system prompts, the loop, compaction and the
   plan have changed without a run. Run the cohort on the current defaults,
   then the leave-one-out ablation of pil's parts that ADR 32 queued; a part
   that does not pay its way is a candidate for removal.
5. **L3 (P8, optional).** A calibrated classifier for the class-unknown
   residual L1 leaves, which feeds the decision with binding and never
   decides alone (SPEC.md). Until it exists the text layers are L1 alone.

Smaller gaps, in no order: MCP's own authorization discovery and dynamic
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
| Prompt and loop changes help one model and repository and hurt others | Measure on the cohort (Next, 4); every rule names the failure it came from, so an unhelpful one can be found and removed |
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
