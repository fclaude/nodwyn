# nodwyn Scheme — language reference

nodwyn embeds a small Scheme for **orchestration only**. Scheme code decides
*what to do*; every effect — reading a file, running a command, calling a
model — is a Go **primitive**, and every primitive application crosses the
guard. Scheme never touches the network or the filesystem on its own.

This document is the contract for the language (ADR 7). The interpreter
implements what is written here and nothing else; the conformance suite has a
snippet for every form and builtin below, and a builtin that is not documented
here fails the suite.

Status: **v0 kernel (P1b)**. §10 lists what is deliberately absent.

---

## 1. Lexical syntax

| Token | Notes |
|---|---|
| `;` … end of line | comment |
| `#|` … `|#` | block comment; may span lines; `|#` ends it (nesting is not supported). Unterminated is *incomplete* input |
| `(` `)` | list delimiters; `[` `]` are **not** accepted |
| `#(` `)` | a vector: `#(1 2 "x")` |
| `'x` `` `x `` `,x` `,@x` | `(quote x)` `(quasiquote x)` `(unquote x)` `(unquote-splicing x)` |
| `"…"` | string; escapes `\"` `\\` `\n` `\t` `\r`, `\xHH` (one byte), `\uHHHH` and `\UHHHHHHHH` (one code point); may span lines |
| `"""…"""` | raw string: no escapes, so the text is exactly what the file holds. See §1.1 |
| `42` `-7` `+3` | integer (int64) |
| `3.14` `1e3` `-0.5` `.5` | float (float64) |
| `#t` `#f` | booleans |
| `.` | dotted pair inside a list: `(a . b)` |
| anything else | a symbol; case-sensitive; `+`, `-`, `...`, `1+` are symbols |

A token is parsed as an integer if `strconv.ParseInt` accepts it, else as a
float if `strconv.ParseFloat` accepts it, else it is a symbol.

Unbalanced input is reported as *incomplete*, which the REPL uses to keep
reading lines.

### 1.1 Raw strings

`"""` begins a raw string and the next `"""` ends it. Nothing between them
is an escape, so quotes and backslashes are themselves:

```scheme
(define pattern """\d+\.\d+""")
```

When the opening delimiter is followed immediately by a newline the block is
read as **indented text**: that first newline is dropped, the indentation
shared by every non-blank line and by the closing delimiter's line is
removed, and the newline before the closing delimiter is dropped. A block
therefore reads at the indentation it is written at and produces text with
none:

```scheme
(define instructions
  """
  Prefer table-driven tests.
  Run `make accept` before saying a change is done.
  """)
```

is exactly `"Prefer table-driven tests.\nRun `make accept` before saying a
change is done."`. Lines indented further than the margin keep the
difference, and blank lines stay blank.

Anything else is verbatim: `"""a "b" c"""` is `a "b" c`.

Note that `"""` always starts a raw string, so an empty string written
immediately before another string (`"""x"`) is read as a raw string rather
than as `""` followed by `"x"`. Separate them, as ordinary formatting
already does.

## 2. Data types

| Type | Written as | Predicate | Notes |
|---|---|---|---|
| empty list | `()` | `null?` | must be quoted in code: `'()` |
| boolean | `#t` `#f` | `boolean?` | only `#f` is false; everything else is true |
| integer | `42` | `integer?` | int64; wraps on overflow like Go |
| float | `2.5` `1.0` | `float?` | float64; always printed with a `.` or exponent |
| string | `"hi"` | `string?` | immutable UTF-8; lengths and indices count **runes** |
| symbol | `foo` | `symbol?` | interned by name |
| pair / list | `(1 2 3)` `(a . b)` | `pair?` `list?` | lists are chains of pairs ending in `()`; no mutation |
| vector | `#(1 2 "x")` | `vector?` | indexed array; `vector-set!` mutates; no mutation of lists |
| procedure | `#<procedure f>` `#<primitive car>` | `procedure?` | closures and primitives are both procedures |
| handle | `#<cred:class:id>` | `handle?` | opaque credential handle (P3). Cannot be converted to a string by any builtin |
| void | `#<void>` | — | result of `define`, `set!`, `display`, …; the REPL does not print it |

`number?` is true for integers and floats.

## 3. Evaluation

- A symbol evaluates to its binding; an unbound symbol is an error.
- Numbers, strings, booleans and void are self-evaluating.
- `()` unquoted is an error ("empty application").
- A list whose head is one of the **special form** names below is that form.
  These names are reserved: binding one as a variable does not change how the
  form is parsed (this includes `with-output-to-string`).
- Any other list is an **application**: the head and every argument are
  evaluated left to right, then the procedure is applied.
- **Proper tail calls.** The last expression of a `lambda` body, `begin`,
  `let`/`let*` body, `cond` clause, `and`/`or`, and either branch of `if` is
  in tail position. A loop written as a tail-recursive function runs in
  constant stack space.

### Special forms

| Form | Meaning |
|---|---|
| `(define name expr)` | bind `name` in the current environment |
| `(define (name param … [. rest]) body …)` | shorthand for a named `lambda` |
| `(lambda (param … [. rest]) body …)` `(lambda args body …)` | closure; `rest`/`args` receive a list |
| `(if test then [else])` | missing `else` yields void |
| `(let ((v e) …) body …)` | bind in parallel |
| `(let name ((v e) …) body …)` | named let: `name` is bound to the loop procedure inside the body |
| `(let* ((v e) …) body …)` | bind sequentially |
| `(letrec ((v e) …) body …)` | mutual bindings: every initializer may reference the other names (a closure resolves them at call time) |
| `(case key [(datum …) body …] … [(else body …)])` | first clause whose datum list contains a member `equal?` to the key wins; no clause → void |
| `(do ((v init [step]) …) (test [e …]) cmd …)` | loop: check `test`; on false evaluate the `cmd`s, then the `step`s (in parallel), rebind, repeat. Value is the test clause's last `e` (void if none). A Go loop — constant stack |
| `(when test e …)` | `e …` only if `test` is true; yields void otherwise |
| `(unless test e …)` | `e …` only if `test` is false; yields void otherwise |
| `(guard (var clause …) body …)` | evaluate `body …`; if it raises a Scheme error, bind `var` to the condition — an alist `((message . "text") (irritants …))` — and try the clauses like `cond` (`(test body …)`, `(test => proc)`, `(else …)`). The first true clause's value is the value of `guard`; no match re-raises. A guard **denial** and a **cancellation** are never catchable |
| `(with-output-to-string e …)` | evaluate `e …` with `display`/`write`/`newline` captured; return the captured text (a minimal string port) |
| `(begin e …)` | evaluate in order, yield the last |
| `(set! name expr)` | assign an **existing** binding; error if unbound |
| `(quote d)` | the datum itself |
| `(quasiquote d)` | like `quote`, but `(unquote e)` evaluates `e` and `(unquote-splicing e)` splices a list. One level only: nested quasiquote is not supported |
| `(cond (test body …) … [(test => proc)] [(else body …)])` | first true clause wins; `=>` passes the test value to `proc`; no clause → void |
| `(and e …)` | short-circuit; `(and)` is `#t` |
| `(or e …)` | short-circuit; `(or)` is `#f` |

`define` inside a `lambda` body binds in that call's environment (internal
definitions). When `define` names a fresh anonymous closure, the closure takes
that name for printing and error messages.

