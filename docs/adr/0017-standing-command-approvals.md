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
