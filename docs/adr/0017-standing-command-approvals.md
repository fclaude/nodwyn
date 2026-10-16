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

## Amendment 2026-10-06: standing approvals review command arguments

At `fcc5c42`, an approved prefix covered flags that gave the command new
authority. A guarded `git diff --output=...` wrote outside the repository with
no prompt, and `git diff --no-index ... /dev/null` returned an outside file's
contents. The reproduced guard cases also covered git's pager, external diff
and text conversion, Go's execution and output flags, and make overrides.

We choose **security confirmation by a person**, rather than the ordinary
judge-then-person path, when arguments would extend a standing prefix's
authority. Selecting executable code or crossing a filesystem boundary should
not depend on a model's judgement. The command is still available after the
person approves that call; neither a standing approval nor an unattended
approval setting supplies that answer.

- Prefix and resolved executable identity remain necessary. A per-tool flag
  allowlist in `internal/guard/command_arguments.go` is also required. Flags
  selecting programs, output files, makefiles or working directories are absent;
  unknown flags ask. Go arguments forwarded to test binaries and git's
  `--no-index`, `--ext-diff` and `--textconv` cannot inherit approval.
- A make variable assignment or an additional make target cannot inherit a
  prefix. Approving `make test` does not approve `make test deploy`.
- Every argv word is checked for absolute and repository-escaping paths,
  including `--flag=VALUE`, nested values, paths with spaces and symlinks.
  Parent-directory components (`..`) always ask: cleaning them before resolving
  a preceding symlink could otherwise conceal an escape.
  Absolute executable paths also cannot inherit approval. Use the approved
  program name or an explicitly approved repository-relative executable.
- Static shell wrappers and pipeline filters get the same path checks. Dynamic
  scripts, substitutions and variables retain the earlier ordinary confirmation
  and hard triggers; they never inherit a standing prefix. `ArgvTrigger`
  independently makes explicit outside paths, including flag values, a security
  confirm. Its exception for the session's scratch directory remains; scratch
  paths still cannot inherit a standing command approval.
- A standing approval outranks only an ordinary confirm. Denies and security
  confirms continue to win. These rules apply equally to user configuration,
  trusted repository approvals and session-only prefixes.

The allowlist is deliberately conservative: a harmless but unrecognised flag
asks until reviewed. It checks the command line, not everything the process
will do. Builds and tests execute project code, write outputs and caches, and
may use the network with the user's operating-system privileges. The original
description of the defaults as read-only was incorrect. The file-tool denylist
does not sandbox a child process.

Evidence: `TestStandingApprovalsRespectArgumentBoundaries` uses model origin,
`prim.Checkers` and `NewCommandAllowIn` for direct and shell-wrapped commands.
`TestArgvTriggerSeesPathsInsideFlagValues` exercises the independent checker.
`TestStandingGitCannotReadOrWriteOutsideTheRepository` runs real git behind the
guard with a non-credential-shaped canary, checking the outside output file and
the Scheme result, output and log. All three tests compile against `fcc5c42`
and fail there. Existing scratch and judge tests preserve their confirmation
behaviour; executable identity tests still cover a retargeted relative symlink.

## Amendment 2026-10-10: the argv trigger reads a script word by word

