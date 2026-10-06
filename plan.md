# nodwyn — Plan

What is being built next, in order, and why. What exists is in
[README.md](README.md) and [docs/manual.md](docs/manual.md); why it is shaped
that way is [SPEC.md](SPEC.md) and the [decision records](docs/adr/). How it
was built, from the bootstrap through v0 and the tracks after it, is the
book's part "How it was built, and what is next" (`make book`) and the git
history; the plan as it stood until 2026-10-02 is `git show af6c36f:plan.md`.

## Where it stands (2026-10-02)

- **v0 is done** except the optional L3 (P8): the log as the source of
  truth, the guard at the only Scheme→Go exit, credentials as handles behind
  a binding table, L1, the plan gate with the judge, L2 taint, ingress
  redaction, the TUI and memory.
- **After v0:** the console, the app and paired devices (ADR 21, 30);
  workspaces, attachments, remote and contained MCP servers, OAuth2
  credentials and documents out (ADR 22–24); unattended and judged runs
  (ADR 18, 20); the loop as Scheme with the working habits that held up on
  the benchmark (ADR 26–29, 32); a chat as the one way to work, which asks
  less (ADR 35).
- **On 2026-10-02:** Settings edits `config.toml`, with a spend limit if
  the person sets one; the keychain may have no passphrase, and credentials,
  the judge's key and model keys are managed in Connections › Credentials;
  workspaces are managed on their page; a run's plan carries across its
  turns; `plan-write`, and the harness fixes from three runs of one UI task;
  the working rules from the review of the jev-key change (ADR 32, amended);
  a repository's `AGENTS.md` joins the system prompt (ADR 36), and nodwyn
  has one.
- **On 2026-10-03:** a task can start in a git worktree of its own from
  the console, listed under its repository (ADR 34, amended: the slice
  below marks what of WT0–WT4 that built); the console keeps an open task's
  page when another is shown, so switching back reads nothing again.
- **Not done:** the rest of sessions in git worktrees (ADR 34), and L3.

## Next, in order

### 1. Sessions in git worktrees, WT0–WT5 (ADR 34)

Several sessions can be open on one repository, and a run invites it, but
they share one working tree, index and stash, and nothing notices. Each
session may instead have its own linked worktree on its own branch: the
host creates it when the user asks, never the model, under
`.nodwyn/worktrees/`; the model commits on that branch, rebases it, and when
asked pushes it as `main`'s new head. The location (inside the repository,
for backups) and the git authority are decided.

Every phase is tested with real `git` in `t.TempDir()`, a bare repository as
the remote and the fake model, so building it costs nothing. Each phase
updates the docs it changes (manual, scheme.md, protocol.md and its schema,
browser.md, tui.md, runs.md) in the same commit.

Built on 2026-10-03 (ADR 34, amended): WT0 for the worktrees nodwyn makes;
WT1 but `--worktree` for the plain CLI; WT4's `worktree.open`, the ask-bar
toggle, the branch in the task's heading and Workspace panel, and the
grouping, with the browser check `worktrees`. What is left of each phase:

- **WT0 — the repository behind a checkout (small).** Left: a worktree made
  by hand. `git rev-parse --git-common-dir`, with `runGit`'s read-only
  flags, gives each git root its repository, so notes, MCP `workspaces`,
  the history and the console's folders key on it there too. Tests use a
  worktree made by hand: the git tools work there, versions defer to git,
  the `.git` file is protected, and the pin refuses the common directory's
  hooks by absolute path. After WT0, `--repo` pointed at an existing
  worktree is first-class.
- **WT1 — a session in a new worktree (medium).** Left: the plain CLI gets
  `nodwyn --worktree [--worktree-base REF]`, and the task-root refusal
  moves into core with it.
- **WT2 — lifecycle (small).** `nodwyn worktree list` and `nodwyn worktree
  remove [--force]`. Removal refuses a dirty tree without `--force`, keeps
  an unmerged branch and logs `worktree.remove`. `nodwyn sessions` shows the
  branch.
