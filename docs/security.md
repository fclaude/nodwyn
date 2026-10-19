# Security boundaries and user choices

The implemented boundaries are the Go guard, user-owned configuration, opaque
credential sinks and redacted event ingress. Scheme chooses the agent's
behaviour; it does not decide whether a refused host operation becomes allowed.
This is a coding agent running with its user's operating-system account. Approved
commands can execute project code with that account's privileges. The guard and
secret scanner are not an operating-system sandbox, and scanning does not detect
every unknown, encoded or transformed secret.

## Repository startup is a trust decision

A repository's author supplies `.nodwyn/init.scm` and `.nodwyn/allow.toml`. A clone
cannot authorise either merely by containing those files. Ordinary startup skips
both and reports how to review them. To enable a project's extensions:

```sh
nodwyn trust --repo /path/to/project
nodwyn trust --repo /path/to/project --accept REVISION_FROM_THE_REVIEW
```

The first command only displays the files and their combined revision. The second
refuses if either file changed. Review files and scripts the init loads or invokes
too: the revision covers the two authority files, not all transitive project code.
Trust allows init to run with user origin. It still crosses the guard; startup
confirms are denied, but an applicable standing approval may permit an action.

`trusted-projects.json` lives beside `config.toml`, outside the repository and
protected from the guarded file tools. There is no Scheme primitive to grant
trust. Exact checked bytes are used for startup; adding, editing or removing an
authority file requires a new review. The same check applies on resume and reload.
The optimiser retains an edited init as a proposal until it is reviewed. A managed
worktree inherits its main project's trust only when both files match. Embedders
that explicitly set Go's `Options.InitFile` authorise that file themselves.

```sh
nodwyn trust --repo /path/to/project --revoke
```

Revocation affects future startup, resume and reload. Close and restart existing
tasks to discard code and grants they already loaded. Trust does not revoke an
already-running process or make arbitrary approved shell commands safe.

## Executable approvals

`git status` approves the resolved executable selected for `git` and that argv
prefix, subject to argument checks. `tools/git status` is a separate command
even when its basename is `git`; an explicit repository-relative executable can
be approved separately. Resolved identities are captured when approvals are
installed, so retargeting a symlink does not transfer an existing approval.
Shell wrappers and pipeline filters must also resolve to host tools. A bare PATH
name inside the repository cannot inherit a system-tool approval.

A standing approval covers only flags in the tool's allowlist in
`internal/guard/command_arguments.go`. Unknown flags need review. Flags that
select another program, an output file, a makefile or a working directory are
excluded, as are git's `--no-index`, `--ext-diff` and `--textconv`, make variable
assignments and additional make targets. Every argument is checked for absolute
and repository-escaping paths, including values inside `--flag=VALUE`, nested
flag values and symlinks. Parent-directory components (`..`) conservatively need
review because resolving a symlink before `..` can escape a cleaned path.
This also excludes an absolute path to the approved
executable: use its approved name or a repository-relative path.

When a prefix would otherwise cover unsafe arguments, the command needs a
**security confirm from a person**. Neither a standing approval, the judge nor
an unattended approval setting answers it. A statically parsed shell command
gets the same checks. Shell scripts with variables or substitutions cannot
inherit standing approval and retain their ordinary confirmation and hard
triggers. `ArgvTrigger` independently asks a person about explicit outside paths,
including flag values; the session's scratch directory retains its exception.
The home directory as the shell spells it (`~`, `~/…`, `$HOME`, `${HOME}`,
`~name`) counts as outside, and `$TMPDIR` as the scratch directory; any other
variable is read as written.
It reads a shell's `-c` line word by word, variables, substitutions, subshells
and redirection targets included, so a path anywhere in the line is seen; a
line it cannot split is read as one word. A program in a Python environment's
`bin` folder (conda or virtualenv) that only root or the person can change
runs without the question, as a program on `PATH` does, and sed, awk, grep and
rg scripts or patterns and `git log -L` ranges are not read as paths (ADR 17,
amended 2026-10-10). What the command does with a path decides the rest: a
program that only lists names and metadata (`ls`, `find` without actions,
`stat`, `which`) may name any path, except inside a substitution or in a line
that reads its input for arguments (`xargs`, `parallel`, `read`); one that only
reads (`cat`, `grep`, `head`) and `source` may use paths in such an
environment; what `echo` and `printf` print is no file access, unless it is in
a substitution (quoted or not), goes into a pipe or the line has such a reader;
a `for` list may name only a Python environment's paths and the system's
binaries, since its body takes each word as a path; and an assignment's
`:`-separated paths are judged one by one. `>&FILE` is a redirection to FILE,
as `&>FILE` is; `>&2` and `2>&1` are descriptors. A command that writes or
runs what it names outside, a read outside an environment and `cd` outside still
ask. The model may therefore list any folder unasked, names but not contents
(ADR 17, amended 2026-10-19). The network tools and the hard-trigger patterns
are looked for in each command of a `-c` line as well as in the argv, so
`bash -c "curl …"` and `git  push` ask as `curl` and `git push` do.

The trigger reads words, not code. A path inside an interpreter's program
(`python3 -c`), in a nested `sh -c` or `eval` string, in a here-document or
here-string handed to `xargs`, or in a file a command wrote earlier and another
reads, is not seen.

These checks inspect argv and executable paths, not binary signatures or the
behaviour of an approved process. Builds and tests execute project code with the
user's operating-system privileges, can write files and can use the network.
The file-tool denylist does not mediate those processes' file access.