## 4. Errors

`(error message irritant …)` raises an error whose text is `message` followed
by the written irritants. Runtime failures (unbound variable, wrong argument
count, wrong type, division by zero, a denied primitive) raise the same kind
of error with a message of the form `who: what`.

Errors may be caught with `guard` (§3):

```scheme
(guard (e ((string-contains? (assoc-ref e 'message) "boom") 'caught)
          (else (assoc-ref e 'irritants)))
  (error "boom" 1 2))            ; caught
```

An error not caught by any clause unwinds to the host, which reports it (the
REPL prints it, the agent loop feeds it back to the model as the tool result).

A primitive the guard denies raises `<name>: denied: <reason>` (`fs-read: denied: …`). Denials and
cancellations (an abort, a timeout) pass through `guard` untouched: a program
cannot hide that it was refused, and an abort always unwinds. So do the agent
loop's stops (§9): a spent budget, the tool-call limit, a progress stop and a
log that can no longer be written end the turn whatever the strategy's `guard`
clauses say.

## 5. Builtins

Signatures use `…` for variadic and `[x]` for optional. Every builtin is a
primitive in the table (§7) and can be listed with `(primitives)`.

### Numbers

| | |
|---|---|
| `(+ n …)` `(* n …)` | sum / product; `(+)` is `0`, `(*)` is `1` |
| `(- n m …)` | subtraction; `(- n)` negates |
| `(/ n m …)` | division; `(/ n)` inverts. Two integers divide exactly when the result is integral, otherwise yield a float. Division by zero is an error |
| `(= a b …)` `(< a b …)` `(> a b …)` `(<= a b …)` `(>= a b …)` | numeric comparison chains; mixed int/float compare by value |
| `(quotient n m)` `(remainder n m)` `(modulo n m)` | integer division family; integers only. `remainder` takes the sign of `n`, `modulo` the sign of `m` |
| `(abs n)` `(min n …)` `(max n …)` | |
| `(floor x)` `(ceiling x)` `(round x)` `(truncate x)` | integers pass through; floats stay floats (`(floor 2.5)` → `2.0`). `round` rounds half to even |
| `(exact x)` `(inexact x)` | float→int (truncates toward zero) / int→float |
| `(expt b e)` `(sqrt x)` | `expt` of two integers with `e ≥ 0` is an integer; everything else is a float |
| `(number? x)` `(integer? x)` `(float? x)` | |
| `(zero? n)` `(positive? n)` `(negative? n)` `(even? n)` `(odd? n)` | `even?`/`odd?` need integers |
| `(number->string n)` `(string->number s)` | `string->number` returns `#f` when `s` is not a number |

### Booleans and equivalence

| | |
|---|---|
| `(not x)` | `#t` only for `#f` |
| `(boolean? x)` | |
| `(eq? a b)` `(eqv? a b)` | **identity** for pairs and procedures; **value** for everything else, including strings. `eqv?` is an alias |
| `(equal? a b)` | structural equality |

### Pairs and lists

| | |
|---|---|
| `(cons a d)` `(car p)` `(cdr p)` | |
| `(caar p)` `(cadr p)` `(cdar p)` `(cddr p)` `(caddr p)` `(cdddr p)` | compositions |
| `(list x …)` `(list? x)` `(pair? x)` `(null? x)` | `list?` is true only for proper lists |
| `(length l)` `(append l …)` `(reverse l)` | |
| `(list-ref l i)` `(list-tail l i)` | 0-based; out of range is an error |
| `(iota n)` | `(0 1 … n-1)` |
| `(memq x l)` `(member x l)` | tail starting at the first match (`eq?` / `equal?`), else `#f` |
| `(assq k al)` `(assoc k al)` | first pair whose car matches (`eq?` / `equal?`), else `#f` |
| `(map f l …)` | n-ary; stops at the shortest list |
| `(for-each f l …)` | for effect; yields void |
| `(filter pred l)` | |
| `(fold f init l)` | left fold: `(f acc x)` |
| `(sort l less?)` | stable; `less?` is a Scheme or primitive procedure |
| `(apply f arg … l)` | last argument must be a list |

### Vectors

| | |
|---|---|
| `(vector x …)` `(vector? x)` | build / predicate |
| `(vector-length v)` | |
| `(vector-ref v i)` | 0-based; out of range is an error |
| `(vector-set! v i x)` | mutate element `i`; yields void |
| `(vector->list v)` `(list->vector l)` | convert between vectors and lists |

`equal?` compares vectors elementwise; `eq?`/`eqv?` compare vectors by
identity.

### Strings

| | |
|---|---|
| `(string? x)` `(string-length s)` | length in runes |
| `(string-append s …)` | |
| `(substring s start [end])` | rune indices; `end` defaults to the length; out of range is an error |
| `(string=? a b …)` `(string<? a b …)` | |
| `(string-upcase s)` `(string-downcase s)` `(string-trim s)` | `string-trim` strips Unicode whitespace at both ends |
| `(string-split s sep)` | `sep` is a literal separator, not a regex; empty `sep` is an error |
| `(string-join l sep)` | |
| `(string-contains? s sub)` `(string-prefix? prefix s)` `(string-suffix? suffix s)` | |
| `(string-replace s old new)` | replace every occurrence |
| `(string->symbol s)` `(symbol->string sym)` `(symbol? x)` | |
| `(format fmt arg …)` | `~a` display, `~s` write, `~%` newline, `~~` tilde; too few or too many args is an error |

There is no character type in v0; one-character strings serve that purpose.

### Output

| | |
|---|---|
| `(display x)` | human form: strings without quotes |
| `(write x)` | machine form: strings quoted and escaped, readable back |
| `(newline)` | |
| `(with-output-to-string e …)` | a special form (reserved like the others): capture `display`/`write`/`newline` from `e …` and return the text |

Output goes to the interpreter's output writer, which the host provides
(the REPL uses stdout; the agent loop captures it). All three yield void.

### Control

| | |
|---|---|
| `(procedure? x)` | |
| `(apply f arg … l)` | see lists |
| `(void)` | the void value |
| `(error message irritant …)` | raise; see §4 |

### JSON

| | |
|---|---|
| `(json-parse s)` | JSON text → value |
| `(json-emit v)` | value → compact JSON text |

Mapping, in both directions:

| JSON | Scheme |
|---|---|
| object | association list with **string** keys, in document order: `(("a" . 1) ("b" . 2))` |
| array | list |
| string / number / `true` / `false` | string / integer-or-float / `#t` / `#f` |
| `null` | the symbol `null` |

`json-emit` treats a proper list whose elements are all pairs with string
cars as an object and any other proper list as an array. Consequently `'()`
emits `[]`, and there is no way to emit `{}` in v0.
Procedures, handles and symbols other than `null` cannot be emitted.

This mapping is also how the agent loop turns a model's tool-call arguments
into Scheme values (ADR 3).

### Host primitives (registered by the session, not by `nodwyn repl`)

