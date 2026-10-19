# ADR 0054: Sandbox mode, a run on a disposable machine

- Status: accepted; implemented 2026-10-18; amended 2026-10-19 (a finish
  check, and a repeated call warns). Amends 17 (the argv trigger), 18
  (unattended runs, the progress guard), 28 (the scratch directory) and 32
  (the lean loop).
- Date: 2026-10-18

## Context

Terminal-Bench gives an agent a task inside a disposable container: a
Docker image, usually with `/app` as its working directory and often
nothing in it that is a git repository, the agent running as root. When the
agent is done, the task's tests run in the same container and grade what it
left there. The tasks are a terminal's work, not a repository's: configure a
web server in `/etc`, install a package or a toolchain under `/usr/local`,
write a key into `/root`, build a program and leave its binary in place, or
start a service the tests talk to once the agent has finished. Of Terminal-Bench
2.1's 89 tasks, 41 are built on `python:3.13-slim-bookworm` and 38 on
`ubuntu:24.04`, images without git.

The owner wants nodwyn compared there with Pi and OpenCode, each set up as
well as it can be with its own defaults. nodwyn could not do these tasks:

1. The guard pins every path to the repository, the session's scratch
   directory and, for a lean session nobody approves, `/tmp` (ADR 32,
   amended 2026-10-17). A file tool's path anywhere else is refused, and so
   is a command line that names one, outright, since nobody could approve
   it. `/etc/nginx/nginx.conf` is as far out of reach as `~/.ssh`.
2. `shell-exec` stops whatever a command leaves in its process group when it
   returns (`runStopping`): a session that left the browser checks' demo
   running for good is why. A server the agent starts with `nohup server &`
   is gone before the tests run.
3. The loop assumes a repository. Its lean prompt says "working in the
   repository at /app" and "Paths are relative to the repository root"; a
   turn that changed files only through the shell, in a folder that is no
   git work tree, is told "No file has changed"; and a run may not be
   started in `/` or the home directory (`rootRefusal`), where one task's
   working directory is.

None of this is wrong for a person's checkout, where the pin is the
boundary between the agent and everything else the person has. In a
container that is thrown away after the task, the container is the
boundary, and the pin only stands in the task's way.

## Decision

1. **A run declares it.** `nodwyn run --sandbox container` (`run.start`
   and `run.resume` with `sandbox: "container"`, the only value) says the
   machine is the run's disposable sandbox. It is a property of the run, as
   `--yolo` is: not of the daemon, not of `config.toml`, not of a repository,
   and nothing a model or a project's Scheme can do turns it on. A resume is
   in sandbox mode only when it says so again. `run.state` records
   `sandbox`, and `core.Options.Sandbox` carries it to the session.
2. **Nobody is asked anything.** Sandbox mode implies `--yolo` and
   `--unattended`: ordinary confirms are answered yes and security confirms
   declined (`guard.AutoAllow`), and the run goes on after a turn only as an
   unattended run does. It excludes `--judge` and `--no-confirm`
   (`resolveOptions` refuses both). Being unattended, the session runs the
   lean loop unless `[loop] profile` or `NODWYN_PROFILE` choose otherwise.
