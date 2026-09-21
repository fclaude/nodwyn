# ADR 0017: Standing command approvals

- Status: accepted
- Date: 2026-09-21

## Context
`shell-exec` is irreversible, so every command needs a confirm. The dev
tools nibb uses to build itself (`go-test`, `git-diff`, `grep-repo`) wrap
`shell-exec`, which would mean a prompt for every test run — confirm fatigue
turns "y" into a reflex, which is worse than no prompt.

## Decision
The user may approve command lines ahead of time, by argv prefix ("go test",
"git status", "rg"). Approvals live in three places: `policy.allow_commands`
in `config.toml` (global), `<repo>/.nibb/allow.toml` (per repository,
tracked), and "a" answers to a confirm prompt (this session only, not
persisted). The guard checker `CommandAllow` turns a match into a *standing*
Allow, which outranks any Confirm from another checker and never outranks a
Deny. Every use is logged as an allow with the matching prefix as reason.

The default lists are read-only and test commands. There is no approval for
anything that commits, pushes, deletes or reaches the network; those stay
per-call confirms.

## Consequences
- This is the command-line analogue of the binding table (SPEC): the user
  authorizes ahead of time, the harness applies silently and logs.
- The model can edit `.nibb/allow.toml` through `fs-write`/`fs-edit`; that
  is a confirmed write whose diff shows the new entry, and it takes effect
  only at the next session start.
- "a" persists nothing: standing approvals that should outlive a session are
  written down by the user, where they can be reviewed.