| | |
|---|---|
| `(fs-read path [start [count]])` `(fs-write path content)` `(fs-edit path old new [replace_all])` `(fs-list path)` | repository-pinned file access; writes and edits are irreversible (confirm, diff preview). `fs-read` returns the whole file (at most 1 MiB; a larger one must be read in ranges), or with `start` (a 1-based line) and `count` (default 200) a header `[lines A-B of N]` and those lines exactly as in the file; a ranged read is streamed, so file size does not limit it, and stops at a whole line within `window.max_result_tokens × 3 − 256` bytes, so the window never cuts it; a single line longer than that comes back cut, under a `[line N of M, cut at B bytes]` header. `fs-edit` replaces exactly one occurrence of `old`, or with `replace_all` (`#t`) every one; when `old` is not found but matches one place with whitespace ignored, the error names those lines and quotes them exactly |
| `(index-grep path pattern [glob [context]])` | **P10.** Search a repository file or directory with a regular expression (RE2 syntax, as in Go and ripgrep; not command-line options). The directory is walked in Go (hidden entries and symbolic links skipped, no ignore files), `glob` narrows the candidates (`*.py`, `tests/**/*.py`, `*.{go,md}`; docs/memory.md has the rules), and each is read through the guarded `fs-read` and matched in Go. Returns matches (path, line, text, and with `context` the lines before and after: 0 to 5, more is capped), files searched and skipped, and a truncated flag, set when the matches reach 100 or the output cap (`MaxOutput`, 256 KiB). More than 3000 candidate files is an error asking for a narrower path or a glob (each read's guard decision is logged, so the limit also bounds the log); the whole query has 30 seconds; protected, hidden, binary and oversized files and symlinks are skipped. Needs no program on `PATH` |
| `(fs-find path glob)` | file names under a repository directory whose path matches `glob`: sorted, relative to the root, at most 500, with their count and a truncated flag. Names only, listed as `index-grep` lists candidates, with the same glob rules; hidden and credential-looking names are left out |
| `(git-status)` `(git-diff [path [staged]])` | only when the root holds `.git`: the working tree's status (branch, changed, staged and untracked files, short format), and the uncommitted changes as a unified diff, the working tree against the index or with `staged` (`#t`) the index against the last commit; `path` narrows it. Read-only, so no approval: git runs with fixed arguments and everything that would run another program switched off (external diff drivers, text conversion, the file system monitor, the pager), and credential-looking files are left out as `fs-read` refuses them. Untracked files are not in a diff |
| `(shell-exec cmd args)` | run a program with an argument **list** in the repository root, scrubbed environment (`TMPDIR` is the scratch directory); irreversible; returns `(("status" . n) ("stdout" . s) ("stderr" . s))` |
| `(shell-start cmd args)` | start a program that keeps running (a server, a watcher, a program to talk to) in the repository root, in the background: no shell, the same scrubbed environment and `TMPDIR` as `shell-exec`, its own process group. Irreversible, and checked like `shell-exec` (its `cmd` and `args` are the command line). Returns text: `p1: running (the command line)` and its first output (as soon as there is some, waiting at most a second). At most 4 run at once; every one is stopped when the session closes |
| `(shell-read id [wait])` | what process `id` printed since the last read (stdout and stderr together), after a first line with its state: `running` or `exited N`. With `wait` (0 to 60 seconds) it first waits for new output or an exit. The latest 1 MiB of output is kept, and a read says how much was dropped before it, or how much is left past the output cap. Read-only, so no approval |
| `(shell-send id text)` | type `text` into the process's input (end a line with `\n`), and return what it printed next (as soon as there is output, waiting at most a second). Irreversible, with the reason that what it does depends on the program: it asks every time; `text` is a payload the credential checks read |
| `(shell-stop id)` | stop the process (SIGTERM to its group, SIGKILL after 2 seconds) and return its last output and exit status. Needs no approval: it ends only what this session started |
| `(scratch-dir)` | the session's scratch directory (a session always has one; `nodwyn repl` and `eval` have no host primitives at all). It is a private directory (mode 0700) under nodwyn's data directory, outside the repository; the file primitives take absolute paths inside it, or paths starting with `$TMPDIR/`, which the interpreter rewrites to it in every path and command argument before the guard; writes there need no approval, and commands get it as `TMPDIR`. It is removed when the session closes, a resumed session gets a new, empty one, and directories a crashed session left are swept once untouched for a week (a session touches its own at every turn). The default system prompt tells the model about it by `$TMPDIR`, not by its path, so the prompt is the same across sessions. |
| `(project-notes)` `(project-notes-save text)` | this repository's notes kept across sessions (how tests run, where things are, runbooks), `""` when there are none; and replacing them. They live in nodwyn's data directory, keyed by the repository's path, never in the repository. A save is capped at 8 KiB and redacted, and its text is a payload the guard checks, so content that looks like a credential needs a person's approval. The default system prompt shows the notes under a heading that calls them leads to check, not instructions |
| `(http-request cred url method position headers body)` | **P3.** Send a request with the credential handle injected at `position` (`header:NAME`, `header:NAME:SCHEME` such as `header:Authorization:Bearer`, `query:NAME`, `body`). Egress: the guard checks the binding table for (class, host, method, position) first. Not offered to the model (a handle cannot come from JSON); reach it through a user-registered tool. Returns `(("status" . n) ("body" . s))`, the body read up to 1 MiB |
| `(policy-read name)` | User-only, hidden from the model catalog. Read configured `bindings` or repository `approvals`, returning content, revision and existence; bounded to 64 KiB and rejects recognized secrets. No arbitrary path. |
| `(policy-write name revision content)` | User-only, hidden from the model catalog. Validate TOML and atomically replace that fixed policy after a one-time diff confirmation and revision check. Binding changes reload before egress; approval-file changes affect new/resumed sessions. |
| `(credential-register class)` | **P3.** Masked input through the front-end, stored in the keychain, returns `#<cred:class:id>`. User origin only: refused to the model and to the loop's strategy; never a model tool |
| `(credential class)` | The handle of a key already in the keychain, or `#f` if there is none (or no keychain is open). An OAuth2 credential (ADR 24; added with `nodwyn credential add --oauth2`) is a handle like any other: its access token is refreshed, when due, just before it is injected. This is how Scheme reaches a keyed service — `(jev-noul (credential "jev-key") …)`, or `http-request` to a judge or a model API — without asking for the key again. Looking a handle up grants nothing: every use is checked against the binding table. Refused to model-originated code |
| `(jev-noul cred state instructions)` | **P5.** Ask the judge a yes/no question about a text; returns the probability of yes (0..1). Egress to the configured Jev host; everything sent (state, instructions, levels, options) is redacted (L1) before it leaves; logged as `jev.req` with the call's origin. Not offered to the model (a handle cannot come from JSON): the model has `judge-noul` |
| `(jev-score cred state instructions levels)` | **P5.** Rate a text on an ordered rubric of 2–10 level strings; returns `(("score" . 0..1) ("raw" . n) ("levels" . k) ("confidence" . c))`. Not offered to the model |
| `(jev-choice cred state instructions options)` | **P5.** Pick one of the options (an alist name → description); returns `(("choice" . name) ("confidence" . c) ("probabilities" . ((name . p) …)))`. Not offered to the model |
| `(judge-noul state instructions)` `(judge-score state instructions levels)` `(judge-choice state instructions options)` | The model's judge tools: the same three calls without the handle. Go uses the stored `jev-key` itself, and the guard judges the call as carrying that class, so the binding for the Jev host decides it as it does for `jev-*`. Registered only when the session starts with a `jev-key` stored and `jev.content = "full"`, because the model chooses the text it sends (ADR 20, amended). Every argument is a payload |

MCP servers you approve in the console (ADR 23) add one primitive per tool
you turned on, named `server__tool` (model APIs refuse `/`; the console shows
`server/tool`). Each is egress to that server's host, with the server's key,
if it has one, bound to it, and a tool you did not declare as reading only confirms every
call. Every argument is a payload: a literal key in it is refused unless its
class is bound for that host (L1), and content from outside may go there
because you approved the server, key or not (L2). Results are tagged
`external:mcp`. They are offered to the model like the tools above.

### Provenance (L2)

Every value carries **provenance tags**: where it came from, not what it
contains. A host primitive tags its result (`fs-read`, `fs-list`, `fs-find`,
`index-grep`, `git-status`, `git-diff` and `policy-read` mint
`external:file`; `shell-exec` and the background-process primitives mint
`external:shell`; `http-request` and the Jev primitives mint `external:http`;
MCP tools mint `external:mcp`; `session-list` and `session-events` mint
`external:log`), a credential
handle carries `cred:<class>`, and the tags of a call's arguments flow into
its result.

| | |
|---|---|
| `(taint-of x)` | the tags as a list of strings, sorted; `()` for a clean value |
| `(tainted? x)` | true if the value carries any tag |
| `(wipe x)` `(box x)` | the same value with its tags removed. **User-originated Scheme only** — the guard denies either from model-originated code and from an agent-turn strategy (§9), which handles every tool result — and every use is logged |

Propagation happens in one place, the primitive dispatch, so there is no
second path. A primitive whose result *measures* its arguments rather than
being made of them carries nothing: `length`, `string-length`,
`vector-length`, every predicate (`null?`, `string?`, `even?`, …), the
comparisons (`=`, `<`, `string=?`, `string-contains?`, …), `not`, and
`taint-of` itself.

What provenance is for: the guard refuses to send content that came from
outside to a destination no binding authorises, and refuses a command line
built from tool output without a confirm. What it is **not**: a boundary
against the user, who may `wipe`, and it does not survive a value being
rebuilt a character at a time through the opaque primitives above. The real
egress lines are the binding table, fail-closed defaults and ingress
redaction (SPEC Risk 1).

### Meta

| | |
|---|---|
| `(primitives)` | list of primitive names, sorted |
| `(primitive-info name)` | alist: `doc`, `params`, `variadic`, `egress`, `irreversible`, `host`, `origin` |
| `(register-tool name proc meta)` | add a primitive backed by a Scheme procedure; see §7 |
| `(load path)` | evaluate a file's forms in the global environment; a relative path resolves against the interpreter's base directory (the repository root in a session). A **host** primitive: it reads the filesystem and is subject to the guard |
| `(handle? x)` | |

## 6. Prelude

`internal/scheme/prelude/*.scm` is embedded in the binary and loaded into the global
environment before anything else. It defines, in Scheme:

| | |
|---|---|
| `(assoc-ref al key [default])` | `cdr` of `(assoc key al)`, or `default` (void if absent) |
| `(identity x)` | |
| `(compose f g)` | `(lambda args (f (apply g args)))` |
| `(string-empty? s)` | |

After the prelude, nodwyn loads `./.nodwyn/init.scm` if present (ADR 14).

## 7. Primitives and the guard

The **primitive table** maps a name to a Go handler plus metadata. It is the
only surface through which Scheme reaches the host (SPEC invariant 4).

Every primitive declares:

| Field | Meaning |
|---|---|
| `doc` | one line, shown to the model as the tool description |
| `params` | ordered list of `(name type doc)`; `type` is one of `string integer number boolean list alist vector any`. A Go primitive may mark trailing parameters optional (ADR 29): a call may stop before any of them, and the handler sees fewer arguments; one skipped before a later one that is given gets its type's empty value. A variadic primitive cannot also have optional parameters |
| `variadic` | extra arguments allowed after the declared ones |
| `egress` | may send bytes off-machine |
| `irreversible` | cannot be undone by the harness |
| `host` | touches the OS (filesystem, processes, network) |
| `origin` | `builtin` or `scheme` |
| `hidden` | never offered to the model as a tool (`load`, `register-check`, the loop's primitives) |
| `audited` | its guard decision is logged even when it is an allow of a pure builtin |
| `preview` | Go-only, for irreversible host primitives: renders what the call would do (a diff) for the confirm prompt |

Parameter **roles** (`path`, `dest`, `payload`, `credential`) tell the guard
what an argument *is*; they are declared by host primitives from B2 on.

**Application of a primitive** — whether from the model, the prelude, a
`.scm` file or the REPL — goes through exactly one Go function, which:

1. checks the argument count against `params`/`variadic`;
2. asks the **guard** (when one is installed) for a decision; a denial raises
   `denied: <reason>`;
3. calls the handler.

`register-tool` adds a primitive whose handler applies a Scheme procedure:

```scheme
(register-tool "go-test"
  (lambda (pkg) (shell-exec "go" (list "test" "-race" pkg)))
  '((doc . "Run the Go tests for a package pattern")
    (params . (("pkg" "string" "package pattern, e.g. ./internal/log")))
    (irreversible . #f)))
```

Rules: the name must be new (builtins cannot be shadowed, and a tool cannot
be re-registered — the tool list must be stable for the whole session,
ADR 9); `doc` and `params` are required; `egress`, `irreversible` and
`variadic` default to `#f`. A registered tool is indistinguishable from a
builtin at the dispatch site, and its own primitive calls are guarded in
turn.

`register-check` (ADR 20) adds a guard checker written in Scheme:

```scheme
(register-check "no-external-llms" "confirm"
  (lambda (prim-name args)
    (or (equal? prim-name "shell-exec")
        (equal? prim-name "http-request"))))
```

- `name` is the checker name that appears in `guard.decision` events.
- `verdict` is fixed to `"deny"` or `"confirm"` at registration time.
  It can never be `"allow"` — the guard merges decisions
  strictest-wins, so a user checker can only tighten, never relax.
- `predicate` is a procedure `(prim-name args)`. When it returns a
  true value (anything but `#f`, as everywhere in Scheme) the checker
  returns its fixed verdict; otherwise it has no opinion (allow). A predicate that raises an error fails closed
  as a denial.

The builtin is **hidden** (never offered to the model as a tool) and
callable only from user-originated Scheme — the model cannot add or
bypass checkers at runtime. Checkers registered this way run after all
built-in Go checkers in the pipeline.

Loading a `.scm` file can grow the primitive table and the global
environment. It **cannot** add special forms or change evaluation: `eval`
and `apply` are two fixed Go functions, and the kernel exposes a fingerprint
that the test suite checks before and after loading arbitrary code.

## 8. Embedding API (Go)

```go
in := scheme.New(scheme.WithOutput(w))       // prelude loaded
v, err := in.EvalString(ctx, "(+ 1 2)")      // last value of the source
v, err = in.Apply(ctx, proc, args)           // call a procedure
err = in.Register(&scheme.Prim{...})         // add a host primitive
in.SetGate(g)                                // install the guard (B2)
fp := in.Fingerprint()                       // kernel fingerprint
```

`ctx` cancellation is checked at every application; a cancelled evaluation
returns the context's error.

## 9. The agent loop

A turn is a Scheme procedure. The session records the user's message and
calls `(nodwyn-turn request)` with the recorded (redacted) text, which runs
the plan gate when the turn must plan and then `(agent-turn request)` (§9.5).
The defaults are installed before `.nodwyn/init.scm` loads, so a project can
redefine either. Stripped of `/optimize`, the plan gate, `ask-model`'s retry
of cut replies and the evidence loop for unattended turns (all below, and in
`internal/loop/strategy.scm`), the default `agent-turn` is this loop:

```scheme
(define (agent-turn request)
  (let loop ((reply (model-turn)))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (unless (null? calls)
        (run-calls calls)
        (maybe-compact request reply)
        (loop (model-turn))))))
```

| | |
|---|---|
| `(model-turn)` `(model-turn name)` `(model-turn name options)` | Ask a model once: the control model (`"control"`), or the configured model `[models.NAME]` (manual, "Choosing models"). `options` is an alist; `'((tools . #f))` offers the model no tools, as the planner's request does, and `'((tools "fs-read" "fs-list"))` only the named ones (an unknown name is an error that lists the tools), as a sub-agent's requests do. Both are recorded (`llm.req` `no_tools`, `offered_tools`) so replay rebuilds the request. Any other value but `#t` is an error: `'((tools #f))`, a list holding #f, is not a silent yes. `'((headroom . #t))` lets the request run past the window's high water, up to its ceiling (`(window-limits)`), before older context is trimmed, as compaction's note request does; `llm.req` records that trim line as `high_water`. Returns an alist: `content`, `thinking`, `done` (why it stopped), `tool-calls`, each call an alist of `id`, `name` and `args` (the model's JSON text), and `prompt-tokens` and `output-tokens`, the server's counts for this request (0 when it gives none). The window is built from the log, so the model sees what the strategy's earlier calls recorded; each request is recorded, with the model it went to, for `nodwyn replay --window`. |
| `(run-tools calls)` | Run calls in order, each through the guard. Returns an alist per call: `id`, `name`, `ok`, `value` (the text the model reads, carrying the tool's provenance), `error`, `denied`. |
| `(skip-tools calls reason)` | Answer calls without running them; the model reads `Not executed: reason`. |
| `(add-message text)` | Add text to the conversation, logged as `harness.msg`. The model reads it as a user message at the next `model-turn`; the current task stays the user's. Refused while calls from the latest `model-turn` are unanswered, since a call must be followed by its result. |
| `(fold-result result text)` | Show a tool result as `text` in later windows, such as an outline or a line saying what it was, once its details stopped mattering; `result` is the `tool.result` event's seq or the id of the call it answers. Recorded as `result.fold`, so replay rebuilds the same window (recipe window-v9); the result itself stays in the log for audit and `recall`. Folding an old result changes the prompt from that point on, so the server re-reads what follows it: fold in batches, or results near the end |
| `(narrow-context system [agent [options]])` | Start a fresh context, like Emacs's narrowing: until `fold-context`, `widen` or the user's next message, models see `system` as their system prompt and only what comes after this point. Follow it with `add-message` to hand over the task. This is how a model gets a task and a plan without the conversation that led to them. Contexts nest. Calls from the latest `model-turn` that are still unanswered wait for this context to fold; its own `model-turn`s start with none pending. `agent` labels the context in the log. `options` is an alist; `'((lasting . #t))` makes the narrowing outlast the turn, as compaction's does (§9.2): if it is still open at the user's next message, the conversation starts there from then on, under the session's system prompt with the new task and plan, and what came before stays in the log only. Recorded as `context.narrow` `lasting` (recipe window-v11). Any other value but `#t` or `#f` is an error. |
| `(fold-context)` | End the innermost narrowed context and fold it away: later windows skip everything since its `narrow-context` and see the enclosing context again; the log keeps it all. The calls that waited for it are the latest `model-turn`'s again. Refused while calls are unanswered. |
| `(widen)` | End every narrowed context. Models see the whole conversation again, including what happened while it was narrowed, from where the latest lasting narrowing of an earlier turn started it. A new user message widens too, except that a lasting narrowing still open becomes where the conversation starts. Refused while a context holds calls waiting for an answer: fold it. |
| `(answer-call call text)` | Answer one of the model's calls with `text`, as a successful result, without running the tool: how a tool the strategy handles itself answers. Counts against the tool-call budget. |
| `(session-system-prompt)` | The system prompt the session started with (§9.1), fixed for its life: what a narrowed context passes to `narrow-context` to keep the model's standing instructions. |
| `(session-requests)` | The user's messages in this session so far, oldest first, as the log recorded them (redacted); the running turn's request is the last. Read from the log, so it holds across a resume: how `/compact` finds the request it hands over (§9.2). |
| `(model-info)` `(model-info name)` | What is known about a model before asking it: the control model, or the configured model `[models.NAME]`. An alist: `name`, `model` (the provider's id), `provider`, `num-ctx` (its context length; 0 when unset), `max-output` (the reply cap its requests carry: its `max_output`, else `window.output_reserve`), and `temperature` and `seed` (its sampling settings, `#f` when the server decides). An unknown name is an error that lists the configured ones. |
| `(model-names)` | The names `model-turn` and `model-info` accept: `"control"` and each configured `[models.NAME]`, sorted. |
| `(window-limits)` | The window's sizes, in tokens as the window estimates them: an alist of `high-water` (past it older context is trimmed), `low-water` (what trimming goes down to), `ceiling` (the most a request with headroom may carry: the control model's context less the reply cap, or the high water when nothing is known above it), `max-result-tokens` (a longer tool result is shortened) and `output-reserve` (the default reply cap). The water marks grow with a large context unless config.toml sets them (manual, "Choosing models"). Compaction's default threshold is read from it (§9.2). |
| `(context-tokens)` | The size of the context the next `model-turn` would send, in the units of `(window-limits)`, before any trimming. Unlike a reply's `prompt-tokens` it counts as the window does, whatever the server's tokenizer, and includes the tool results added since the last request. Compaction measures it (§9.2). |

When nobody reviews the work as it happens — `(unattended?)` (`--yolo`, a
run's `auto_approve`) or a supervised run's turn (`plan-required?`) — the
default `agent-turn` ends a turn on evidence, not on the model's word
(`evidence-loop`): a reply that stops before anything changed, or with a
change and no test run after it, gets `finish-no-change-message` or
`finish-untested-message`, at most `finish-reminders` (2) times. A command is
a test run when its command line contains one of `test-command-words`
(pytest, go test, cargo test, npm test, make test, …; a project may redefine
the list). After `unrunnable-after` (6) test commands in a row fail to run,
the model gets `(finish-unrunnable-message n)` once: finish and report the
change as unverified rather than spend the budget on a broken environment.

| | |
|---|---|
| `(unattended?)` | true when nobody approves this session's calls: it answers ordinary confirms itself (`--yolo`, a run's `auto_approve`), the judge answers them (`--judge`), or nobody is there at all (`nodwyn run --unattended`) |

A reply's `done` is `"length"` when the model reached the output cap (the
model's `max_output`, else `window.output_reserve`); the core notes it in the log. What to do is the
strategy's: `(ask-model …)` is `model-turn`, asked again with
`output-cut-message` when the reply was cut with no tool call to answer, at
most `output-cut-retries` (2) times. The default loop, the planner and
`sub-agent` use it.

A strategy talks to the model through tools: a tool's description is an
instruction, its result and a skip reason are feedback. `add-message` is for
when there is no call to answer, such as a model that stopped with work left.

Routing work to another model is `narrow-context`, `add-message` and
`(model-turn name)` in a loop: the other model gets a context the strategy
built, and its tool calls run like any model's. Narrowing and routing are
events in the log, so replay rebuilds every request whichever model it went
to.

These primitives are hidden from the model. The ones that act on the
conversation (`model-turn`, `run-tools`, `skip-tools`, `answer-call`,
`add-message`, `log-note`, `fold-result`, `record-plan`, `plan-steps`,
`record-step`, `narrow-context`, `fold-context`, `widen`) are refused outside
a turn and from model-originated code; the ones that only describe the session (`model-info`, `model-names`,
`window-limits`, `session-system-prompt`, `unattended?`, `plan-required?`,
`plan-recorded?`, `session-list`, `session-events`) answer at any time, which
is how `compact.scm` reads `(window-limits)` as it loads. The strategy runs with harness origin, so it cannot
strip provenance or change policy (`wipe`, `register-check` and `policy-write`
are user-only); the tools it runs execute with model origin, as if the model
had called them directly. Whatever the strategy does:

- it runs or skips only calls the model made, each once, with the arguments
  the model sent;
- every call gets exactly one result: calls left unanswered are skipped when
  the strategy asks the model again or when the turn ends;
- budgets, the tool-call limit and the progress guard (the same call four
  times in a row, a denied call retried three times, ten failures in a row)
  end the turn with an error no `guard` clause can catch (§4), and so does a
  failure to write the log, which is the source of truth.

### 9.1 Prompts

Every prompt nodwyn sends is Scheme. The defaults live in
`internal/loop/prompts.scm`, which loads with the default strategy, before
`.nodwyn/init.scm`; a project redefines any of them there, usually with raw
strings (§1.1):

| | |
|---|---|
| `(system-prompt repo)` | the control model's system prompt: `(unattended-system-prompt repo)` when `(unattended?)`, else `(interactive-system-prompt repo)`. Unattended, it is how to work — understand the request, set up a check that runs, make the whole change, validate by running it, report honestly — instead of announcing each call to an approver |
| `plan-explore-system`, `(plan-explore-task request)`, `(plan-draft-with-findings request findings)` | the explorer that looks before a plan is drafted (§9.5), and the draft request that carries what it found |
| `finish-no-change-message`, `finish-untested-message`, `(finish-unrunnable-message n)` | what the evidence loop tells the model (§9) |
| `task-header` `plan-header` | the headers under which the window adds the user's latest request and its plan to the system prompt, where they are never trimmed away |
| `(run-turn-prompt task complete-line turn max-turns)` | the message a supervised run sends for each turn; `complete-line` is the marker the run looks for |
| `(plan-system-prompt repo)` | the planner's system prompt |
| `(plan-draft-prompt request)` | the planner's request for a plan |
| `(plan-revise-prompt request plan score)` | the request for a better plan after the judge scored it `score` (0..1) |
| `plan-needed-question` | the judge's yes/no question: does this request need a plan? |
| `compact-summary-prompt`, `(compact-handover-prompt request note)` | compaction's request for a handover note and the start of the compacted context (§9.2) |
| `(optimizer-system-prompt)`, `(optimizer-task history focus)` | the optimizer's system prompt and task (§9.4) |
| `plan-score-rubric` | the judge's instruction for scoring a plan |
| `plan-score-levels` | the rubric's 2–10 levels, worst first |
| `working-rules` | how to work, added to both system prompts (§9.6): shell command lines, the project's environment, reading with the file tools, a plan as a first guess, the check loop, following the request's conclusion, widening in general form, whole test modules, comparing failures with the unchanged code, finding regressions file by file |

```scheme
(define base-system-prompt system-prompt)      ; keep the default to extend it
(define (system-prompt repo)
  (string-append (base-system-prompt repo) "\n"
    """
    Work unattended: nobody approves your calls.
    Run the tests of every module you change before you finish.
    """))
```

nodwyn reads the system prompt and the two headers once, after `init.scm` has
run: they are then fixed for the session like the tool list (ADR 9), and
recorded with the window's recipe so a replay rebuilds requests that used a
project's own. A system prompt that is not a procedure returning a
non-empty string, or a header that is not a non-empty string, stops the
session from starting. The others are ordinary Scheme, read when the plan gate (§9.5)
uses them, so a `(reload)` changes them from the next turn.

### 9.2 Compaction

A long turn fills the context with tool results the model no longer needs.
nodwyn elides the oldest ones past `[window] high_water` on its own; compaction
goes further, as user code over the primitives above, in
`internal/loop/compact.scm`. When the context passes `compact-threshold`,
measured as the window measures it (`(context-tokens)`, not a reply's
`prompt-tokens`, which a server counts its own way and which lacks the latest
tool results), `maybe-compact` asks the model for a handover note (the
task and its example as given, what it found with the evidence for each
finding, what it changed, how to test, what is unsure or left, and its check
script), then narrows the
context to the session's system prompt, the request and that note. The
narrowing lasts (`narrow-context` with `'((lasting . #t))`, labelled
`compaction`): the rest of the turn goes on from the note, and so do later
turns, each under the session's system prompt with its own request, rather
than from the whole conversation again. The
repository keeps the model's changes, and the log keeps the whole
conversation, as `context.narrow` with `lasting` and the note before it, so
replay and audit see everything the note replaced. The note request may use
the room between the window's high water and its ceiling, so a round that
jumps past the high water is still compacted rather than trimmed; a context
no bigger than the window has no such room.

The user compacts whenever they like, whatever the context's size, with a
message; it works in every client, like `/optimize` (§9.4):

```
/compact                     compact now
/compact the failing tests   any words after it are what the note must keep
```

The default `agent-turn` runs `compact-turn` for it instead of the loop. It
hands over the user's latest request before it, not counting other
`/compact`s (`session-requests`), so a note written between turns keeps the
task that was being worked on; the words after `/compact` are added to the
note request with `compact-focus-prompt`. The turn ends once the note is
handed over: the model is not asked to go on, so it does not start on what
the note says is left, and the user's next message goes on from the note.
A session with no request before the `/compact` has nothing to compact, and
the model is not asked. Each outcome is a `note` in the log, which the
clients show.

A strategy asks for compaction at the next round, whatever the size, with
`(compact-next-round [focus])`: `maybe-compact` runs it where the loop next
calls it, in this turn or the next, and the request is used once.

| | |
|---|---|
| `compact-threshold` | context tokens, as `(context-tokens)` counts them, past which the context is compacted; `#f` never compacts on its own (`/compact` and `compact-next-round` still do). The default is just under the window's high water, a twenty-fifth below it (`(window-limits)`: 58983 at the default 61440), so only work that really runs long compacts (ADR 32) |
| `compact-attempts` | replies to the note request that may be tool calls (they are skipped) before compaction gives up; after giving up it waits for the context to grow another quarter of the threshold |
| `compact-summary-prompt` | the request for the note (a prompt, §9.1) |
| `(compact-focus-prompt focus)` | what the note must keep, as the user put it after `/compact` or a strategy gave `compact-next-round`, added after `compact-summary-prompt` |
| `(compact-handover-prompt request note)` | what the compacted context starts with: the request, and the note with a request to re-run its reproduction and re-check its findings before building on them |
| `(maybe-compact request reply)` | compact if a strategy asked for it with `compact-next-round`, or if the context the next `model-turn` would send passed the threshold; call it once the reply's calls are answered (`reply` itself is not read) |
| `(compact-context request [focus])` | compact now, handing over `request`; `#t` if the turn continues in the narrowed context |
| `(compact-next-round [focus])` | compact at the next `maybe-compact`, whatever the size; `compact-requested` holds the request (`#f` when there is none) |
| `(compact-request? request)` `(compact-turn request)` | whether a message is a `/compact`, and what the default `agent-turn` does with one |

```scheme
(define compact-threshold 40000)    ; in .nodwyn/init.scm
```

A strategy that redefines `agent-turn` calls `maybe-compact` itself, where the
default does: after running the calls, before the next `model-turn`; it keeps
`/compact` by calling `(compact-turn request)` when `(compact-request?
request)`. Compacting
rewrites the start of the context, so the server re-reads the new, short
prompt once instead of reusing its cache (ADR 9); a late threshold compacts
rarely and keeps the prefix stable in between. A note that the model will
not write (it keeps calling tools, or writes nothing) leaves the context as
it was, and the model is told to go on.

### 9.3 Sub-agents

A sub-agent is a model loop in a context of its own: it gets a system prompt
and a task, works with the tools it is given, and answers; the context then
folds away, so whoever asked sees only the answer (ADR 27). They are user
code, in `internal/loop/agents.scm`:

| | |
|---|---|
| `(sub-agent system task tools [name])` | run `task` in a narrowed context under `system`; `tools` lists the tool names it may use (its model is offered only those, and other calls are skipped; `'()` offers none), is a procedure that takes a call and says whether it may run (its model is then offered every tool), or is `#f` for all; `name` labels it in the log (default: the agent tool being answered, else `sub-agent`); returns its final answer, after `fold-context` |
| `sub-agent-max-rounds` | replies with tool calls a sub-agent may make before it must answer (40); its last calls are then skipped and it is asked for its answer with no tools (`sub-agent-wrap-up-message`), so its work is not lost |
| `sub-agent-repeat-limit` | identical calls (same tool and arguments) a sub-agent may run in a row (2). The core's progress stop, at four in a row, ends the whole turn and cannot be caught, so a sub-agent stops short of it: the next identical call is skipped with `sub-agent-repeat-reason`, and if it repeats again it is asked for its answer with no tools, which it returns with `sub-agent-stopped-note`. Its caller's work goes on |
| `(define-agent-tool name doc params handler)` | register a tool the model calls like any other, answered by `(handler call args)`, `args` parsed from the model's JSON; define it in `.nodwyn/init.scm` |
| `(run-calls calls)` | run a reply's calls: agent tools through their handlers and `answer-call`, the rest with `run-tools`; returns one result alist per call. The default `agent-turn` uses it |

```scheme
;; .nodwyn/init.scm: a reviewer the model can consult, reading but not writing
(define-agent-tool "ask-reviewer"
  "Ask a reviewer who can read the repository whether a change is finished."
  '(("change" "string" "what you changed and why, and how you tested it"))
  (lambda (call args)
    (sub-agent "You are a strict code reviewer. Read what you need, then reply VERDICT: done, or what is missing."
               (assoc-ref args "change")
               '("fs-read" "fs-list" "index-grep"))))
```

Every sub-agent leaves its whole transcript in the session log: its
`context.narrow` names the agent and lists the model's calls it answers,
then come its task, requests, replies, tool calls and guard decisions, up to
its `context.fold`; the calls it answered follow as `tool.result`. Folding
hides the span from later windows, never from the log, so an agent's work
can be read, replayed (`nodwyn replay --window`) and learned from on its own.

A strategy can also run one itself, such as a review when the model stops:
`(sub-agent reviewer-system (string-append issue diff tests) '())`. A
sub-agent always asks the control model (whichever model its caller was
routed to); a strategy that wants another writes its own loop with
`(model-turn name)`. Each fold costs the server one re-read of the
caller's context: the prefix it had cached was the sub-agent's (ADR 9).

### 9.4 History, reload and the optimizer

A strategy can read this repository's past sessions and change how nodwyn
works here, while the session runs:

| | |
|---|---|
| `(session-list [n])` | this repository's sessions, newest first (all, or the latest `n`); each an alist with `id`, `first-message`, `last-event`, `current`, `in-flight` and `truncated`. Other repositories' sessions are not listed |
| `(session-events id [kinds])` | one of those sessions as its log events, optionally only the kinds listed (`'("user.msg" "tool.result")`); each an alist with `seq`, `ts`, `kind` and `payload`, the event's JSON redacted and parsed as `json-parse` does. The values carry the `external:log` taint, like a tool's output |
| `(reload)` | run `.nodwyn/init.scm` again, with the caller's origin. The strategy, compaction settings, agent-tool handlers and anything else read when used apply from the next turn; a tool keeps its schema and takes a changed body. What a session is fixed with (ADR 9) does not change: new or changed tools, guard checks (`register-check`) and prompts are returned as notes and apply from the next session or a resume |

The optimizer (`internal/loop/optimize.scm`) is user code over these. The
user starts it with a message; the default `agent-turn` runs it instead of
the loop:

```
/optimize                 every earlier session of this repository
/optimize 5               the latest five
/optimize ID ID ...       the sessions named (ids from session-list)
/optimize 5 test runs     any other words are a focus
```

It digests the sessions (requests, answers, the calls made, failures and
refusals, judges' verdicts, sub-agents, notes) within
`optimize-session-chars` per session and `optimize-total-chars` in all, and
hands them to a sub-agent labelled `optimizer`. That agent may read the
repository and write only under `.nodwyn/`, a protected path, so every write
asks the user and shows the diff; an unattended run (`--yolo`) refuses those
writes, so `/optimize` needs a person at the prompt. When it is done `init.scm` is reloaded (and
restored if the reload fails), and the model tells the user what changed.
Its prompts are `(optimizer-system-prompt)` and `(optimizer-task history
focus)`, in `prompts.scm` with the others. A strategy that redefines `agent-turn` keeps `/optimize` by
calling `(optimize-turn request)` when `(optimize-request? request)`.

### 9.5 The plan gate

The plan gate is user code too, `internal/loop/plan.scm`. The host runs
`nodwyn-turn` for every user message, not `agent-turn` directly:

```scheme
(define (nodwyn-turn request)
  (when (plan-required?) (plan-gate request))   ; a supervised run's turns
  (agent-turn request))
```

and the default `agent-turn` calls `(plan-gate request)` before its loop, so
an interactive turn is gated too. `plan-gate` does nothing once the turn has
a plan, so a strategy may call it as well; a strategy that redefines
`agent-turn` still plans in a run. A project that plans its own way
redefines `nodwyn-turn` (a name specific to nodwyn, so a project's own `turn`, a counter say, cannot shadow it). With a judge
(Jev, §5: its primitives registered and a `jev-key` stored), it asks
`plan-needed-question`; a request that needs a plan gets one drafted by
the control model in a narrowed context of its own (§9), offered no
tools and folded away afterwards, then scored against the request and revised until it scores
`plan-accept-score` or `plan-max-revisions` run out. The outcome is
recorded as `gate.plan`, and the window shows the plan to the model for
the rest of the turn. Without a judge there is no gate. If the judge's
yes/no question fails, the model plans anyway, unscored ("degraded"); a
judge the guard refuses (an unbound key) ends the turn, as any denial does.

| | |
|---|---|
| `plan-needed-threshold` | the judge's probability of "needs a plan" at which a request gets one (0.5) |
| `plan-accept-score` | the normalised score at which a plan is accepted (0.7) |
| `plan-max-revisions` | revisions after the first draft (3) |
| `plan-empty-retries` | more times the planner is asked when it answers with no plan, in a fresh context each time and noted in the log (2); an empty revision keeps the best plan |
| `plan-explore-rounds` | before the first draft, a read-only sub-agent labelled `explorer` (fs-read, fs-list, index-grep, fs-find) looks at the repository for up to this many tool rounds and the planner is told what it found, so the plan is not written blind (12); 0 skips it. Its reads come before the plan is recorded, which the core allows: none is irreversible |
| `(record-plan outcome)` | record the gate's outcome as `gate.plan`, from an alist: `decision` (`plan`, `proceed`, `degraded` or `required`), `plan`, `score`, `revisions`, `reason` |
| `(plan-required?)` | true when this turn must plan before it acts, as a supervised run's turns must |
| `(plan-recorded?)` | true once this turn has recorded a non-empty plan |
| `(plan-steps)` | the steps of the plan recorded in this turn, as strings: its numbered or bulleted lines, in order, or the whole plan when it is prose (one step); `'()` before a plan is recorded. The console reads a plan's steps the same way |
| `(record-step n state note)` | record that step `n` (from 1) of this turn's plan is `"done"` or `"skipped"`, with a note saying what shows it, as `plan.step`; a later mark of the same step replaces it. Returns `((done . k) (of . n) (next . m))`: the steps marked so far, the plan's steps, and the first unmarked one, or `#f`. An error without a plan, for a step the plan does not have, or for another state |
| `plan-step` | the model's tool for those marks, an agent tool (`define-agent-tool`) answered by `plan-step-answer`: `{"step", "state", "note"}`, answered with what was marked and the next unmarked step. The plan header (`plan-header`) and a run's turn prompt ask the model to use it. A mark is the model's word, not a proof: the console shows it as such, and the evidence is the transcript |
| `(repository)` | the repository's root, for a prompt to name (the planner's system prompt does) |
| `(workspace)` | the workspace's kind and mounted folders from `.nodwyn/workspace.toml` (ADR 22), as `(("kind" . "docs") ("mounts" (("path" . P) ("access" . "read")) …))`. The kind is a label: every kind has the same tools. Both default system prompts add a section naming the mounted folders, so a plain repository's prompt is unchanged; mounted folders are reached by absolute path, and a read-only one refuses every write. Before `fs-write` or `fs-edit` replaces a file that no git work tree at the root covers, its previous content is kept in `.nodwyn/versions/` and a `file.version` record names it |
| `(log-note text)` | add a note to the session log, for the record and the transcript; the model never sees it |

A turn that must plan (`plan-required?`) always drafts one, scored if there
is a judge, and fails if the plan is empty. The core keeps one invariant
whatever the Scheme does: until a non-empty plan is recorded, nothing
irreversible runs, and a turn that records none fails. What it enforces is
that a plan was recorded, not who wrote it: the default `nodwyn-turn` has the
control model plan, and a project's own `nodwyn-turn` may record a plan of its own
with `record-plan`, as trusted project code.

**Judged approval** (`internal/loop/approve.scm`). When the judge answers a
session's confirms (`nodwyn run --judge`), the core calls
`(approve-call call)` for every ordinary confirm, with `call` an alist of
`task`, `plan`, `prim`, `reasons`, `path`, `argv`, `args`, `detail` and
`content` (the `jev.content` level), and allows the call only if the result's
`allow` is true. Security confirms never reach it, and an error is a no.

| | |
|---|---|
| `approve-threshold` | the judge's probability of "allow" at which a call runs (0.8) |
| `approve-question` | what the judge is asked about the call |
| `approve-options` | the choice it makes: `allow` or `deny`, each described |
| `(approve-state call)` | the text the judge is shown: task, plan, the call and its target; at `full`, what it would do |
| `(approve-call call)` | asks `jev-choice` and returns `((allow . #t/#f) (score . p) (reason . text))`; no judge, or no answer, is a no |

The model marks the plan's steps as it finishes them (`plan-step`), and the
console's plan panel shows which are done, which were skipped and which is
next. A strategy that does not use `run-calls` (where agent tools are
answered) leaves `plan-step` unanswered; its own handler may call
`record-step`.

### 9.6 Working habits

Beside the prompts' `working-rules`, the default loop carries the habits that
held up on a coding benchmark (ADR 32), as user code in `internal/loop/`,
loaded after the files above in this order. Each wraps `run-calls` (and
`fold-result`, `sub-agent` or `nodwyn-turn` where it says), so a project's
`init.scm` can wrap them again or redefine them away.

- `shell.scm`: a command line sent through `shell-exec`'s `cmd`/`args` (a
  program with its arguments in `cmd`, shell operators such as `|` or `&&`
  in `args`) is answered, unrun, with the `bash -c` call that runs it as
  meant; shell results read as plain text, the exit status then the output,
  long output keeping its start and its end (`shell-head-chars`,
  `shell-tail-chars`); and `(ran-ok? result)` counts a test run with failures
  as a run, so the evidence loop's way out is offered only for commands that
  never got that far.
- `hygiene.scm`: in the main context (not in sub-agents), a test command
  that never ran a test is folded to one line once a later one does; a read
  of a file edited since is folded to its outline after `hygiene-stale-age`
  rounds; a read `hygiene-age` rounds old is folded to its outline. Reads
  fold in batches of at least `hygiene-min-bytes`, so the server re-reads
  the prompt rarely.
- `readdef.scm`: the tool `read-definition` (path, name) reads a function,
  method or class by name, its whole definition and line range, or the start
  and members of a long class; a shell script that only reads a file to print
  it gets a hint naming the file tools, `readdef-hints` times a session.
- `envhint.scm`: a command that fails for a missing module gets a hint to
  find the project's own environment, `envhint-hints` times a session.
- `checkloop.scm`: the tool `second-look` (check, saw): when the model's check
  script does not show the problem a request reports, one read-only helper
  reads the request alone and another compares that reading with the check,
  listing what may have been read differently. A history search (`git log`,
  `git blame`) before any code edit gets a hint to use it,
  `second-look-hints` times a session.

## 10. Not in nodwyn Scheme

Deliberately absent from v0.

- characters and the full string-port API (only `with-output-to-string`)
- source positions in runtime errors (reader errors carry positions)
- `define-macro` — the prelude does not need it (ADR 7)
- `set-car!` / `set-cdr!` and string mutation
- nested quasiquote
- hygienic macros (`define-syntax`, `syntax-rules`)
- `call/cc`, `dynamic-wind`
- an `eval` primitive — the kernel surface is not exposed to programs
- exact rationals and bignums