The denylist judges the file a path reaches, not only the name a call uses: a
link is matched by its own name and by the name of the file it points at, so
`notes.png` linked to `.env` is refused as `.env` is, in every file tool and in
the images `doc-export` embeds. Names are matched ignoring case, since macOS's
file system opens `.env` for `.ENV`.

## Credentials and HTTP

Credential injection into a header, query or body requires HTTPS by default.
A key injected into a query is part of the request's address, and a send that
fails (a refused connection, a timeout) is reported with the address stripped
of its query, so the key never comes back in the call's error, the log or the
model's context. A server that repeats the query in its reply is not covered:
prefer a header position when the service offers one.
HTTP is accepted for `localhost` and literal loopback addresses. For a deliberately
insecure remote service, set this in the **user's** config.toml:

```toml
[policy]
allow_insecure_http = true
```

The same switch is in **Settings › Keys & devices › Credentials over remote HTTP**.
False is the default. New and resumed tasks take the saved value; existing tasks
keep theirs. Provider setup, tests and model-list requests use the current user
setting. On HTTP, anyone able to intercept the route can read or change credentials
and content. A binding is still required, redirect responses remain refused, TLS
certificate checks remain enabled, and ChatGPT plan credentials retain their
special fixed sinks. This switch does not relax separate MCP, OAuth endpoint or
Google upload URL validation. Requests without credentials are governed by their
own existing endpoint rules.

## Workflows: what the daemon asks, and what it sends

A workflow (ADR 53) makes the daemon send requests nobody typed at the moment
they go out, so the boundaries are stated here.

- **Who makes one.** A workflow, its source and its answer are made, changed
  and removed on this computer, never from a paired device (the daemon refuses
  it, whatever the console's tier allows), which may only see it, pause it and
  ask it to check now. Its
  definition is a record in nodwyn's data directory (`workflows/<id>.jsonl`),
  written by the daemon that holds the scheduler lock; nothing in a repository
  writes one, so a stranger's clone cannot add a workflow or change a source.
- **How a source is asked.** A poll of a forge or Gmail goes through
  `core.Keyed.Authorize`, the check a task's own requests meet: the key goes
  only where the binding table allows that credential at that host for that
  method, within the scopes it was authorized for, over HTTPS unless Settings
  allows plain HTTP, with an OAuth2 token refreshed first and no redirect
  followed. A poll lists pull requests or the newest messages and nothing
  else (GET); a host without a binding is refused and said on the workflow.
- **What an event is.** What a pull request or an email says is attached to
  the run as a file and framed as data. The instruction is the workflow's own
  words, so a description that says "ignore your instructions" is a quoted
  description. The event passes the workflow's filters, which read its fields,
  before any model is asked.
- **What can be sent out.** Only a pull-request review comment and a saved
  Gmail draft, and never from the run: the model has no way to send, and the
  run's answer is held in the outbox until a person posts it (or the person
  turned on **Without asking** for that workflow, on this computer). The host
  posts it with a key bound for that one call, which needs its own POST
  binding. A review is never an approval or a request for changes; a draft is
  never sent; header values are stripped of line breaks.
- **A command source** runs a program the person named, with no shell, as an
  absolute path or a name on `PATH` (a relative path into the folder is
  refused, since a repository would choose what runs), in the workflow's
  folder, for at most a minute, with its output capped. It inherits the
  daemon's environment. It is a program of this person's, run on their word
  when the workflow is saved here, not something an event can choose; the
  guard's approvals do not apply to it, which is why only this computer may
  save one.
- **Limits.** At most 20 events at one check (more pauses the workflow), a
  number of runs at once, and a spend limit per run and per day, so a hostile
  source cannot start unbounded work.

These paths have been exercised against fakes of Gitea, GitHub and Gmail, not
the real services. The exfiltration suite (`make exfil`) covers the guard and
the keychain; it does not drive a workflow.

## Keychain input and persistence

The existing XChaCha20-Poly1305 file format and default Argon2id parameters remain.
Before deriving a key, readers require a regular, non-symlink file of at most
16 MiB, a complete header, nonce and authentication tag, and bounded parameters:
time 1–10, threads 1–16, memory 8 KiB per thread through 256 MiB, and time times
memory at most 512 MiB. Invalid parameters return errors; they cannot request
unlimited memory or zero-round Argon2 work. A previously custom keychain outside
these supported bounds is refused, not silently rewritten.

Writers use a unique exclusive 0600 sibling, sync it, rename it, then sync the
parent directory. A planted `keychain.tmp` is unused. Cooperative changes retain
the sibling lock and reread-before-change rules. For local entry changes and
passphrase changes, a failure before publication restores the previous in-memory
state. OAuth refresh retains a received token in memory when saving fails, because
the provider may already have invalidated the previous token, and keeps it across
rereads of the file until a save publishes it; the refresh reports the save error. A directory-sync failure after publication
is reported as saved with uncertain durability; memory keeps the newly published
key. This protocol reduces crash-loss windows; tests do not simulate power loss.
A keychain without a passphrase remains only as private as the file and its backups.

## Existing project data

Folder identities resolve symbolic links, including macOS `/tmp` and `/var`
aliases. Registrations, session summaries, MCP workspace selection, kept file
versions and notes use that identity. Existing logs and version indexes are read
compatibly. On startup, old notes named by the requested path or recorded sessions
are migrated, leaving `notes.md.before-path-migration` beside the old file.
Different note contents require manual reconciliation. Duplicate registered
aliases require choosing one entry and its intended mounts; permissions are never
silently combined. Symlinks should continue to point to the same project during
this migration; unknown historical aliases cannot be recovered from a path hash.

The detailed decision and validation cases are in [ADR 45](adr/0045-project-trust-and-security-boundaries.md).