On the long-context SWE-bench cohort (ADR 32, amended 2026-10-10), 223 of
nodwyn's shell commands were refused in 94 of its 107 attempts, all as
security confirms for a path outside the repository, which an unattended run
cannot answer. 98 named the project environment's interpreter
(`/opt/miniconda3/envs/testbed/bin/python`), which the system prompt tells the
model to find and use; 48 were `-c` lines that `shellLine` would not split (a
variable, an interpreter, a continued line) and that the trigger therefore read
as one word, so `PYTHONPATH=/testbed python \…` was flagged as the path
`/testbed python \` though `/testbed` is the repository; a few were sed
addresses (`/^def polyval/,/^def /p`) and a `git log -L` range. Each was
answered by a way round the guard (`conda run`, a script, Python walking
`/opt`), which the guard does not see at all. The same one-word reading hid
paths: a redirection to a file outside, `cat $(ls /outside)`, or
`cat "$X" /etc/hostname`, asked nobody.

- `ArgvTrigger` splits a shell's `-c` line with `scriptWords`
  (`internal/guard/scriptwords.go`) whatever it holds: variables,
  substitutions, subshells, background jobs, assignments and redirections.
  Each command's words and each redirection's target are judged on their own;
  a backslash before a newline joins lines, as in the shell; a here-document's
  body is data, as it was. A line it cannot split (an unclosed quote), or one
  with positional parameters, is read as one word, as before. `shellLine`
  still alone decides what a standing approval covers.
- A command's program, or the program a wrapper runs (`env`, `timeout`,
  `nice`, `nohup`, `time`, `exec`, `command`), needs no question when it is in
  the `bin` folder of a Python environment, conda (`conda-meta`) or a
  virtualenv (`pyvenv.cfg`), and the program, its real folder, that `bin`
  folder and the environment's folder belong to root or the person and nobody
  else may write them (`envExecutable`). That is the authority a program on
  `PATH` has: its arguments are still read, the same path given as an
  argument is still asked about, and an environment another user could have
  put there is not let through.
- sed's and awk's script, grep's, rg's and `git grep`'s pattern, the values of
  their `-e`, and `git log`'s `-L` range are not paths; the files they read
  (`-f`, the operands) still are. `/dev/null` and the other standard devices
  are no outside path.
- The file tools' refusal of a path outside the repository and the scratch
  directory now says where a throwaway file goes, written as
  `$TMPDIR/<name>`: 68 writes were refused so in 56 attempts.

The checks a person answers are otherwise unchanged: a path outside is still
a security confirm, never covered by a standing approval, the judge or an
unattended setting. Evidence: `TestArgvTriggerReadsAScriptWordByWord`,
`TestArgvTriggerLetsAnEnvironmentsInterpreterRun`,
`TestArgvTriggerReadsPatternsAsPatterns` and
`TestRepoPinRefusalNamesTheScratchDirectory` fail on `d208b2c` and pass with
the change. `TestTheJudgeIsToldOfAScratchScript` named `/etc/hostname` in a
line with a variable and expected the judge to answer; it now names a
repository file, since the outside path is a question for a person.

## Amendment 2026-10-10: what a command does with a path

Reading a `-c` line word by word (above) saw every path in it, and asked
about more than before. Replayed through the trigger, the 2,874 shell commands
of nodwyn's long-context benchmark attempts and the first canary of `85695c5`
gave 442 questions, against 226 when they ran: 115 of those were gone, and 331
commands that had run unasked now asked. The 331 were the trigger seeing what
the one-word reading had hidden, and nearly all of them found or used the
project's environment: `source /opt/miniconda3/etc/profile.d/conda.sh` (89),
`ls /opt/*/envs` and `find / -maxdepth 6 -name python3.6`,
`PY=$(ls -d /opt/*/envs/*/bin/python)`, reads of its installed packages, and
`for p in /usr/bin/python3 …; do $p -V; done`. The canary of `85695c5` asked
once in 36 commands, for `P=/opt/miniconda3/envs/testbed/bin/python`.

A path outside the repository asks a person because the command may read what
is there, change it or run it. So the trigger now asks what the command does
with its paths (`access`, `internal/guard/scriptwords.go`):

- A program that shows only names and metadata (`ls`, `stat`, `which`,
  `readlink`, `realpath`, `du`, `test`, and `find` without `-exec`, `-ok`,
  `-delete` or `-fprint`) may name any path. Inside a substitution, whose
  output may become another command's arguments (`cat $(ls -d /etc/*)`), it
  may name only a Python environment's paths.
- A program that only reads (`cat`, `head`, `tail`, `grep`, `rg` without
  `--pre`, `wc`, `diff`, `sort` without `-o`, …) may read in a Python
  environment, a conda or virtualenv folder only root or the person can
  change (`inEnvironment`, globs matched). `sed` and `awk` are not readers:
  their scripts can write and run commands.
- `source` and `.` may run a script in such an environment, the script and its
  folder owned as the environment must be: conda's activation scripts.
- A `for` loop's list and what `echo` and `printf` print are not file
  accesses.
- An assignment, before a command or after `export` and its kin, names its
  paths: each element of a `:` list is judged on its own, and an
  environment's may be named (`PATH=…/envs/testbed/bin:$PATH`,
  `PYTHONPATH=/testbed:/testbed/tests`).
- A program is always judged as a program: `/elsewhere/ls` is no names-only
  `ls`.
- The scratch directory written with a trailing slash is the scratch
  directory. A refused `/tmp` path says where a throwaway file goes.

Everything else asks as before: a command that writes or runs what it names
outside (`cp`, `python3 /tmp/x.py`, `find -exec`, `sort -o`), a read outside
an environment (`cat /etc/…`), `cd` outside, and a redirection to a file
outside. Replayed again, the 2,874 commands ask 39 times: `/tmp` paths,
mistyped scratch paths, the repository's parent, `/dev0` typos, a read of
`/etc/ld.so.preload`, `cd /`, and three environment globs that matched nothing
in the replay's stand-in tree. The model may now list any folder without the
question, `~/.ssh` included: names, never contents, which the person chose
over a question on every search for the environment.

`TestArgvTriggerAsksWhatACommandDoesWithAPath` fails on `85695c5` and passes
with the change. `TestArgvTriggerLetsAnEnvironmentsInterpreterRun` asked
about `cat` on the environment's interpreter, a read now let through; it asks
about `cp` over it instead.

## Amendment 2026-10-16: the refusal of the repository's own absolute path says what to write

The rule is unchanged: no standing approval covers an absolute path, the
repository's own included. The system prompt gives the model the repository's
absolute path, and on commit-bench (dsl20r, 7513f7eb) an unattended run's
`cd <repository> && go test ...` was refused for good with a reason that
said only that the path was absolute. When the path resolves inside the
repository (and has no `..`), the reason now adds that the command already
runs in the repository root, so the path is written relative to it
(`pathRisk`, `TestAbsoluteRepositoryPathRefusalSaysToWriteItRelative`).
Nothing else about the decision changes; a path outside the repository gets
the reason it had.
