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
- A call may stop before any of them. `ArgsFromJSON` leaves missing or
  `null` optional arguments at the end off: handlers see fewer arguments
  and branch on `len(c.Args)`; Scheme callers write `(fs-read path)` or
  `(fs-read path 120 50)`.
- An optional argument skipped before a later one that is given gets its
  type's empty value (`""`, `0`, `#f`, `()`), so handlers must treat the
  empty value as "not given" (fs-read's start 0 is line 1, index-grep's
  glob "" is every file). Amended 2026-09-28: the first version refused
  such calls ("argument context needs glob too"), and on the first
  SWE-bench dev tasks with these tools that was 10 of the 17 failed file
  tool calls: models pass options by name, not position.
- Arity accepts any count from the required ones to the total.
- Guard checkers and previews already read arguments by position with a
  length check; that is now a rule for new ones too.
- `register-tool` does not declare optional parameters yet: Scheme tools
  are all-required until a use needs more.

## Consequences
Tools can gain options without new names or breaking project code. Replay
is unaffected: requests carry the tool schemas recorded in the log.
