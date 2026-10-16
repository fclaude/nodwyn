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

## Amendment 2026-10-06: arguments the model wrapped in a JSON string

Of 12,109 tool results in the 161 local session logs of 2026-09-21 to 10-05,
23 were refused for the shape of their arguments, and 12 of those were a
model wrapping JSON in a string: the whole object sent as a JSON string (7,
glm: six `shell-exec` and an `fs-edit`), and `args` sent as a JSON string
holding the list (5: qwen three times, glm, qwen3.8-max). Each cost a round
to be sent again. The other 11 were names the tool does not have (`limit`,
`offset`, `timeout`, `commentary`, `parameters`) and a missing `path` or
`cmd`.

`scheme.Prim.TidyArgs` unwraps the first two. When a reply arrives
(`Loop.tidyCalls`, before the log, the strategy and the tool read it), a
call's arguments that are a JSON string holding an object, and a list or
object parameter that is a JSON string holding a list or an object, are
replaced by what the string holds, and the log gets a note saying which call
and what was read that way. A string that is not exactly that JSON is left as
it is, and `ArgsFromJSON` still refuses it. The guard is unchanged and judges
what it always judged: the values `ArgsFromJSON` makes of the arguments, which
are the same values the plain call would give. Strategies that parse a call's
`args` (the test and check detection, `shell.scm`, the read tracking) see the
unwrapped text, so a wrapped `go test` is still counted as a test run.

Names the tool does not have stay errors. Ignoring a `limit` on `fs-read`
would tell the model a bound applied that did not, and ADR 38 already refuses
an unknown field for the same reason.

## Amendment 2026-10-16: an MCP tool tells "left out" from false and 0

An optional parameter the model leaves out before one it gives arrives as
its type's empty value, and an MCP tool's handler left out of the call
every optional argument that was empty: "", 0 or #f. A false or a 0 the
model gave on purpose was therefore never sent, and the server applied its
default (a delete asked to be non-recursive went out recursive). An MCP
primitive now takes a parameter left out as void (scheme.Prim.SkipsAsVoid),
the handler and the confirm's preview leave out only void, and false, 0
and "" go to the server as given. Host primitives keep the typed empty
values. The regression is `TestMCPSendsFalseAndZeroAsGiven`.
