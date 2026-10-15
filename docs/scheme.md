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

A token is parsed as an integer if `strconv.ParseInt` accepts it in base 10,
else as a float if `strconv.ParseFloat` accepts it and it is made only of
digits, signs, a point and an exponent, else it is a symbol: `inf`, `nan`
and `1_000` are symbols, and an integer too large for 64 bits reads as a
float.

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
| `(eq? a b)` `(eqv? a b)` | **identity** for pairs, vectors, procedures and handles; **value** for everything else, including strings. `eqv?` is an alias |
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
Procedures, handles, vectors, void, symbols other than `null`, and a NaN or
infinite float cannot be emitted.

This mapping is also how the agent loop turns a model's tool-call arguments
into Scheme values (ADR 3).

### Host primitives (registered by the session, not by `nodwyn repl`)

| | |
|---|---|
| `(fs-read path [start [count]])` `(fs-write path content)` `(fs-edit path old new [replace_all])` `(fs-edit path "" "" #f edits)` `(fs-list path)` | repository-pinned file access; writes and edits are irreversible (confirm, diff preview). `fs-read` returns the whole file (at most 1 MiB; a larger one must be read in ranges), or with `start` (a 1-based line) and `count` (default 200) a header `[lines A-B of N]` and those lines exactly as in the file; a ranged read is streamed, so file size does not limit it, and stops at a whole line within `window.max_result_tokens × 3 − 256` bytes (the cap as the window is sized now, which grows with the model's context), so the window never cuts it; a single line longer than that comes back cut, under a `[line N of M, cut at B bytes]` header. `fs-edit` replaces exactly one occurrence of `old`, or with `replace_all` (`#t`) every one; when `old` is not found but matches one place with whitespace ignored, the error names those lines and quotes them exactly; a file that holds credential-shaped text is matched as the model reads it, each credential a `[REDACTED <class>]` placeholder (ADR 43): the placeholder in `old` matches whatever the file holds there, and in `new`, with the span's other placeholders in the same order, keeps it (leave them out of `new` to drop it; any other placeholders in `new` are an error, so a credential is never copied or moved); a match that starts or ends inside a placeholder is refused, an `old` that equals the file's own text still matches, quoted lines are as the model reads them, and an `old` with credential-shaped text is told to write the placeholder instead. Several changes to one file go in one call (ADR 38): `edits` is a list of objects, alists with string keys as `json-parse` makes them, such as `'((("old" . "a") ("new" . "b")) (("old" . "c") ("new" . "d") ("replace_all" . #t)))`, given instead of `old` and `new`; a model's call names only `path` and `edits`, which reaches Scheme as the second form, `old` and `new` empty and `replace_all` `#f`. The edits apply in order, each to the text the one before left; nothing is written unless all apply, and a failure names the edit (`edit 2 of 3 failed, so nothing was written: …`). The call asks once, with one diff from the file to the text all of them leave, keeps one previous version and writes once. Giving both forms, or neither, is an error that says so. A written edit answers with the lines it changed as the file now reads them, numbered, with two lines around each (`edited PATH, now N lines (+D); the changed lines now read:` then `LINE<tab>text` rows; a changed run longer than 40 lines shows its ends, and one answer shows at most 120 lines) |
| `(index-grep path pattern [glob [context]])` | **P10.** Search a repository file or directory with a regular expression (RE2 syntax, as in Go and ripgrep; not command-line options). The directory is walked in Go (hidden entries and symbolic links skipped, no ignore files), `glob` narrows the candidates (`*.py`, `tests/**/*.py`, `*.{go,md}`; docs/memory.md has the rules), and each is read through the guarded `fs-read` and matched in Go. Returns matches (path, line, text, and with `context` the lines before and after: 0 to 5, more is capped), files searched and skipped, and a truncated flag, set when the matches reach 100 or the output cap (`MaxOutput`, 256 KiB). More than 3000 candidate files is an error asking for a narrower path or a glob (each read's guard decision is logged, so the limit also bounds the log); the whole query has 30 seconds; protected, hidden, binary and oversized files and symlinks are skipped. Needs no program on `PATH` |
| `(fs-find path glob)` | file names under a repository directory whose path matches `glob`: sorted, relative to the root, at most 500, with their count and a truncated flag. Names only, listed as `index-grep` lists candidates, with the same glob rules; hidden and credential-looking names are left out |
| `(index-search query scope [limit])` | **ADR 50.** Search by words, best match first. `scope` is `"conversation"`, this session's conversation (the repository and earlier tasks are not built yet, and any other scope is an error that says so): the events a model could have seen — the person's and nodwyn's messages, attached files, plans, summaries, replies with their tool calls, and tool results with the call each answers, in sub-agents' contexts too — read from the log, including what the window has since elided, folded or cut. Not guard, egress, request or note records, not thinking, and not earlier searches and recalls, whose results repeat other events. Words are split as code names them (`ScrubEnv` gives `scrubenv`, `scrub` and `env`; `index-grep` gives `index-grep`, `index` and `grep`; `internal/retrieval`), and events are ranked by BM25: more of the query's words, and rarer ones, rank higher, and a result's call counts three times its output. Returns the best `limit` events (8 unless given, at most 20), each as `seq N` with what it is (`the result of shell-exec {…}, which failed, in the explorer's context`) and its three lines that hold the most of the words matched, numbered as `recall` numbers them, each cut at 200 characters; no match is a sentence saying so, not an error. The index is built in memory from the whole log at the first search or recall (a resumed session holds only the end of its log, so the rest is read from disk) and extended with what is recorded after; it is never written |
| `(recall seq [start [count]])` | **ADR 50.** One event of this session's conversation as the log recorded it (redacted), by its seq: the `N` of `[elided seq=N]`, or a seq `index-search` gave. Only the kinds `index-search` ranks; any other seq is an error saying which it reads. A result is text with lines, as both read it: a shell's as the model read it (the exit status, the output, then `[stderr]` and the errors, as `shell.scm` renders it) but whole, other JSON indented, anything else as recorded. Under a heading naming the event, its lines from `start` (1 unless given), `count` of them (as many as fit unless given), as a ranged `fs-read` returns a file's: `[lines A-B of N]`, stopping at a whole line within the window's cut for one result, so the window never cuts it. A reply comes with a line per call naming the seq of its result. It shows what was, not what is: a file is read again with `fs-read` |
| `(git-status)` `(git-diff [path [staged [against]]])` `(git-head)` | only when the root holds `.git`: the working tree's status (branch, changed, staged and untracked files, short format), and the uncommitted changes as a unified diff, the working tree against the index or with `staged` (`#t`) the index against the last commit; `path` narrows it; `against`, a commit or branch name (never anything that starts like an option), compares with it instead of the last commit, so what was committed since counts too (`--end-of-options` goes before it). `(git-head)`, not offered as a tool, is the commit HEAD names as a full hash, `#f` before the first commit: a strategy measures a turn's change against the commit it began on, so a commit moves nothing out of it (this repository's validate reminder does). Read-only, so no approval: git runs with fixed arguments and everything that would run another program switched off (external diff drivers, text conversion, the file system monitor, the pager), and a diff leaves out the contents of the files `fs-read` refuses. The status names every file, credential files included: a name is no secret, and the evidence loop reads it to know what changed. Untracked files are not in a diff |
| `(shell-exec cmd args)` | run a program with an argument **list** in the repository root, scrubbed environment (`TMPDIR` is the scratch directory); irreversible; returns `(("status" . n) ("stdout" . s) ("stderr" . s))`. A `bash -c` command line runs with `pipefail`, so a pipeline's status is that of its last command that failed (`make test \| tail -n 40` reports the tests' failure; a pipeline into `head` can end in 141 when `head` stops reading early). Whatever the program leaves running in its process group (a server started with `&`) is stopped when it ends: SIGTERM, then SIGKILL after 2 seconds; a program that keeps running goes to `shell-start` |
| `(shell-start cmd args)` | start a program that keeps running (a server, a watcher, a program to talk to) in the repository root, in the background: no shell, the same scrubbed environment and `TMPDIR` as `shell-exec`, its own process group. Irreversible, and checked like `shell-exec` (its `cmd` and `args` are the command line). Returns text: `p1: running (the command line)` and its first output (as soon as there is some, waiting at most a second). At most 4 run at once; every one is stopped when the session closes. A `bash -c` command line runs with `pipefail`, as `shell-exec`'s does |
| `(shell-read id [wait])` | what process `id` printed since the last read (stdout and stderr together), after a first line with its state: `running` or `exited N`. With `wait` (0 to 60 seconds) it first waits for new output or an exit. The latest 1 MiB of output is kept, and a read says how much was dropped before it, or how much is left past the output cap. Read-only, so no approval |
| `(shell-send id text)` | type `text` into the process's input (end a line with `\n`), and return what it printed next (as soon as there is output, waiting at most a second). Irreversible, with the reason that what it does depends on the program: it asks every time; `text` is a payload the credential checks read |
| `(shell-stop id)` | stop the process (SIGTERM to its group, SIGKILL after 2 seconds) and return its last output and exit status. Needs no approval: it ends only what this session started |
| `(term-open cmd args [cols rows])` | **Experimental and temporary (ADR 48); only with `[experimental] terminal = true`.** Run a program on a pseudo-terminal in the repository root (100 by 30 unless `cols` (20 to 250) and `rows` (5 to 100) say otherwise), with `shell-start`'s scrubbed environment, `TERM=xterm-256color`, a UTF-8 locale when there is none and a `TMUX_TMPDIR` of the session's own, so full-screen programs (tmux, neovim, `less`) work. Irreversible, with a confirm that says it is experimental; `cmd` and `args` are checked like `shell-start`'s. Nothing typed into it afterwards is checked. Returns the screen (see `term-screen`) once the program has been quiet for 300 ms. At most 3 are open; all are hung up on when the session closes |
| `(term-type id text)` | type `text` into a terminal, a newline pressing Enter; typed literally, so a word like `Escape` is letters. At most 16 KiB. Returns the screen once the program has been quiet for 150 ms, or after 2 seconds. No approval: it is not checked at all |
| `(term-keys id keys)` | press the keys of a list of strings in order, each a single character, a name (`Enter` `Escape` `Tab` `BTab` `Space` `BSpace` `Delete` `Insert` `Up` `Down` `Left` `Right` `Home` `End` `PageUp` `PageDown` `F1` to `F12`, not case-sensitive) or `C-x` or `M-x` (Control or Alt with a letter). A key that is none of these is an error and none of the list is pressed. At most 200 keys. Returns the screen as `term-type` does. No approval |
| `(term-screen id [offset])` | the terminal's screen as text: a header (`t1: running (nvim main.go) 100x30, cursor row 7 col 12, full-screen`, or `exited N`), then each row with its number and `\| ` (not part of the screen), trailing spaces cut and three or more blank rows in a row shown as one `2-19\| (blank)`. With `offset` (a number of lines), the view scrolled back that many lines through the history of a program that does not use the alternate screen (5000 lines kept), as a scroll wheel would; a full-screen program keeps no history and refuses it. Readable after the program has exited, until `term-close`. Read-only, so no approval |
| `(term-wait id [seconds [until]])` | wait, then return the screen as `term-screen` does and, on a last line, why it returned: `[matched after 2.1s]` when a line of the screen matches the regular expression `until` (Go syntax, multi-line; the rows without their numbers; it returns at once if one already does), `[quiet after 0.5s]` when with no `until` the program has printed nothing for half a second, `[exited after …]`, or `[timed out after …]` after `seconds` (1 to 60, 10 when left out). Read-only, so no approval |
| `(term-close id)` | hang up on the terminal's program (SIGHUP to its process group, SIGKILL after 2 seconds), return its last screen and free the id. A tmux server the program started keeps running, in the session's own `TMUX_TMPDIR`, until the session ends, so a later `term-open` can run `tmux attach`. No approval: it ends only what this session opened |
| `(scratch-dir)` | the session's scratch directory (a session always has one; `nodwyn repl` and `eval` have none of the session's primitives, and `load` is their only host primitive). It is a private directory (mode 0700) under nodwyn's data directory, outside the repository; the file primitives take absolute paths inside it, or paths starting with `$TMPDIR/`, which the interpreter rewrites to it in every path and command argument before the guard; writes there need no approval, and commands get it as `TMPDIR`. It is removed when the session closes, a resumed session gets a new, empty one, and directories a crashed session left are swept once untouched for a week (a session touches its own at every turn). The default system prompt tells the model about it by `$TMPDIR`, not by its path, so the prompt is the same across sessions. |
| `(project-notes)` `(project-notes-save text)` | this repository's notes kept across sessions (how tests run, where things are, runbooks), `""` when there are none; and replacing them. They live in nodwyn's data directory, keyed by the repository's path, never in the repository. A save is capped at 8 KiB and redacted, and its text is a payload the guard checks, so content that looks like a credential needs a person's approval. The default system prompt shows the notes under a heading that calls them leads to check, not instructions. `project-notes-save` is also a tool the model is offered, and the working rules ask it to keep there what a later session would need and could not quickly find |
| `(git-land message)` | offered only in a session given a git worktree of its own (ADR 34, amended 2026-10-05): lands the session's work on its base branch in one step. It commits every change with `message` (needed when there is one; the files `fs-read` refuses are left out and named), signed as the repository's git configuration says with the person's agent (`SSH_AUTH_SOCK`, `GNUPGHOME` and the like pass to git, every prompt is off, so a passphrase prompt fails at once); rebases the session's own commits, those after the worktree's base commit or after where the branch last met the base, onto the newest base (its upstream, fetched first, when it has one); refuses a change whose outgoing diff holds credential-shaped text; pushes to the base's upstream by fast-forward only; and moves the local base branch: its ref when no checkout has it, a fast-forward merge in a clean checkout that has it, nothing (said) in one with changes. A conflict stops the rebase, naming the files; a call after the model resolved them goes on. A `core.hooksPath` inside the worktree is refused, since the model could have written those hooks. Irreversible: it asks, as any such call does. Returns what it did, a line each |
| `(worktree)` | `#f`, or the git worktree made for this session when the person started it in one (ADR 34), as `(("repository" . P) ("branch" . "nodwyn/1968c13a") ("base" . "main") ("base-commit" . C))`: the session's root is then that worktree, at `P/.nodwyn/worktrees/1968c13a`, and `base` is `""` when the repository's `HEAD` was detached. No primitive makes or moves a worktree: the host makes it before the session loads, when a client asks with `worktree.open` |
| `(project-instructions)` | the repository's `AGENTS.md` (or `agents.md`), its maintainers' instructions for coding agents, `""` when it has none. Read as a regular file inside the root after links are followed and past the credential denylist under both names (a link to a key elsewhere reads as no file), capped at 32 KiB with a line that says the file goes on, and redacted, since it rides in every request (ADR 36) |
| `(http-request cred url method position headers body)` | **P3.** Send a request with the credential handle injected at `position` (`header:NAME`, `header:NAME:SCHEME` such as `header:Authorization:Bearer`, `query:NAME`, `body`). Egress: the guard checks the binding table for (class, host, method, position) first. Credential injection requires HTTPS except on loopback unless the user enabled `policy.allow_insecure_http`; the model cannot enable it. Not offered to the model (a handle cannot come from JSON); reach it through a user-registered tool. Returns `(("status" . n) ("body" . s))`, the body read up to 1 MiB |
| `(policy-read name)` | User-only, hidden from the model catalog. Read configured `bindings` or repository `approvals`, returning content, revision and existence; bounded to 64 KiB and rejects recognized secrets. No arbitrary path. |
| `(policy-write name revision content)` | User-only, hidden from the model catalog. Validate TOML and atomically replace that fixed policy after a one-time diff confirmation and revision check. Binding changes reload before egress; approval-file changes need a fresh `nodwyn trust` review before new/resumed sessions load them. |
| `(credential-register class)` | **P3.** Masked input through the front-end, stored in the keychain, returns `#<cred:class:id>`. User origin only: refused to the model and to the loop's strategy; never a model tool |
| `(credential class)` | The handle of a key already in the keychain, or `#f` if there is none (or no keychain is open). An OAuth2 credential (ADR 24; added with `nodwyn credential add --oauth2`) is a handle like any other: its access token is refreshed, when due, just before it is injected. This is how Scheme reaches a keyed service — `http-request` to a model API, say — without asking for the key again. The judge needs none: its primitives carry its server's key themselves. Looking a handle up grants nothing: every use is checked against the binding table. Refused to model-originated code |
| `(judge-yes-no state instructions)` | Ask the judge a yes/no question about a text; returns the probability of yes (0..1). Egress to the judge's server (ADR 46): Jev's System One API (a `noul`), or OpenAI's Decisions API (a `predicate`) for a judge on an OpenAI server, whose answer is read into the same values. Go injects the server's stored key, and the guard judges the call as carrying its class, so that server's binding decides it. Everything sent (state, instructions, levels, options) is redacted (L1) before it leaves, and every argument is a payload; logged as `jev.req` with the call's origin. A Decisions refusal is an error |
| `(judge-score state instructions levels)` | Rate a text on an ordered rubric of 2–10 level strings, low to high; returns `(("score" . 0..1) ("raw" . n) ("levels" . k) ("confidence" . c))` |
| `(judge-choice state instructions options)` | Pick one of the options (an alist name → description); returns `(("choice" . name) ("confidence" . c) ("probabilities" . ((name . p) …)))` |
| `judge-yes-no`, `judge-score`, `judge-choice` | The three ask the task's judge (ADR 47): `[judge]`'s, or the one the task was opened with or moved to, read on every call, so a call after a switch goes to the new judge with its key, and one with no judge is an error. They are registered when a judge could be asked at all (a keychain open, `judge.content` not `none`, a judge provider in `config.toml`), and are the model's tools only at `judge.content = "full"` and when the task starts with a judge whose key is stored, because the model chooses the text it sends (ADR 20, amended); below that they are hidden, for the plan gate and your Scheme. A tool you register may call them at any level, with the model's origin: which judge-backed tools exist is yours to decide (ADR 20 §3) |
| `(judge-available?)` | Whether the task has a judge now whose key is stored: what the plan gate's `(plan-judge?)` asks. Hidden, no egress; registered with the judge's primitives (ADR 47) |
| `(doc-export source target)` | **W5.** Convert a Markdown (`.md`) or LaTeX (`.tex`) file to a Word document (`.docx`), OpenDocument text (`.odt`) or HTML page (`.html`) with pandoc (2.15 or later, from `PATH`); the target's extension picks the format. `source` is a source (read: pinned, denylisted, a read-only mount may supply it) and `target` a path (written): irreversible, so it asks with a preview (source → target, formats, whether it creates or replaces), a standing write approval covers it as it covers `fs-write`, and the target's previous version is kept. pandoc runs twice with fixed arguments, no shell, the scrubbed environment and `shell-exec`'s timeout: `--sandbox --from=F --to=json` reads the source on stdin and nothing else (an `\input`, an include or a remote image reads nothing); Go then keeps, for `.docx` and `.odt`, only the images that are files in the workspace and not credential-shaped, by absolute path, and turns any other into its description, and for `.html` drops images linked from elsewhere; raw content (OpenXML, HTML, LaTeX written into the source) becomes literal code in every format; `--from=json --to=T --standalone --output=-` writes the result, which Go writes to `target`. Nothing the model writes becomes a pandoc option. No PDF: a TeX engine reads and runs what a document asks, so a LaTeX build stays `shell-exec` (`latexmk`). Returns what it wrote, the images it left out and pandoc's warnings |
| `(google-doc-upload path [title])` | **W5.** Send a `.docx`, `.odt`, `.html` or `.txt` file (at most 5 MiB, Drive's limit for one upload) to Google Drive as a new Google Doc, in one multipart upload asking for `application/vnd.google-apps.document`; returns `(("id" . ID) ("url" . "https://docs.google.com/document/d/ID/edit") ("title" . T))`. `title` defaults to the file's name without its extension. Go uses the stored credential of the class `docs.google_credential` names (default `google`, an OAuth2 credential from `nodwyn credential add --oauth2 google`, whose narrow `drive.file` scope suffices), and the guard judges the call as carrying that class to the upload endpoint's host (`www.googleapis.com`) by `POST` at `header:Authorization:Bearer`: unbound is denied before anything is asked. Egress and irreversible: how a call is handled is the person's setting `docs.google_upload`. With `ask` every call is a security confirm naming the file, its size and the destination (the confirm also names the class and scopes), which no standing answer, `--yolo`, `--judge` or unattended run answers yes. With `allow` no call asks. With `deny` every call is refused and later sessions are not offered the tool. While it is unset, the first call asks the person which and saves the answer (`setting.change`). The file's bytes never become a Scheme value, so the handler scans them itself before sending (L1, `extract.AllText`: a text file as it is; every XML part of a `.docx` or `.odt`, runs joined; an embedded file is refused) and refuses a known secret shape whose class is not bound for that host. The token is injected through the session, so an OAuth2 token is refreshed first and `cred.refresh` logged; each upload is recorded as `doc.upload` (file, size, hash, destination, document id; never content or token). Registered only when the session starts with such a credential stored and the setting is not `deny`. `path` is a source; `title` is a payload |

MCP servers you approve in the console (ADR 23) add one primitive per tool
you turned on, named `server__tool` (model APIs refuse `/`; the console shows
`server/tool`). Each is egress to that server's host, with the server's key,
if it has one, bound to it, and a tool you did not declare as reading only confirms every
call. Every argument is a payload: a literal key in it is refused unless its
class is bound for that host (L1), and content from outside may go there
because you approved the server, key or not (L2). Results are tagged
`external:mcp`. They are offered to the model like the tools above.

A contained server's tools (ADR 23 W3b) send to its container, `mcp:NAME`,
which may pass the arguments on to the hosts of its network list and
nowhere else. A literal key in them, or a value derived from a credential,
is refused unless its class is bound for every one of those hosts; with no
network, always. The call carries no credential: the server's key went in
when it started, and its binding is checked again before each call. The
confirm and the tool's description name the contained server and what it
can reach.

### Provenance (L2)

Every value carries **provenance tags**: where it came from, not what it
contains. A host primitive tags its result (`fs-read`, `fs-list`, `fs-find`,
`index-grep`, `git-status`, `git-diff`, `doc-export` and `policy-read` mint
`external:file`; `shell-exec` and the background-process primitives mint
`external:shell`; `http-request`, the `judge-*` primitives and
`google-doc-upload` mint `external:http`;
MCP tools mint `external:mcp`; `session-list`, `session-events`,
`index-search` and `recall` mint `external:log`), a credential
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

After the prelude, nodwyn loads the current `./.nodwyn/init.scm` only after
external user trust (ADR 14, amended by ADR 45). `nodwyn trust` reviews and
accepts the init/allow revision; a clone cannot grant its own startup authority.

## 7. Primitives and the guard

The **primitive table** maps a name to a Go handler plus metadata. It is the
only surface through which Scheme reaches the host (SPEC invariant 4).

Every primitive declares:

| Field | Meaning |
|---|---|
| `doc` | one line, shown to the model as the tool description |
| `params` | ordered list of `(name type doc)`; `type` is one of `string integer number boolean list alist vector any`. A Go primitive may mark trailing parameters optional (ADR 29): a call may stop before any of them, and the handler sees fewer arguments; one skipped before a later one that is given gets its type's empty value. A variadic primitive cannot also have optional parameters. A Go primitive's list parameter whose elements are objects may describe their fields (`Items`, ADR 38): the tool schema gives it an `items` object schema, and the guard reads a field's role as it reads a parameter's |
| `variadic` | extra arguments allowed after the declared ones |
| `egress` | may send bytes off-machine |
| `irreversible` | cannot be undone by the harness |
| `host` | touches the OS (filesystem, processes, network) |
| `origin` | `builtin` or `scheme` |
| `hidden` | never offered to the model as a tool (`load`, `register-check`, the loop's primitives) |
| `audited` | its guard decision is logged even when it is an allow of a pure builtin |
| `preview` | Go-only, for irreversible host primitives: renders what the call would do (a diff) for the confirm prompt |

Parameter **roles** (`path`, `source`, `dest`, `payload`, `credential`, and
for commands and keyed requests `argv`, `method` and `position`) tell the
guard what an argument *is*; they are declared by host primitives from B2
on. A `path` is what the primitive acts on, and writes when it is
irreversible; a `source` is a file it only reads, whatever else it does
(`doc-export`'s document, the file `google-doc-upload` sends). Both are
pinned and checked against the credential-file denylist, but a read-only
mount may supply a `source` even to an irreversible primitive. A write is an
irreversible primitive with a `path` and something to put there: a
`payload`, or a `source`. A field of a list parameter's elements may carry a
role too (ADR 38): each `new` in `fs-edit`'s `edits` is a `payload`, so the
secret scan and the taint check see every replacement, and not the `old`
text it replaces, as they see a single edit's `new`.

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
redefine either. Stripped of `/compact`, `/optimize`, the plan gate, `ask-model`'s retry
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
| `(model-turn)` `(model-turn name)` `(model-turn name options)` | Ask a model once: the control model (`"control"`), or the configured model `[models.NAME]` (manual, "Choosing models"). `options` is an alist; `'((tools . #f))` offers the model no tools, as the planner's request does, and `'((tools "fs-read" "fs-list"))` only the named ones (an unknown name is an error that lists the tools), as a sub-agent's requests do. Both are recorded (`llm.req` `no_tools`, `offered_tools`) so replay rebuilds the request. Any other value but `#t` is an error: `'((tools #f))`, a list holding #f, is not a silent yes. `'((headroom . #t))` lets the request run past the window's high water, up to its ceiling (`(window-limits)`), before older context is trimmed, as compaction's note request does; `llm.req` records that trim line as `high_water`. Returns an alist: `content`, `thinking`, `done` (why it stopped), `tool-calls`, each call an alist of `id`, `name` and `args` (the model's JSON text; a call whose arguments the model wrapped in a JSON string, the whole object or a list or object parameter, arrives unwrapped and the log notes it, ADR 29), and `prompt-tokens` and `output-tokens`, the server's counts for this request (0 when it gives none), `cached-tokens`, the prompt tokens the provider read from its cache (`#f` when it does not say), `reasoning-tokens`, the part of `output-tokens` spent thinking (0 when the server does not say), and `cost`, the request's cost in USD as the provider reported it or as the model's configured prices estimate it (`#f` when neither). The window is built from the log, so the model sees what the strategy's earlier calls recorded; each request is recorded, with the model it went to, for `nodwyn replay --window`. An attached image in the window goes only to a model that takes images at the host its confirm named; any other request carries a line saying why it does not, recorded as `llm.req` `withheld_images` (ADR 22, amended 2026-10-01). |
| `(run-tools calls)` | Run calls in order, each through the guard. Returns an alist per call: `id`, `name`, `ok`, `value` (the text the model reads, carrying the tool's provenance), `error`, `denied`. |
| `(skip-tools calls reason)` | Answer calls without running them; the model reads `Not executed: reason`. |
| `(add-message text)` | Add text to the conversation, logged as `harness.msg`. The model reads it as a user message at the next `model-turn`; the current task stays the user's. Refused while calls from the latest `model-turn` are unanswered, since a call must be followed by its result. |
| `(fold-result result text)` | Show a tool result as `text` in later windows, such as an outline or a line saying what it was, once its details stopped mattering; `result` is the `tool.result` event's seq or the id of the call it answers. Recorded as `result.fold`, so replay rebuilds the same window (recipe window-v9); the result itself stays in the log for audit and `recall`. Folding an old result changes the prompt from that point on, so the server re-reads what follows it: fold in batches, or results near the end |
| `(narrow-context system [agent [options]])` | Start a fresh context, like Emacs's narrowing: until `fold-context`, `widen` or the user's next message, models see `system` as their system prompt and only what comes after this point. Follow it with `add-message` to hand over the task. This is how a model gets a task and a plan without the conversation that led to them. Contexts nest. Calls from the latest `model-turn` that are still unanswered wait for this context to fold; its own `model-turn`s start with none pending. `agent` labels the context in the log. `options` is an alist; `'((lasting . #t))` makes the narrowing outlast the turn, as compaction's does (§9.2): if it is still open at the user's next message, the conversation starts there from then on, under the session's system prompt, and what came before stays in the log only. Recorded as `context.narrow` `lasting` (recipe window-v11). Any other value but `#t` or `#f` is an error. |
| `(fold-context)` | End the innermost narrowed context and fold it away: later windows skip everything since its `narrow-context` and see the enclosing context again; the log keeps it all. The calls that waited for it are the latest `model-turn`'s again. Refused while calls are unanswered. |
| `(widen)` | End every narrowed context. Models see the whole conversation again, including what happened while it was narrowed, from where the latest lasting narrowing of an earlier turn started it. A new user message widens too, except that a lasting narrowing still open becomes where the conversation starts. Refused while a context holds calls waiting for an answer: fold it. |
| `(answer-call call text)` | Answer one of the model's calls with `text`, as a successful result, without running the tool: how a tool the strategy handles itself answers. Counts against the tool-call budget. |
| `(session-system-prompt)` | The system prompt the session started with (§9.1), fixed for its life: what a narrowed context passes to `narrow-context` to keep the model's standing instructions. |
| `(session-requests)` | The user's messages the session holds, oldest first, as the log recorded them (redacted); the running turn's request is the last. A session opened here holds every one; a resumed one holds the end of its log (ADR 49 §10), which has at least the latest three and the latest that is not a `/compact`. Read from the log, so it holds across a resume: how `/compact` finds the request it hands over (§9.2). |
| `(request-context)` | What the latest request answers and carries beside its words, as the log recorded it (redacted): an alist of `reply`, the agent's last answer before it (its last reply that said something and called no tool; `""` when there is none), and `attachments`, the names of the files attached since the request before it. How the plan gate's explorer and planner, in contexts of their own, see what a reply answers (§9.5). |
| `(model-info)` `(model-info name)` | What is known about a model before asking it: the control model, or the configured model `[models.NAME]`. An alist: `name`, `model` (the provider's id), `provider`, `num-ctx` (its context length, or what its provider publishes; 0 when neither says), `max-output` (the reply cap its requests carry: its `max_output`, else the longest reply its provider publishes at most a quarter of its context, else `window.output_reserve`, at most a quarter of a context too small for the window and that reply), and `temperature` and `seed` (its sampling settings, `#f` when the server decides) and `labels` (the words its config entry gives it, a list of strings). An unknown name is an error that lists the configured ones. |
| `(model-names [label])` | The names `model-turn` and `model-info` accept: `"control"` and each configured `[models.NAME]`, sorted. With a `label`, only the models that carry it: `(model-names "fast")`. A model's labels are its entry's, else those of another entry for the same model, as its efforts are; the default model goes by its `[models.NAME]` entry's. |
| `(tool-names)` | The names of the tools the model is offered, in the order it is offered them: the names `model-turn`'s `tools` option and `sub-agent`'s `tools` list accept. Empty until the tool list is fixed, once the session's Scheme has loaded (ADR 9). |
| `(window-limits)` | The window's sizes, in tokens as the window estimates them: an alist of `high-water` (past it older context is trimmed), `low-water` (what trimming goes down to), `ceiling` (the most a request with headroom may carry: the control model's context less the reply cap, or the high water when nothing is known above it), `max-result-tokens` (a longer tool result is shortened) and `output-reserve` (the default reply cap). The water marks grow with a large context unless config.toml sets them, and shrink to fit a small one whether set or not (manual, "Choosing models"). Compaction's default threshold is read from it (§9.2). |
| `(context-tokens)` `(context-tokens result)` | The size of the context the next `model-turn` would send, in the units of `(window-limits)`, before any trimming. Unlike a reply's `prompt-tokens` it counts as the window does, whatever the server's tokenizer, and includes the tool results added since the last request. Compaction measures it (§9.2). With a tool result (its seq, or the id of the call it answers, as for `fold-result`), only the part from that result to the end: what a server with a prompt cache reads again when the result is folded, since the prompt before it is unchanged. `#f` when the window does not show the result (a narrowed context or a compaction left it out). `hygiene.scm` weighs its folds with it (§9.6). |

When nobody reviews the work as it happens — `(unattended?)` (`--yolo`, a
run's `auto_approve`, `--judge`, `--unattended`) or a supervised run's turn
(`plan-required?`) — the
default `agent-turn` ends a turn on evidence, not on the model's word
(`evidence-loop`): a reply that stops before anything changed, or with a
change and no test run after it, gets `finish-no-change-message` or
`finish-untested-message`, at most `finish-reminders` (2) times; one whose
last test or check run after the last edit exited non-zero gets
`finish-failing-message` once until the next edit, and may answer that the
tests fail on the unchanged code too and end. An exit that says no test ran
is no failure: `not-run-statuses` (126 and 127, a program not found or not
executable) and, for a pytest command, `pytest-not-run-statuses` (4 and 5, a
path it could not find, no tests collected). A run on the copy of the last
commit (`$TMPDIR/base`, `base-run?`) tests nothing of the change; one that
fails after the change's run failed is the comparison done, and the failing
run is not told. After `finish-no-change-message` a turn that has run a test
or check may end unchanged: the code may already do what the request asks,
and the message says to show it with a check (ADR 32, amended 2026-10-10). A
command is
a test run when its command line contains one of `test-command-words`
(pytest, go test, cargo test, npm test, make test, …; a project may redefine
the list). After `unrunnable-after` (6) test commands in a row fail to run,
the model gets `(finish-unrunnable-message n)` once: finish and report the
change as unverified rather than spend the budget on a broken environment.
When a person reads the turn's answer before anything else happens,
`(reviewed?)` (a chat's turn, or a supervised run's turn that a run
checkpoint follows; not an unattended run, nor a run's last turn), a reply
that changed nothing gets `finish-no-change-reviewed-message` instead: make
the change, unless the request asks for no change yet, such as a proposal to
review first. After it the turn may end unchanged.
Where `git-status` exists, it names every changed file, credential files
included (a name is no secret; `git-diff` leaves their contents out), so a
file changed through the shell counts.
The scratch directory is not the repository: a file the model writes there
(as `$TMPDIR/…` or by its path) is not a change, and a command that runs one
of those files — its check script — is a test run. A check fails until the
work is done, so its failing runs never count toward `unrunnable-after`.
In a turn nobody reads (`(unattended?)`), a test or check run that passed
after the turn changed something has the loop call `(keep-passing)`: the host
keeps the repository's changes as they are. If a hard limit or the progress
guard then stops the turn, which no strategy can catch, the core puts the
files back to that state, saves what they held after it, and says so in the
log and in the turn's end (ADR 18, amended 2026-10-06). A turn that changed
nothing keeps nothing, and a chat, a person's own stop or a soft limit put
nothing back.
Where `git-status` exists, git says whether anything changed, edits made
through the shell included: a tracked file's status differs from the start
of the turn, or the file tools wrote a file git now lists as new or listed
already; files a run leaves behind are no change. Files new
since the turn began that git does not track get
`(finish-new-files-message files)` once before the turn ends: whatever is
left in the repository is in the change. Files the turn's plan names, by path
or by name (`plan-names?`), are left out of it: the plan asked for them.

Both loops also count, in `stall-check`, the rounds in a row whose reply
thought at length (`stall-reasoning-tokens`, 10,000, or more reasoning
tokens, as `ask-model` noted in `last-reasoning`) and whose calls wrote
nothing; at `stall-rounds` (3; 0 turns it off) the model gets
`stall-message`, to write down the design it reached and make the edit, and
the count starts again. Each turn starts it at 0.

Both loops, the chat's and `evidence-loop`, count the rounds in a row whose
calls only look, at the repository or the conversation: every call names
one of `look-tools` (`fs-read`, `fs-list`, `fs-find`, `index-grep`,
`read-definition`, `git-status`, `git-diff`, `index-search`, `recall`; a
project adds the tools of its own that only look). After `reading-reminder-after` (12; a table, §9.7) such rounds the model gets
`(reading-reminder-message n)`, or `(reading-reminder-reviewed-message n)`
when `(reviewed?)`: its thinking may not come back to it, so write down what it found
now, as a plan with `plan-write`, or, when a person reads the answer and the
request asks a question or needs their decision, as the answer. A turn that
has a plan (`(plan-recorded?)`) is told instead to make the change for the
plan's next unmarked step, and to write the plan again only if it must
change, since a step it rewords starts unmarked. Any other
call starts the count again, and so does the reminder; 0 turns it off.

| | |
|---|---|
| `(unattended?)` | true when nobody approves this session's calls: it answers ordinary confirms itself (`--yolo`, a run's `auto_approve`), the judge answers them (`--judge`), or nobody is there at all (`nodwyn run --unattended`). `[approval]` (ADR 35) does not make it true: a chat whose commands the judge answers still has a person to ask |

A reply's `done` is `"length"` when the model reached the output cap (the
model's reply cap: its `max_output`, else the longest reply its provider publishes (at most a quarter of its context), else `window.output_reserve`, at most a quarter of a context too small for the window and that reply); the core notes it in the log. What to do is the
strategy's: `(ask-model …)` is `model-turn`, asked again with
`output-cut-message` when the reply was cut with no tool call to answer, at
most `output-cut-retries` (2) times. A plain `(ask-model)`, the one that offers
every tool, is also asked again, once (`empty-reply-retries`), with
`empty-reply-message`, when the reply has no words and no tool call: such a
reply would end the turn with nothing to read. The default loop, the planner
and `sub-agent` use it.

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
`record-step`, `record-compaction`, `narrow-context`, `fold-context`, `widen`) are refused outside
a turn and from model-originated code; the ones that only describe the session (`model-info`, `model-names`,
`window-limits`, `session-system-prompt`, `session-requests`,
`context-tokens`, `unattended?`, `reviewed?`, `plan-required?`, `plan-recorded?`, `run-plan`,
`unfinished-plan`, `session-list`, `session-events`) answer at any time (`session-system-prompt`
and `context-tokens` once the session has started), which
is how `compact.scm` reads `(window-limits)` as it loads. The strategy runs with harness origin, so it cannot
strip provenance or change policy (`wipe`, `register-check` and `policy-write`
are user-only); the tools it runs execute with model origin, as if the model
had called them directly. Whatever the strategy does:

- it runs or skips only calls the model made, each once, with the arguments
  the model sent;
- every call gets exactly one result: calls left unanswered are skipped when
  the strategy asks the model again or when the turn ends;
- budgets, the tool-call limit and the progress guard (the same call four
  times in a row, a denied call retried three times, ten failures in a row,
  one tool failing on one file in six of the last sixteen responses, a
  background command still running read thirty times in a row)
  end the turn with an error no `guard` clause can catch (§4), and so does a
  failure to write the log, which is the source of truth.

### 9.1 Prompts

Every prompt nodwyn sends is Scheme. The defaults live in
`internal/loop/prompts.scm`, which loads with the default strategy, before
`.nodwyn/init.scm`; a project redefines any of them there, usually with raw
strings (§1.1):

| | |
|---|---|
| `(system-prompt repo)` | the control model's system prompt: `(unattended-system-prompt repo)` when `(unattended?)`, else `(interactive-system-prompt repo)`. Unattended, it is how to work — understand the request, set up a check that runs, make the whole change, validate by running it, report honestly — instead of announcing each call to an approver. Interactive, the answer says what changed, how it was checked and what was not, and names the choices made when the user went ahead without answering them; a question is answered, and a request for something never built is told so, with what exists, what is missing and what the model would build, before it builds anything |
| `plan-explore-system`, `(plan-explore-task request)`, `(plan-draft-with-findings request findings)` | the explorer that looks before a plan is drafted (§9.5): the code at the place the request describes first, every place the change must reach, the request's own examples and formats copied exactly, the options it finds without choosing one, the tests involved, and the documents and what the repository's instructions for coding agents (`AGENTS.md`) ask a change to include; and the draft request that carries what it found |
| `finish-no-change-message`, `finish-no-change-reviewed-message`, `finish-untested-message`, `finish-failing-message`, `(finish-unrunnable-message n)`, `(finish-new-files-message files)` | what the evidence loop tells the model (§9) |
| `(reading-reminder-message n)`, `(reading-reminder-reviewed-message n)` | what the default loop tells a model whose last `n` rounds only looked (§9) |
| `(plan-findings-message findings)` | what the model doing the work is told of the explorer's findings once a plan is recorded (§9.5): leads found without running anything, and any fix in them a guess to test |
| `task-header` `plan-header` | the headers under which the window shows the user's request, at the head of their message, and its plan, as a message of its own after the calls it came with are answered; neither is ever trimmed away. The system prompt stays the same, so a new request or a plan keeps the server's cached prefix (window recipe v15); before v15 the system prompt carried both |
| `(run-turn-prompt task complete-line turn max-turns)` | the message a supervised run sends for each turn; `complete-line` is the marker the run looks for. It asks for the plan's next unmarked step and says the plan carries over to the next turn (§9.5) |
| `(plan-system-prompt repo)` | the planner's system prompt |
| `(plan-shape)` | the shape every plan is asked for, built from `plan-kinds` (§9.5): each step starts with its kind and a short name for what it does, then — and the detail the console folds away; a step that says all it should in its name needs no dash; the last is the `Check:`; a plan with no test or no doc to change says so, and why, in a step (`Test: none — only docs change`) |
| `(plan-draft-prompt request)` | the planner's request for a plan, in the shape `(plan-shape)` gives |
| `(plan-revise-prompt request plan score)` | the request for a better plan after the judge scored it `score` (0..1), with the shape again: the planner revises in a context of its own |
| `plan-needed-question` | the judge's yes/no question: does this request need a plan? A plan is a plan of changes: a question, a review, a comparison, an explanation or a proposal asks for an answer and needs none, however much reading the answer takes; a reply to the model's question or a correction carries on the request before it |
| `(plan-request-with-earlier earlier request)` | a chat's latest request after the person's messages before it, `earlier`, as the gate's judge, explorer and planner read it (`plan-chat-request`) |
| `(plan-request-after-reply reply)`, `(plan-request-attachments files)` | what the explorer and the planner read beside that, never the judge (`plan-model-request`): the agent's last answer before it, and the names of the files the person attached, with the instruction to say what they cannot tell rather than guess |
| `compact-summary-prompt`, `(compact-handover-prompt request note)` | compaction's request for a handover note and the start of the compacted context (§9.2) |
| `(optimizer-system-prompt)`, `(optimizer-task history focus)` | the optimizer's system prompt and task (§9.4) |
| `plan-score-rubric` | the judge's instruction for scoring a plan: it fulfils the request, and its steps have the shape the planner is asked for |
| `plan-score-levels` | the rubric's 2–10 levels, worst first. A plan whose tests do not say what they check and why they fail today, or that leaves out the docs, does not reach the top one |
| `working-rules` | how to work, added to both system prompts (§9.6): shell command lines, programs that keep running or may hang (`shell-start`), the project's environment, reading with the file tools, calls that do not depend on each other in one reply, a plan as a first guess, the check loop, following the request's conclusion, widening in general form, whole test modules (and code a browser runs checked by the project's browser checks; a build or a syntax check is not a check that it works), comparing failures with the unchanged code only when tests fail, on a copy of the last commit and never with `git stash`, finding regressions file by file, keeping what a later session would need in the project notes (`project-notes-save`) |
| `(git-rule)` | in both system prompts after `working-rules`, only where `git-diff` is offered: look at the change with `git-status` and `git-diff` before finishing; it holds nothing it does not use |
| `(worktree-section)` | in both system prompts after `(git-rule)`, only in a session given a git worktree of its own (`(worktree)` is not `#f`, ADR 34): the folder is a worktree made for the session, on its branch, from its base, of the repository at its path; other sessions may work in other worktrees of it at the same time, so work only here and leave other branches alone; the changes reach the repository only through the branch, which the answer names. It also says never to use `git stash` there, since every worktree shares one stash, and to compare with the unchanged code on a copy of the last commit (`git archive HEAD \| tar -x -C "$TMPDIR/base"`) instead. `""` in any other session, so their prompts are unchanged (ADR 32, amended 2026-10-03) |
| `(project-instructions-section)` | in both system prompts after the workspace's folders and before the project notes: the repository's `AGENTS.md` under a heading that says to follow it where it applies and the request where they differ, or `""` without one. Read when the session starts, so an edit to the file shows in the next session (ADR 36) |

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

Every compaction, whatever set it off and whether it worked, is recorded as
`context.compact` (`record-compaction`): the trigger (`auto`, `command` for
`/compact`, `requested` for `compact-next-round`), whether it compacted, the
context's size before and after in the window's estimated tokens, the
threshold an automatic one passed, the focus, and the handover note's reply
(its `llm.resp` seq, its length in tokens, and whether it stopped at the
output limit). The clients show it as one line, such as
`compacted (past ~58,983 tokens): context ~59,210 → ~4,100 tokens`, and one
that gave up as a warning.

A strategy asks for compaction at the next round, whatever the size, with
`(compact-next-round [focus])`: `maybe-compact` runs it where the loop next
calls it, in this turn or the next, and the request is used once.

| | |
|---|---|
| `compact-threshold` | context tokens, as `(context-tokens)` counts them, past which the context is compacted; `#f` never compacts on its own (`/compact` and `compact-next-round` still do). The default is just under the window's high water, a twenty-fifth below it (`(window-limits)`: 58983 at the default 61440), so only work that really runs long compacts (ADR 32) |
| `compact-attempts` | replies to the note request that may be tool calls (they are skipped), or notes cut off at the output limit (asked for again, shorter, with `compact-note-cut-message`), before compaction gives up or keeps the cut note; after giving up it waits for the context to grow another quarter of the threshold |
| `compact-stop-unchanged` | `#t` (the default): the second automatic compaction of a turn with no `fs-write` or `fs-edit` asked for since the first stops the turn instead, with `compact-stop-message` and `turn-stopping` set, so the model's next reply is asked without tools and ends the turn; `#f` compacts as often as the context fills |
| `turn-stopping` | (strategy.scm) `#f`, or the message that stopped the turn: while it is set `ask-model` offers no tools and the finish checks ask nothing more; each turn starts with it `#f` |
| `compact-summary-prompt`, `compact-note-cut-message` | the request for the note, at most 600 words with an edit decided and not made first, and the request for a shorter one when it was cut off (prompts, §9.1) |
| `(compact-focus-prompt focus)` | what the note must keep, as the user put it after `/compact` or a strategy gave `compact-next-round`, added after `compact-summary-prompt` |
| `(compact-handover-prompt request note)` | what the compacted context starts with: the request, and the note with a request to make the edit it names as decided and not made, then re-run its reproduction and re-check its findings before building on them |
| `(maybe-compact request reply)` | compact if a strategy asked for it with `compact-next-round`, or if the context the next `model-turn` would send passed the threshold; call it once the reply's calls are answered (`reply` itself is not read) |
| `(compact-context request [focus [trigger]])` | compact now, handing over `request`; `#t` if the turn continues in the narrowed context. `trigger` (`auto`, `command`, or `requested`, the default) is what the record says set it off |
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
| `(sub-agent system task tools [name [options]])` | run `task` in a narrowed context under `system`; `tools` lists the tool names it may use (its model is offered only those, and other calls are skipped; `'()` offers none), is a procedure that takes a call and says whether it may run (its model is then offered every tool), or is `#f` for all; `name` (or `#f`) labels it in the log (default: the agent tool being answered, else `sub-agent`); `options` is an alist, `'((model . "NAME"))` asking the configured model `NAME` instead of the control model; returns its final answer, after `fold-context`. An error on the way folds its context before it is raised again, unchanged, so a caller that catches it goes on in its own context |
| `(delegate-models)`, the `delegate` tool | the configured models this task's model may hand work to, chosen when the task opened (none by default; never `"control"`; hidden primitive). When there are any, a `delegate` tool is offered: `model` and `task`, run as a sub-agent on that model, offered every tool but `delegate` (`tool-names`). A helper that fails (its provider down, out of quota, refusing the request) answers the call with its error, so the task's model goes on without it. Its description is fixed with the tool list, so a call with an empty `task` lists the models, with each one's id, context length and current labels; the model chooses by labels, not names. A model outside the set is refused whatever name is asked for. No exposed model, no tool |
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
| `(reload)` | run the currently trusted revision of `.nodwyn/init.scm` again, with the caller's origin; changed or revoked project trust refuses before evaluating any code. An explicit Go-host `Options.InitFile` remains host-authorised. The strategy, compaction settings, agent-tool handlers and anything else read when used apply from the next turn; a tool keeps its schema and takes a changed body. What a session is fixed with (ADR 9) does not change: new or changed tools, guard checks (`register-check`) and prompts are returned as notes and apply from the next session or a resume |

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
asks the user and shows the diff; a session nobody approves (`--yolo`,
`--judge`, an unattended run) refuses those writes, so `/optimize` needs a
person at the prompt. A changed project init is kept pending review with
`nodwyn trust`; the new code has not run. Once trusted, reload or resume to use it.
For an explicitly authorised init, reload still applies immediately; an ordinary
reload failure restores the old file. The model is told which outcome occurred.
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
(§5: `[judge]` on a provider, its primitives registered and its server's key stored), it asks
`plan-needed-question` about the request, in a chat's later turn after the
person's messages before it (`plan-chat-request`); a request that needs a plan gets one drafted by
the control model in a narrowed context of its own (§9), which also reads the agent's last answer and the names of the files attached (`plan-model-request`), offered no
tools and folded away afterwards, then scored against the request and revised until it scores
`plan-accept-score` or `plan-max-revisions` run out. The outcome is
recorded as `gate.plan`, and the window shows the plan to the model for
the rest of the turn. Without a judge there is no gate. If the judge's
yes/no question fails, the model plans anyway, unscored ("degraded"); a
judge the guard refuses (an unbound key) ends the turn, as any denial does.

A run's plan is the run's, not the turn's. Its first turn drafts the plan;
each later turn carries it (`plan-carry`): it records the same plan again,
decision `carried`, with the steps the run's turns have marked, so the steps
keep their numbers, `plan-step` answers with the right next step, and the
window shows the marks under the plan. Nothing is explored, drafted or
scored for a carried plan. Only when every step is marked and the run goes
on does a turn draft a new one, from the request with the previous plan and
how each step went (`plan-run-request`). A model that finds the plan wrong
marks the steps that no longer apply skipped, saying why.

A chat's plan is carried the same way past a turn that did not finish. When
an error, a limit or the person's Stop ended the turn, the next message
("continue", or a correction) carries its plan with the steps marked so far
(`unfinished-plan`, then `plan-carry`), so the model goes on from the next
unmarked step instead of reading the repository again and writing the plan
again. A turn that finished, its answer given, leaves no plan: the next
message is a new request, gated as usual.

Every plan, the planner's and the model's own, is asked for in one shape
(ADR 32, amended 2026-10-03): numbered steps, each starting with its kind
and a short name for what it does, then a dash (—) and the detail after
it; a step that says all it should in its name needs no dash.
`Change:` names a file (or function) and what changes in it; `Test:` the
test file and name, the behaviour it checks and why it fails on the
unchanged code ("fails today: …"), or that it guards behaviour that
already works ("passes today: guards …"); `Docs:` each file a person or a
later session reads that the change makes wrong or incomplete; and the last
step, `Check:`, the command that shows the whole change works. A plan with
no test or no doc to change says so in a step, with the reason. The judge
scores against the shape, and `plan-write` turns down a plan without it.

| | |
|---|---|
| `plan-needed-threshold` | the judge's probability of "needs a plan" at which a request gets one (0.5) |
| `plan-accept-score` | the normalised score at which a plan is accepted (0.7) |
| `plan-max-revisions` | revisions after the first draft (3) |
| `plan-empty-retries` | more times the planner is asked when it answers with no plan, in a fresh context each time and noted in the log (2): an empty reply, tool calls, a line that opens a fenced code block, or more than `plan-max-chars` (8000) characters (`plan-not-a-plan`); after the retries the last reply stands. An empty revision keeps the best plan |
| `plan-explore-rounds` | before the first draft, a read-only sub-agent labelled `explorer` (fs-read, fs-list, index-grep, fs-find) looks at the repository for up to this many tool rounds and the planner is told what it found, so the plan is not written blind (12, and 16 for the muse-spark, glm, qwen, deepseek and gpt-6-luna families, whose rounds hold fewer calls: a table, §9.7); 0 skips it. Once the plan is recorded the model doing the work is told too (`plan-hand-over`, `plan-findings-message`), so it does not read the same files again to learn it. Its reads come before the plan is recorded, which the core allows: none is irreversible |
| `plan-earlier-requests` | in a chat's later turn, how many of the person's messages before it the gate's judge, explorer and planner read first, so a reply to the model's question or a correction is not read alone (2); 0 reads the request alone. Each is cut at `plan-earlier-cap` characters (2000). A run's turns each repeat the run's task, and read it alone |
| `(record-compaction facts)` | record a compaction as `context.compact`, from an alist: `trigger` (`auto`, `command` or `requested`), `compacted`, `before` (the size `(context-tokens)` gave before it), `threshold` and `focus`. The size after and the note's reply are read when it is called, so call it once the handover is in place, as `compact-context` does |
| `(record-plan outcome)` | record the gate's outcome as `gate.plan`, from an alist: `decision` (`plan`, `proceed`, `degraded`, `required`, `carried` or `model`), `plan`, `score`, `revisions`, `reason`, and `marks`, the steps of the plan already marked, each an alist of `step`, `state` and `note` as `run-plan` and `unfinished-plan` give them; the turn's `record-step` and the window start from them. Without `marks`, a plan recorded in a turn that has marked steps of an earlier plan keeps the marks of the steps it keeps word for word, at their new numbers. An error for a step the plan does not have, another state, a step marked twice, or marks without a plan |
| `(plan-required?)` | true when this turn must plan before it acts, as a supervised run's turns must |
| `(reviewed?)` | true when a person reads this turn's answer before anything else happens: a chat's turn, and a supervised run's turn that a run checkpoint follows; false in an unattended run, in a run's last turn (no checkpoint follows it) and outside a turn. The evidence loop lets a reviewed turn end unchanged when the request asks for no change yet |
| `(plan-recorded?)` | true once this turn has recorded a non-empty plan |
| `(keep-passing)` | keep the repository's uncommitted changes as they are now, as a state a test run passed on, and return how many files were kept; `#f` when the session has no keeper, an error saying why when it cannot keep (not a git work tree, a file over 8 MiB, more than 64 MiB or 4000 files). What is kept is the files git lists as changed (modified, new, deleted) as the bytes on disk, in the scratch directory, replacing the earlier keep. A file the credential denylist refuses, a path the guard protects (`.nodwyn/`, `.git/`, `Makefile`, `scripts/` …), a link and a directory are neither kept nor touched, and the index is not. The evidence loop calls it after a passing run in a turn nobody reads; when a hard limit or the progress guard stops the turn, the core puts back what differs from it, from the files kept or from HEAD, never through a link out of the repository, and refuses if HEAD moved. What it replaces is saved under the scratch directory's `kept/discarded` |
| `(run-plan)` | the plan of the run this turn belongs to, as its earlier turns left it, read from the log: `((plan . text) (steps . list) (marks . list) (next . n))`, where `marks` are the latest mark per step, in step order, each `((step . n) (state . s) (note . t))`, and `next` is the first unmarked step, or `#f` when every step is marked. `#f` when this turn is not a run's (its `user.msg` follows no `run.turn`) or no turn of the run has recorded a plan. Only the run's turns count: a chat turn between leaves its plan alone, a resume keeps it, and a run started afresh has none |
| `(unfinished-plan)` | the plan a turn that did not finish left to this one, read from the log, in the shape `run-plan` gives. A turn did not finish when an error, a limit or the person's Stop ended it (its `turn.end` outcome is `error`, `stopped`, `hard_budget`, `spend_limit` or `canceled`), or when nothing ended it; its plan, with every mark made since, goes on through such turns. `#f` when the latest turn that ended finished (outcome `completed`, or none recorded, as in an older log) or no turn since has recorded a plan. `plan-gate` carries it in a chat's turn when a step is unmarked (`plan-left`) |
| `(plan-carry run)` | record `run`'s plan, from `(run-plan)` or `(unfinished-plan)`, as this turn's, decision `carried`, with its marks |
| `(plan-run-request request run)` | the planner's request when a run's plan has every step marked: `request`, the previous plan and how each step went, and what remains to plan in the same shape; `request` alone when `run` is `#f` |
| `(plan-steps [plan])` | the steps of the plan recorded in this turn, as strings: its numbered or bulleted lines, in order, or the whole plan when it is prose (one step); `'()` before a plan is recorded. With `plan`, a plan's text, the steps of that text, read the same way: `plan-write` checks a plan's steps before it records it. The console reads a plan's steps the same way |
| `(record-step n state note)` | record that step `n` (from 1) of this turn's plan is `"done"` or `"skipped"`, with a note saying what shows it, as `plan.step`; a later mark of the same step replaces it, one a carried plan brought included. Returns `((done . k) (of . n) (next . m))`: the steps marked so far, carried ones included, the plan's steps, and the first unmarked one, or `#f`. An error without a plan, for a step the plan does not have, or for another state |
| `plan-write` | the model's tool for a plan of its own, an agent tool answered by `plan-write-answer`: `{"plan"}`, recorded with `record-plan`, decision `model`, so the window shows it under `plan-header` after the call's answer and `plan-step` marks it. The model's thinking comes back to it only as the server reported it (window recipe v14), often a summary or nothing, and a reply that only calls tools keeps nothing else: without a written plan a design is worked out again at every request. Writing it again revises it: a step it keeps word for word keeps its mark and note, at its new number, and a new or reworded step starts unmarked; in a run it becomes the run's plan, and a turn that does not finish leaves it to the next. The working rules ask for it once the model knows what to change. Its description gives the shape, with an example, and it checks it (`plan-problems`): a plan with a step of no known kind, no `Change:`, `Test:` or `Docs:` step, a last step that is not a `Check:`, a `Test:` step that says neither how its test fails on the unchanged code nor that it guards behaviour that already works, or a step of a known kind whose short name — what comes before the first boundary the console folds a step on, the kind included — runs past `plan-name-cap` (80) characters is turned down, unrecorded, with each problem by step number and what to add. After `plan-write-refusals` refusals in a turn the next plan is recorded as written, and so is any after it in that turn, answered `Plan recorded: ` and what it still lacks, so a model that cannot get the shape right is not kept from the work |
| `plan-kinds`, `plan-required-kinds`, `plan-last-kind`, `plan-none-kinds`, `plan-test-reasons`, `plan-name-cap`, `plan-name-boundaries`, `plan-write-refusals` | the shape `plan-write` checks and the planner is asked for (ADR 32, amended 2026-10-03): the kinds a step starts with, the word before its first `:` in any case, each with what a step of it names (`Change`, `Test`, `Docs`, `Check`); the kinds a plan needs a step of (`Change`, `Test`, `Docs`); the kind of its last step (`Check`; `#f` for any); the kinds whose step may say `none` and why instead (`Test`, `Docs`); what a `Test:` step says of the unchanged code, any of them in any case (`fails today`, `passes today`, and the same with `on the unchanged code`); how many characters a step's short name may take, the kind included, before the console cuts its title (80); and the boundaries the console folds a step on, a dash with a space before it or a sentence end or semicolon with a space after (` —`, ` –`, ` --`, `. `, `; `); and the refusals in a turn before a plan is recorded as written (2). A project redefines them in `.nodwyn/init.scm` |
| `(plan-problems steps)` | what is wrong with a plan's steps, from `(plan-steps plan)`: one message per problem, step by step and then for the plan as a whole, `'()` when there is none. The messages are Scheme too (`plan-kind-problem`, `plan-test-problem`, `plan-name-problem`, `plan-missing-problem`, `plan-last-problem`, `plan-refused-message`, `plan-recorded-message`); a project that wants no check defines it to return `'()` |
| `plan-step` | the model's tool for those marks, an agent tool (`define-agent-tool`) answered by `plan-step-answer`: `{"step", "state", "note"}`, answered with what was marked and the next unmarked step. Its description says to mark a step as soon as its work is done, before starting the next — never several together at the end — so whoever follows the task sees the plan's progress move as the work happens (ADR 32, amended 2026-10-04), and that a `Test:` step is done once its test has failed without the change and passes with it, the note naming both runs. The plan header (`plan-header`) and a run's turn prompt ask the model the same. A mark is the model's word, not a proof: the console shows it as such, and the evidence is the transcript |
| `(repository)` | the repository's root, for a prompt to name (the planner's system prompt does) |
| `(workspace)` | the workspace's mounted folders, from the workspace list in the config directory (ADR 22), as `(("mounts" (("path" . P) ("access" . "read")) …))`. Both default system prompts add a section naming the mounted folders, so a plain repository's prompt is unchanged; mounted folders are reached by absolute path, and a read-only one refuses every write. Before `fs-write` or `fs-edit` replaces a file, its previous content is kept in `.nodwyn/versions/` and a `file.version` record names it, unless a git work tree at the root has the file committed and unchanged (ADR 35) |
| `(log-note text)` | add a note to the session log, for the record and the transcript; the model never sees it |

A turn that must plan (`plan-required?`) always drafts one, scored if there
is a judge, and fails if the plan is empty. The core keeps one invariant
whatever the Scheme does: until a non-empty plan is recorded, nothing
irreversible runs, and a turn that records none fails. What it enforces is
that a plan was recorded, not who wrote it: the default `nodwyn-turn` has the
control model plan, and a project's own `nodwyn-turn` may record a plan of its own
with `record-plan`, as trusted project code.

**Judged approval.** When the judge answers a session's confirms
(`nodwyn run --judge`), the decision is Go's (`internal/core/judge.go`): it
shows the judge the task, the turn's plan and the call, asks `judge-choice`
whether to allow it, and allows it only at `judge.approve_threshold` (config)
or above. Scheme cannot approve anything; it may refuse more:

| | |
|---|---|
| `(approve-refuse? call)` | optional, defined by a project: asked about a call the judge allowed, with `call` an alist of `task`, `plan`, `prim`, `reasons`, `path`, `argv`, `args` and `detail`; true declines it, and so does an error. Never asked about a call the judge refused |

The model marks each of the plan's steps as its work is done, before the next
(`plan-step`), and the
console's plan panel shows which are done, which were skipped and which is
next. A strategy that does not use `run-calls` (where agent tools are
answered) leaves `plan-step` unanswered; its own handler may call
`record-step`.

### 9.6 Working habits

Beside the prompts' `working-rules`, the default loop carries the habits that
held up on a coding benchmark (ADR 32), as user code in `internal/loop/`,
loaded after the files above in this order. Each wraps `run-calls` (and
`fold-result`, `sub-agent`, `model-turn` or `nodwyn-turn` where it says), so a project's
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
  that never ran a test is folded to one line once a later one does (a
  command ran tests when it exited 0 or its output says so in a runner's
  words, `hygiene-ran-words`: pytest's, Go's `ok`, `--- PASS`/`--- FAIL`
  and `=== RUN`, mocha's, jest's, node's, cargo's; a Go package that did
  not build ran none); a read
  of a file edited since is folded to its outline after `hygiene-stale-age`
  rounds; a read `hygiene-age` rounds old is folded to its outline; neither
  folds a read of a file edited in the last `hygiene-editing-rounds` (4)
  rounds, the file being worked on. Reads
  fold in batches of at least `hygiene-min-bytes`, and only when the fold
  pays for the prompt cache it costs: a fold makes a caching server read
  everything after the first folded read again, at the full input price. So
  a batch folds when it is at least as large as the rest of the window after
  it (`(context-tokens result)`), when the provider's cache is not serving the
  context anyway (it read less than `hygiene-cache-share`, a quarter, of the
  last `hygiene-cache-requests` prompts; a provider that does not say, such
  as Ollama, counts as serving it), or when the context is past
  `hygiene-pressure` (half) of the high water. The log note of each fold says
  which. It wraps `sub-agent`, to watch the main context only, and
  `model-turn`, to read the cache counts of its requests.
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

### 9.7 Per-model tuning

A round is one reply's calls, and models batch calls into rounds very
differently (in the local session logs, `gpt-6.1-sol` made 2.75 calls a round
and `glm-5.3-flash` 1.22), so a limit counted in rounds is a different amount
of work for each. A knob that depends on the model is a number, which holds
for every model, or a table: rows of `(family . value)` and a
`(default . value)` row.

```scheme
(define plan-explore-rounds
  '(("muse-spark" . 16) ("glm" . 16) ("qwen" . 16) ("deepseek" . 16) ("gpt-6-luna" . 16) (default . 12)))
(define reading-reminder-after '((default . 12)))
```

| | |
|---|---|
| `(tuned knob)` | the knob's value for the control model as it is now: a number as it is; from a table, the first row whose family is the front of the model's id, else the default row (`#f` when there is none). Switching the control model in a chat changes the value from the next use |
| `(model-family-id)` | the control model's id as its provider spells it, without any `vendor/` (`z-ai/glm-5.3-flash` is `glm-5.3-flash`), which is what a family is the front of: `"qwen"` fits `qwen3.8-27b` and `qwen/qwen3.8-27b` alike. The model is asked about only when a family row could use it |

A narrower family goes before a broader one, since the first row that fits
wins. The knobs that are tables are `plan-explore-rounds` (§9.5) and
`reading-reminder-after` (§9.6); a project that redefines either as a number
keeps that number for every model, and `0` still turns it off. The rows
shipped come from the logs and say so in their comments (ADR 32, amended
2026-10-05): `gpt-6.1-sol` and `claude-sonnet` have none and take the
default, and the reminder's table has only a default row.

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

### Project authority (ADR 45)

The repository supplies init.scm and allow.toml; their presence is not a grant.
Use `nodwyn trust --repo PATH` and accept its displayed revision before ordinary
sessions load them. The user-owned registry is outside the project. The exact
checked init bytes run with user origin and the guard still applies; standing
approvals can permit actions during startup. Review transitive Scheme and scripts
too, since their contents are not part of the two-file revision. A change to either
authority file, including creating or removing it, needs review again. See
[Security boundaries](security.md) for revocation, worktrees and HTTP choices.
