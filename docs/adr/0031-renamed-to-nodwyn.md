# ADR 0031: The project is renamed nodwyn

- Status: **accepted**
- Date: 2026-09-30

## Context
Reaching a computer from a phone without a tailnet needs a public service,
and a public service needs a name that can be registered and defended. On
2026-09-30 `nibb.com` was parked, `nibb.io` served a newly launched "Nibb"
AI calorie tracker offering MCP, `nibb.app` had been registered in August,
and an AI nutrition app had filed "NIBBY" with the USPTO. The name was fine
for a private tool and poor for a public one.

`nodwyn` (*nod*, the approval given before anything acts, with the Welsh
*-wyn* ending) had a free `.com`, `.app` and `.dev`, no USPTO record under
that name or a similar one, and was free on npm, PyPI, Homebrew, crates.io
and GitHub. This was a screening, not a legal clearance.

## Decision
Everything is renamed: the Go module (`github.com/fclaude/nodwyn`), the
command (`nodwyn`, `nodwyn-stable`, `nodwyn-schema`), `Nodwyn.app` (bundle
identifier `com.nodwyn.app`), the config and data directories
(`~/.config/nodwyn`, `~/.local/share/nodwyn`), the directory kept in each
folder (`.nodwyn/`), environment variables (`NODWYN_*`), cookies, the
console's names, the Scheme turn procedure (`nodwyn-turn`) and the docs.

What exists on disk moves once, never over something that exists:

- On startup, `~/.config/nibb` and `~/.local/share/nibb` are renamed to the
  new directories when those do not exist yet (default layout only), and
  nodwyn says so on stderr.
- Opening a session in a folder, or saving or listing it as a workspace,
  renames its `.nibb/` to `.nodwyn/` when only the former exists.

Kept under the old name, on purpose:

- The keychain's file-format marker `NIBBKC01`: it is part of the header
  layout, and changing it would make every existing keychain unreadable.
- The recorded sessions in `test/sessions`: they are replayed byte for byte.
- ADR history keeps its decisions; its text now says nodwyn.

## Consequences
- The guard keeps denying `~/.config/nibb` and `~/.local/share/nibb`, and
  keeps `.nibb/` protected, so a folder or directory that has not moved yet
  is guarded as before.
- A project's `.nibb/init.scm` that redefined `nibb-turn` must rename it to
  `nodwyn-turn`; otherwise the default turn runs.
- Browsers paired before the rename pair again (cookie names changed); a
  saved light/dark choice in the console resets once.
- `nibb-stable` in `~/.local/bin` is not touched; the next `make promote`
  installs `nodwyn-stable`.
