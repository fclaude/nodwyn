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
prefix. `tools/git status` or a different absolute executable is a separate
command even when its basename is `git`. Explicit paths can be approved on their
own. Resolved identities are captured when approvals are installed, so retargeting
a symlink does not transfer an existing approval. Shell wrappers and pipeline
filters must also resolve to host tools. A bare PATH name inside the repository
cannot inherit a system-tool approval. This checks paths, not binary signatures;
an approved build or test can still run the project's own code.

## Credentials and HTTP

Credential injection into a header, query or body requires HTTPS by default.
HTTP is accepted for `localhost` and literal loopback addresses. For a deliberately
insecure remote service, set this in the **user's** config.toml:

```toml
[policy]
allow_insecure_http = true
```

The same switch is in **Settings > Approvals > Credentials over remote HTTP**.
False is the default. New and resumed tasks take the saved value; existing tasks
keep theirs. Provider setup, tests and model-list requests use the current user
setting. On HTTP, anyone able to intercept the route can read or change credentials
and content. A binding is still required, redirect responses remain refused, TLS
certificate checks remain enabled, and ChatGPT plan credentials retain their
special fixed sinks. This switch does not relax separate MCP, OAuth endpoint or
Google upload URL validation. Requests without credentials are governed by their
own existing endpoint rules.

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
the provider may already have invalidated the previous token; the refresh reports
the save error. A directory-sync failure after publication
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