- **WT3 — the session's git (medium).** Built on 2026-10-05 as
  `(git-land message)` (ADR 34, amended): one primitive offered only in
  worktree sessions commits the work, rebases the session's commits onto
  the newest base, pushes to the base's upstream by fast-forward only,
  and moves the local base safely. The prompt calls it after validate
  when the request asks for the work to land. It scans the outgoing diff
  with L1 and refuses credential-shaped text and hooks inside the
  worktree. A clean checkout of the base fast-forwards; a dirty one stays
  as it is, with that said. This settles the two questions originally
  open before WT3.
  - Not built: pushing the session's branch itself with a lease, a rebase
    abort action (the person aborts from a terminal), the
    `push = [URL, …]` list in `allow.toml`, and the `git.push` log record.
    Landing is irreversible and asks through the guard; its decision and
    the tool result record what was pushed where. The amendment replaces
    the separate `git-commit`, `git-rebase` and `git-push` primitives.
- **WT4 — daemon and clients (medium).** Left:
  - RPC adds `worktree.list` and `worktree.remove` (local only), and
    `run.start` gains `worktree`. Each change goes into the method table,
    the schema, the web bridge's allowlist and protocol.md; the drift tests
    already hold them together.
  - `nodwyn run --worktree` and `nodwyn tui --worktree`, plus the TUI
    chooser row and its branch in the title.
  - In the console: commits ahead and the last push in the task header and
    Workspace panel, with Remove, each extending the browser check
    `worktrees`.
  - Built on 2026-10-05 (ADR 34, amended): reviewing a worktree task's
    changes. `worktree.diff` reads what it changed since its base commit and
    `worktree.review` sends line and file comments as one message; the
    console's Review tab draws and sends them, extending the browser check
    `worktrees`. Comments only: approving or rejecting a branch is not
    built, and a change of direction is a message to the task.
- **WT5 — proof (small).**
  - Exfil scenarios: a worktree session cannot read the repository's `.env`
    through `../`, cannot write the common directory's hooks, and `git-diff`
    refuses a path outside the worktree. A push to a remote re-pointed
    through `shell-exec` is a security confirm, and so is a commit carrying
    a canary credential.
  - Two fake-model sessions edit one repository at once, and each sees only
    its own changes. One lands on `main` while the other's push is refused
    until it rebases.
  - The book and the expert guide are rebuilt, and friction.md records the
    race this closes.

Not in this track: worktrees for sub-agents, which run one at a time;
force-pushing, merge commits, tags and pull requests through a host's API;
copying untracked files such as `.env` into a new worktree; version control
other than git.

### 2. Measure what changed since the benchmark run

The working habits were adopted on one measurement: 86 of 121 SWE-bench
Verified tasks (ADR 32). Since then both system prompts and the loop
changed without a run: reads fold only when the fold pays for the cache it
costs, `plan-write` and the explorer's findings handed to the worker, the
rules from the jev-key review, and `AGENTS.md` (which the benchmark's
repositories do not have). Run the cohort on the current defaults, then the
leave-one-out ablation of pil's parts that ADR 32 queued; a part that does
not pay its way is a candidate for removal.

### 3. L3 (P8, optional)

A calibrated classifier for the class-unknown residual L1 leaves, which
feeds the decision with binding and never decides alone (SPEC.md). Until it
exists the text layers are L1 alone.

### Smaller gaps, in no order

- MCP's own authorization discovery and dynamic client registration
  (ADR 24): an MCP server behind OAuth needs a `custom` credential whose
  client the user registered.
- Google Docs beyond creating one: changing a document, folders, sharing,
  uploads over 5 MiB (ADR 22, W5).
- `AGENTS.md` files below the root, which the convention lets apply to
  their subtree (ADR 36), if a repository needs them.
- Read-only checks that ask: with "allow all" off and no judge set up, a
  `node --check` asks like any command, since `.nodwyn/allow.toml` lists
  only Go and git commands. Decide whether syntax and lint checks belong in
  it; the browser checks do not as they are, since `run.sh` installs
  packages from the network.

Dated: raise the Go base to 1.27 once Fedora 45 ships it (due 2026-10-20)
and a 1.27 feature is worth having (ADR 33).

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
| `shell-exec` runs what was confirmed, and the guard sees only the command line | The scrubbed child environment, the argv trigger, the pin and denylist on arguments; a confirm names the command exactly. Permanent: worktrees (WT5) narrow it for git, not in general |
| Confirms become a rubber stamp | Unbound egress is denied by default; a confirm names class, destination and scope; ADR 35 asks less often, so what asks matters; security confirms are never put to the judge |
| Prompt and loop changes help one model and repository and hurt others | Measure on the cohort (Next, 2); every rule names the failure it came from, so an unhelpful one can be found and removed |
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