3. **The guard, four checkers changed and the rest not.**
   - `RepoPin.Sandbox`: every path is inside, absolute or relative to the
     folder. A read-only mount stays read-only.
   - `ArgvTrigger.Sandbox` holds the session's credential denylist. A path on
     a command line, a flag's value, a glob's places and a program included,
     is outside only in the denylist's directories: nodwyn's configuration
     and data directories as the session resolved them, the credentials
     directory a service manager gave the daemon, `~/.ssh`, `~/.aws`,
     `~/.gnupg`, `~/.config/gcloud`, `~/.azure`, `~/.kube` and nodwyn's
     former names for its own. Each is judged as the system reaches it, links
     followed before a `..` is applied (`physical`), and a line naming one is
     refused outright ("in a directory nodwyn keeps off limits in sandbox
     mode too ... so it was refused and nothing of it ran"), since nobody
     could approve it. The denylist's file names (`*.pem`, `id_*`, `.env`)
     are not judged on a command line, as they are not for the repository's
     own files in any mode: `openssl ... -keyout server.key` is the work of
     several tasks. Network tools and the hard-trigger patterns (`rm -rf`,
     `git push`) still raise their ordinary confirms, which the run answers.
   - `ProtectedPaths.Sandbox`: a task's `Makefile`, `scripts/`, `.git/`,
     `.github/` and `.gitea/` are the task's to write; nodwyn's own folder,
     `.nodwyn/` (and `.nibb/`), still needs a person, and so is refused.
   - Unchanged, in sandbox mode as in every other: the denylist for the file
     tools (directories and credential-shaped names, links followed), L1's
     scan of what is written or sent, taint, the egress bindings, ingress
     redaction of what the model reads, and the scrubbed environment of the
     programs a primitive runs (`PATH`, `HOME`, `LANG`, `TMPDIR` and the rest
     of `prim.DefaultOptions().EnvAllow`).
4. **What a command starts in the background keeps running.** With
   `prim.Options.Sandbox`, `runStopping` and `runHolding` leave the
   command's process group alone when the command's own program exits
   (`piped.release`): no SIGTERM, no SIGKILL. The call still returns then:
   the output is copied until every holder of the pipes has closed them or
   for `procSettle` (a second), so what a server prints first (its address)
   is part of the result, and the writers are then cut from the call
   (`dropping`) while the pipes stay open, read and dropped, until the
   processes holding them end. A background process therefore never writes
   to a closed pipe while the daemon runs. Nothing stops such a process when
   the session ends either. Still stopped: a command aborted, or one still
   running at `shell-exec`'s time limit in the full profile; in the lean
   profile the latter is left running and stopped when the session ends, as
   for every session; and `shell-start`'s processes, which belong to the
   session. The harness's own re-run of a check (`check-without`) stops what
   it left, sandbox or not. `shell-exec`'s full description says so in
   sandbox mode ("Whatever the program starts in the background keeps
   running after it ends"); the lean one never spoke of it.
5. **The scratch directory is kept** when a sandbox session ends: the
   programs left running have `TMPDIR` set to it, and a server whose
   temporary files went there would fail once it was gone. The machine is
   thrown away with it.
6. **No git is needed, and none is made.** The folder may be any existing
   folder, `/` and the home directory included, but one of the denylist's
   directories or nodwyn's, or a folder inside one (`sandboxFolder`). A
   folder that is no git work tree has no git tools, no kept passing state
   (`Keeper`, which works on git's list of changes) and no worktree, as any
   such folder has; the version store keeps the previous content of what the
   file tools write, in the data directory. A private `git init` was
   considered and not made: most task images have no git binary to run it
   with; a `.git` in the folder, or `GIT_DIR` elsewhere with the folder as
   its work tree, would show only the folder while the task's changes are
   in `/etc` and `/usr/local` as much; and a directory the agent did not ask
   for is something a task's tests may see.
7. **The loop.** `(sandbox?)` tells Scheme. The lean prompt's first two
   sentences become "You are a coding agent working in /app, unattended:
   nobody approves or answers your calls. This machine is a disposable
   sandbox: anything on it is yours to read, change, install or run." and
   "Relative paths are relative to /app; throwaway files go in $TMPDIR or
   /tmp.", since the repository's would tell the model its paths stop at a
   repository root, which they do not there. In either profile no turn is
   told "No file has changed": the whole machine is the workspace, and a
   command may change a file anywhere on it that neither git nor the file
   tools see.

What the default refuses and sandbox mode allows, each shown both ways by
`internal/guard/sandbox_test.go`, `internal/prim/shell_sandbox_test.go`,
`internal/core/sandbox_test.go` and `internal/frontend/rpc/sandbox_test.go`:
a file tool's path outside the folder, the scratch directory and `/tmp`; a
command line naming such a path (by default a security confirm, refused
unattended); a write to the folder's `Makefile`, `scripts/` or `.git/` (by
default a security confirm, refused unattended); a background process
outliving its command and the session (by default stopped as the command
returns); the scratch directory outliving the session; a run whose folder is
the home directory; and a turn that changed nothing through the file tools
going unreminded. Refused in both: a file tool's path in `~/.ssh` or
nodwyn's data directory, a credential-shaped file name, a command line
naming a path in one of those directories (by default a security confirm,
in sandbox mode refused outright), a write to `.nodwyn/`.

## Not built

- The full profile's prompts: they still say every path must stay inside
  the repository and that `/tmp` is not to be used. Sandbox mode is meant
  for the lean loop, an unattended run's default.
- The machine's environment for commands: a variable an image sets
  (`PYTHONPATH`, a toolchain's home) is not passed, as it is not in any
  session; a command sets it itself.
- A path a command line makes as it runs is judged as it was when the call
  was made: `ln -s ~/.ssh k && cat k/id_rsa` names no protected directory.
  The same holds for the repository in every mode.
- A configuration key. A daemon or a config file that put every session in
  sandbox mode would do so for a session a person opens later from the
  console too.

## Measured

Not yet as a pass: the comparison with Pi and OpenCode is the measurement.
The adapter that runs nodwyn under Harbor lives with the benchmark host's
infrastructure, not here: it starts `nodwyn daemon --no-keychain` in the
task's container with its configuration, data and socket in directories of
its own, gives it an OpenAI-compatible provider whose proxy holds the key,
and submits `nodwyn run --sandbox container` with the task's instruction.

A smoke run on 2026-10-18, one task (regex-log, an `ubuntu:24.04` image
whose `/app` is empty and no repository) through Harbor 0.24.0 with Muse
behind such a proxy: the run completed in its one turn in 2 min 13 s, with
10 model requests, 9 `shell-exec` calls and no refusal, about half a cent.
The model found no `python3` in the image, installed it with `apt-get`, wrote
`/app/regex.txt` and its checks in `/tmp`, and tested the pattern; the task's
tests then passed in the same container. Its fifth call, `cat
/etc/os-release`, is one a lean unattended run outside sandbox mode refuses.

## Amendment 2026-10-19: a finish check, and a repeated call warns

A pass of Terminal-Bench 2.1 (89 tasks, every trial reviewed blind) showed
two things sandbox mode lacked. Both change a run in sandbox mode only;
every other session is as it was.

1. **A finish check, once.** Twice a sandbox run passed a task's checks
   with a result shaped to them and did not say so: it hard-coded three
   pixel values to make a reimplemented renderer's output byte-identical to
   the original's, and it left a deploy hook's expected pages in place "as
   a fallback", so the checks passed whatever the hook did. Now the first
   time a turn of the session ends on the model's word (a reply without
   tool calls, no reminder or review due; not a budget or progress stop, a
   cancellation, an error or a forced report), the default loop says
   `sandbox-finish-message` and the turn goes on as the evidence loop: check
   the result as someone who did not watch you work would, and replace what
   only makes your checks pass with a real solution, or remove it and say
   what does not work. It wraps the turn as this repository's completeness
   check does (`.nodwyn/init.scm`), and is said in either profile,
   `lean-quiet` or not. A resumed run is a new session and may be asked
   once more. The log has a note beginning "sandbox finish check:" each
   time.
2. **The same call four times in a row warns.** A run polled a training job
   of about fifty minutes with one command line (`sleep 55; sleep 55; tail
   -n 5 ...; date`), and the progress guard ended it at minute 52 of 57,
   before the job finished; a sandbox run is bounded by its wall-clock and
   tool-call budgets, and waiting with one command is legitimate. The
   fourth identical call, and every fourth after, now gets "[this exact
   call has now run 4 times in a row; if you are waiting on something, that
   is fine; otherwise look at what the results show before running it
   again]" after its output, kept after any fold of the result
   (`fold-result`), and the log a note beginning "progress warning:"; the
   turn goes on. The guard's other stops are unchanged.

`TestASandboxRunIsAskedOnceToCheckItsResult`,
`TestASandboxRunIsWarnedOfARepeatedCallAndGoesOn` and
`TestProgressRepeatsWarnInSandboxMode` show both, with and without sandbox
mode. Not measured yet.
