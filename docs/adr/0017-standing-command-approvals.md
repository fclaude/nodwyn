# ADR 0017: Standing command approvals

- Status: accepted
- Date: 2026-09-21

## Context
`shell-exec` is irreversible, so every command needs a confirm. The dev
tools nodwyn uses to build itself (`go-test`, `git-diff`, `grep-repo`) wrap
`shell-exec`, which would mean a prompt for every test run — confirm fatigue
turns "y" into a reflex, which is worse than no prompt.

## Decision
The user may approve command lines ahead of time, by argv prefix ("go test",
"git status", "rg"). Approvals live in three places: `policy.allow_commands`
in `config.toml` (global), `<repo>/.nodwyn/allow.toml` (per repository,
tracked), and "a" answers to a confirm prompt (this session only, not
persisted). The guard checker `CommandAllow` turns a match into a *standing*
Allow, which outranks any Confirm from another checker and never outranks a
Deny. Every use is logged as an allow with the matching prefix as reason.

The default lists are read-only and test commands. There is no approval for
anything that commits, pushes, deletes or reaches the network; those stay
per-call confirms.

## Amendment 2026-09-21: writes

The same shape applies to writes. `writes = ["internal/", "docs/"]` in
`.nodwyn/allow.toml` (or `policy.allow_writes`) and "a" at a write prompt
(the file's directory, session only) make `fs-write`/`fs-edit` under those
prefixes a standing Allow; "t" at a write prompt approves every further
write until the turn ends. A standing-approved write is not silent: its diff
goes into the `guard.decision` record (`detail`) and the front-end shows it
dimmed. Paths that change policy or run in CI — `.nodwyn/`, `.github/`,
`.gitea/`, `scripts/`, `Makefile` — can never be standing-approved and
always prompt. git remains the undo: review `git diff` before committing.

*Amended 2026-09-27:* `.git/` is on that list too (hooks run at your next
commit), and a write to any of these paths is a security confirm
(`guard.ProtectedPaths`), judged after resolving absolute paths, `..`, case
and symbolic links like the repository pin. An unattended run (`--yolo`)
answers ordinary confirms but never security ones, so it cannot write them;
before this, it could.

*Amended 2026-10-02 (ADR 35):* `[approval] writes = "allow"`, the
default, makes every write the pin lets through a standing approval
(`guard.FolderWrites`), so a write prefix list matters only with
`writes = "ask"`. Protected paths and credential-shaped content remain
security confirms. git is no longer the only undo: a write keeps the
file's previous content unless git has it committed and unchanged.
Commands are unchanged here: a standing approval still runs one without a
confirm, and what no standing approval covers goes to `[approval]
commands` (the judge, then the person, by default).

## Consequences
- This is the command-line analogue of the binding table (SPEC): the user
  authorizes ahead of time, the harness applies silently and logs.
- The model can edit `.nodwyn/allow.toml` through `fs-write`/`fs-edit`; that
  is a confirmed write whose diff shows the new entry, and it takes effect
  only at the next session start.
- "a" persists nothing: standing approvals that should outlive a session are
  written down by the user, where they can be reviewed.

## Amendment 2026-10-02: "don't ask", a switch only a person reaches

The console offers one switch under the composer, **Don't ask: approve
writes and commands**: while it is on, every confirm a standing approval
could answer, an ordinary write or command (`ApprovalOptions().Session`), is
answered yes as the person who turned it on, with scope `all` in the guard
decision; the confirm pending when it is turned on is answered too. It
installs no prefix. Security confirms (a write that looks like a credential,
a command built from tool output), protected paths and every refusal are
untouched: it answers questions, it never overrules the guard. MCP tools,
uploads and egress keep their own questions.

The point is who can reach it. It is a field of the Go session with one
writer, `Session.SetApproveAll`, called only by the daemon's
`session.approve_all` with the authenticated peer as actor. No primitive
reads or writes it, whatever the Scheme's origin: user origin is not enough,
since a repository's `.nodwyn/init.scm` runs as user-origin Scheme.
`scripts/lint-boundaries.sh` fails a build that calls `SetApproveAll` outside
`internal/frontend/rpc` or touches the field outside `internal/core/confirm.go`.
Turning it on is refused to a paired device (ADR 30), as starting a run that
answers itself is, and to a session whose confirms are not asked of a person.
Like "a", it persists nothing: it ends with the session, a resume starts it
off, and each switch is an `approval.change` (`kind: "all"`, `off` when it is
turned off) in the log.

## Amendment 2026-10-04: the switch's record names the ask it answered

A switch that answered an ask which was waiting records its
`approval.change` with that ask's `prompt_id`, as every log event that
resolves a prompt carries the prompt it resolved. Without it the console's
Totals clocks read the switch's record as an event that did not end a
wait, and the whole wait — the time the task sat waiting for the answer
the switch then gave — was added to the working time, which made the
working time match the running time at the answer and lost the split the
panel keeps (reported on a session that answered a command's confirm with
the switch).

## Amendment 2026-10-05: confirmed approvals retain previews

Explicitly approved confirmations retain their existing preview in the final
`guard.decision.detail`, as standing approvals already do. The Changes inspector
needs the recorded preview rather than a reconstruction from a later file.
Denials remain denials, and old records are not backfilled. This is an approval
record before execution, never evidence that the operation succeeded.

## Amendment 2026-10-05: "don't ask" is offered only when Settings says so

The switch answered too much too easily. In the 75 local session logs of
2026-09-30 to 10-05 it was on in 40 sessions, often within seconds of the
first question, and 1,327 of the 1,329 confirms a person answered were
yes. Under it, with every ordinary command answered as the person, models
listed `~/.ssh`, tested the private key for a passphrase and loaded it into
a new `ssh-agent` to sign their commits (session 20261004T053047Z-1b4c67dc);
read, through `cat`, a session log `fs-read` had refused two seconds before
(20261004T073130Z-40187c24); committed with `--no-gpg-sign`
(20261004T024748Z-c7491879); and ran `git pull --rebase` on the person's
own checkout (20261002T204507Z-19a161b2). The guard's denylist checks the
paths a file tool is given, not the words of a command line, so the switch
turned the guard's questions off where they mattered most. It is still
sometimes the right tool, for a task a person is watching.

So the switch is offered only when the person asks for it in Settings:

- `[approval] dont_ask` (Settings › Approvals, "Don't ask in tasks"), off
  by default. `settings.write` takes it, and only the computer nodwyn runs
  on may call that (ADR 30); `config.toml` is the person's, so no
  repository and no Scheme turns it on.
- While it is off, `session.approve_all` refuses to turn the switch on, and
  `session.info` says `approve_all_offered: false`; the console shows the
  switch only when it is offered, or while it is still on, so it can be
  turned off. Turning it off is always allowed.
- Turning the setting off turns the switch off in every open task as well,
  each as an ordinary `approval.change` by the person who saved it: a
  person who takes the offer away is not left with tasks still approving
  everything. Turning it on changes no task until the person switches it
  on there.
- While Settings offers it, the page shows a red warning in place of the
  hint: what the switch lets a task do, and what models did here with it,
  from the sessions above.

The switch itself is unchanged: what it answers, what it never answers,
who may reach it and that a resumed task starts with it off.

## Amendment 2026-10-05: a standing approval names the program, not the shell

The system prompt sends every pipe, redirect and chain through
`bash -c "<line>"` (prompts.scm), and `Prefix` offered "Always under bash"
for every such call, since `-c` looks like a flag. One "a" on any of them
approved every command the session would ever run, which is "don't ask" by
another door: three of the local sessions of 2026-10-02 to 10-04 granted
`bash` within their first two commands (20261002T192800Z-920da7d8 at seq
1503, then `git stash && go test; git stash pop` and a python rewrite of a
file the guard had refused ran unasked).

- `Prefix` never suggests an interpreter: a shell, python, node, perl,
  ruby, env, xargs, sudo, ssh and the like run whatever their arguments
  say. A shell's `-c` line is suggested by the command it runs, when it
  runs one, piped into filters or not (`go test ./... 2>&1 | tail -n 40`
  is "go test"); a line of several commands is suggested nothing, and its
  prompt offers no standing answer.
- A standing approval covers a `-c` line only when every command in it is
  approved, joined by `&&`, `||` or `;`, and every command a pipe feeds is
  one of head, tail, wc, sort, uniq or grep with flags that name no file
  (`internal/guard/shellline.go`). A substitution, a variable, a subshell
  or group, a background job, an assignment, positional parameters or a
  redirection to anything but `/dev/null` (or `2>&1`) leaves the line to
  ask. So `.nodwyn/allow.toml`'s "go test" now also covers
  `bash -c "go vet ./... && go test ./x 2>&1 | tail -20"`, which asked
  before, and a shell's name in `allow_commands` covers no `-c` line.
- "Don't ask" answers what is ordinary (`Prompt.Ordinary`: not a security
  confirm, not a protected path), not what a prefix could cover, so it
  still answers a line of several commands while it is on.

## Amendment 2026-10-06: security review and project identity

[ADR 45](0045-project-trust-and-security-boundaries.md) supersedes the earlier
behaviour where it differs: repository startup and approval files need an external
user trust record; executable identity is resolved, credential transport is a user
setting with secure defaults, keychain decoding and persistence are bounded, and
project paths resolve symlinks consistently. Its compatibility rules apply to
existing sessions, workspaces, notes and worktrees.
