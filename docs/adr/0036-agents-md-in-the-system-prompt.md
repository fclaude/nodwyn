# ADR 0036: A repository's AGENTS.md joins the system prompt

- Status: accepted; implemented 2026-10-02
- Date: 2026-10-02

## Context
What a model needs to know to work well in one repository, how its changes
are validated there and what the house rules are, had two places in
nodwyn: the working rules (ADR 32), which are the same for every
repository, and the project notes, which the agent writes for itself and
the prompt calls leads to check. Neither is the maintainers' word. In the
jev-key console change (ADR 32, amended 2026-10-02) the model ran Go tests
and `node --check` for four turns while the repository's browser check for
that very page sat in `test/browser`. Nothing it was given said that a
change to the console is validated there.

`AGENTS.md` at a repository's root is the convention other coding agents
read for this: a README for agents, written by the people who maintain the
code.

## Decision
- `(project-instructions)`, a hidden primitive, returns the root's
  `AGENTS.md`, or `agents.md` when there is no `AGENTS.md`, and `""` when
  there is neither.
- `(project-instructions-section)` adds it to both system prompts, after the
  working rules, the git rule and the workspace's folders and before the
  project notes, under a heading that says it is the maintainers'
  instructions: follow them where they apply, and follow the request where
  the two differ. Maintainers' instructions come before the agent's own
  notes, and the request, which a person wrote for this task, wins.
- It is read once, when the session starts, like the rest of the system
  prompt (ADR 9): the prompt stays one prefix for the provider's cache, and
  an edit to the file shows in the next session.
- The file is the repository's, so it may be anything a clone brought, and
  its text goes to the model with every request. It is read only as a
  regular file inside the root after links are followed, and only if the
  credential denylist passes it under both its name and its target's; a
  link to a key elsewhere reads as no file. Its text is redacted like a
  tool result, and capped at 32 KiB, cut at a line, with a line that says
  the file goes on and that `fs-read` reads the rest.
- Only the root's file is read. Files in subdirectories, which the
  convention lets apply to their subtree, are not looked for: a repository
  that has them can say so in the root's file.
- No setting turns it off. A project that wants it out redefines
  `(project-instructions-section)` in `.nodwyn/init.scm`, as it would any
  other part of the prompt.

## Consequences
- A repository with an `AGENTS.md` has a longer system prompt in every
  session, by the size of the file.
- The instructions are text for the model, not policy: what they ask still
  crosses the guard. A file that tells the model to send something
  somewhere gets no further than the binding table, the denylist and the
  confirms allow, whatever it says.
- nodwyn's own repository gains an `AGENTS.md`: how a change there is
  validated (the Go checks, the protocol schema, the browser checks for the
  console, the prompt tests), and the rules that the failures seen in its
  sessions' logs call for.

## Amendment 2026-10-18: the lean prompt carries it too

The lean profile (ADR 32, amended 2026-10-17) brought a third system prompt,
`lean-system-prompt`, which a session nobody approves runs by default, and it
did not call `(project-instructions-section)`: until 2026-10-18 no lean
session had the repository's `AGENTS.md` in its prompt. It now
ends with the same section, under the same heading, after its last fixed
sentence, so the fixed part stays one prefix for the provider's cache and a
repository without the file gets the lean prompt unchanged. The cap, the
redaction, the reading rules and a project's redefinition of the section
apply as above. The lean prompt still has no project notes: those are the
agent's own, not the maintainers' word (ADR 32, amended 2026-10-18).
