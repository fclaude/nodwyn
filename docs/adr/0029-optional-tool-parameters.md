# ADR 0029: Optional tool parameters

- Status: accepted
- Date: 2026-09-28

## Context
Every parameter of a primitive was required, so a tool could not grow a
refinement without breaking its callers: `fs-read` could not take a line
range, `index-grep` a file glob, `fs-edit` a replace-all switch. Models
worked around the gaps through the shell (on the SWE-bench runs, about
1,100 `sed -n`, `head`/`tail`/`cat` and `python -c` reads against 586
`fs-read` calls), where the reads escape the file tools' guard rules and
their results the structure the window relies on.

## Decision
A Go primitive may declare trailing parameters `Optional`.

- Optional parameters come last; `Register` refuses a required one after
  an optional one, and optional parameters on a variadic primitive.
- The tool schema leaves them out of `required`.
- A call may stop before any of them. `ArgsFromJSON` treats a missing or
  `null` optional argument as the end of the list, and a later one given
  after a missing one is an error ("argument count needs start too"),
  since positions would shift. Handlers see fewer arguments, never
  placeholders, and branch on `len(c.Args)`; Scheme callers write
  `(fs-read path)` or `(fs-read path 120 50)`.
- Arity accepts any count from the required ones to the total.
- Guard checkers and previews already read arguments by position with a
  length check; that is now a rule for new ones too.
- `register-tool` does not declare optional parameters yet: Scheme tools
  are all-required until a use needs more.

## Consequences
Tools can gain options without new names or breaking project code. Replay
is unaffected: requests carry the tool schemas recorded in the log.
