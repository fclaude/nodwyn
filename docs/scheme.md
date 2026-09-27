# nibb Scheme — language reference

nibb embeds a small Scheme for **orchestration only**. Scheme code decides
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
| `(do ((v init [step]) …) (test [e …]) cmd …)` | loop: check `test`; on false evaluate the `step`s (in parallel), rebind, evaluate the `cmd`s, repeat. Value is the test clause's last `e` (void if none). A Go loop — constant stack |
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

A primitive the guard denies raises `denied: <reason>`. Denials and
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

### Host primitives (registered by the session, not by `nibb repl`)

| | |
|---|---|
| `(fs-read path)` `(fs-write path content)` `(fs-edit path old new)` `(fs-list path)` | repository-pinned file access; writes and edits are irreversible (confirm, diff preview) |
| `(index-grep path pattern)` | **P10.** Search a repository file or directory with a ripgrep pattern (not command-line options). Returns matches (path, line, text), files searched and skipped, and a truncated flag; at most 128 files and 100 matching lines; protected, binary and oversized files and symlinks are skipped. Needs `rg` on `PATH` |
| `(index-symbols path)` | **P10.** Outline the named definitions in one Go, JavaScript or Python file with bundled Tree-sitter grammars: name, kind, line, end line (at most 100), parse errors. Syntax, not types |
| `(shell-exec cmd args)` | run a program with an argument **list** in the repository root, scrubbed environment; irreversible; returns `(("status" . n) ("stdout" . s) ("stderr" . s))` |
| `(http-request cred url method position headers body)` | **P3.** Send a request with the credential handle injected at `position` (`header:NAME`, `query:NAME`, `body`). Egress: the guard checks the binding table for (class, host, method, position) first. Not offered to the model (a handle cannot come from JSON); reach it through a user-registered tool. Returns `(("status" . n) ("body" . s))` |
| `(policy-read name)` | User-only, hidden from the model catalog. Read configured `bindings` or repository `approvals`, returning content, revision and existence; bounded to 64 KiB and rejects recognized secrets. No arbitrary path. |
| `(policy-write name revision content)` | User-only, hidden from the model catalog. Validate TOML and atomically replace that fixed policy after a one-time diff confirmation and revision check. Binding changes reload before egress; approval-file changes affect new/resumed sessions. |
| `(credential-register class)` | **P3.** Masked input through the front-end, stored in the keychain, returns `#<cred:class:id>`. User origin only; never a model tool |
| `(credential class)` | The handle of a key already in the keychain, or `#f` if there is none (or no keychain is open). This is how Scheme reaches a keyed service — `(jev-noul (credential "jev-key") …)`, or `http-request` to a judge or a model API — without asking for the key again. Looking a handle up grants nothing: every use is checked against the binding table. Refused to model-originated code |
| `(jev-noul cred state instructions)` | **P5.** Ask the judge a yes/no question about a text; returns the probability of yes (0..1). Egress to the configured Jev host; `state` and `instructions` are redacted (L1) before they leave; logged as `jev.req` |
| `(jev-score cred state instructions levels)` | **P5.** Rate a text on an ordered rubric of 2–10 level strings; returns `(("score" . 0..1) ("raw" . n) ("levels" . k) ("confidence" . c))` |
| `(jev-choice cred state instructions options)` | **P5.** Pick one of the options (an alist name → description); returns `(("choice" . name) ("confidence" . c) ("probabilities" . ((name . p) …)))` |

### Provenance (L2)

Every value carries **provenance tags**: where it came from, not what it
contains. A host primitive tags its result (`fs-read`, `fs-list` and the
index primitives mint `external:file`, `shell-exec` mints `external:shell`,
`http-request` and the Jev primitives mint `external:http`), a credential
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

After the prelude, nibb loads `./.nibb/init.scm` if present (ADR 14).

## 7. Primitives and the guard

The **primitive table** maps a name to a Go handler plus metadata. It is the
only surface through which Scheme reaches the host (SPEC invariant 4).

Every primitive declares:

| Field | Meaning |
|---|---|
| `doc` | one line, shown to the model as the tool description |
| `params` | ordered list of `(name type doc)`; `type` is one of `string integer number boolean list alist vector any` |
| `variadic` | extra arguments allowed after the declared ones |
| `egress` | may send bytes off-machine |
| `irreversible` | cannot be undone by the harness |
| `host` | touches the OS (filesystem, processes, network) |
| `origin` | `builtin` or `scheme` |
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
  (lambda (pkg) (shell-exec "go" "test" "-race" pkg))
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
- `predicate` is a procedure `(prim-name args) → boolean`. When it
  returns `#t` the checker returns its fixed verdict; otherwise it has
  no opinion (allow). A predicate that raises an error fails closed
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
calls `(agent-turn request)` with the recorded (redacted) text. The default is
installed before `.nibb/init.scm` loads, so a project can redefine it:

```scheme
(define (agent-turn request)
  (let loop ((reply (model-turn)))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (unless (null? calls)
        (run-tools calls)
        (maybe-compact request reply)
        (loop (model-turn))))))
```

| | |
|---|---|
| `(model-turn)` `(model-turn name)` | Ask a model once: the control model, or the configured model `[models.NAME]` (manual, "Choosing models"). Returns an alist: `content`, `thinking`, `done` (why it stopped), `tool-calls`, each call an alist of `id`, `name` and `args` (the model's JSON text), and `prompt-tokens` and `output-tokens`, the server's counts for this request (0 when it gives none). The window is built from the log, so the model sees what the strategy's earlier calls recorded; each request is recorded, with the model it went to, for `nibb replay --window`. |
| `(run-tools calls)` | Run calls in order, each through the guard. Returns an alist per call: `id`, `name`, `ok`, `value` (the text the model reads, carrying the tool's provenance), `error`, `denied`. |
| `(skip-tools calls reason)` | Answer calls without running them; the model reads `Not executed: reason`. |
| `(add-message text)` | Add text to the conversation, logged as `harness.msg`. The model reads it as a user message at the next `model-turn`; the current task stays the user's. Refused while calls from the latest `model-turn` are unanswered, since a call must be followed by its result. |
| `(narrow-context system)` | Start a fresh context, like Emacs's narrowing: until `widen` or the user's next message, models see `system` as their system prompt and only what comes after this point. Follow it with `add-message` to hand over the task. This is how a model gets a task and a plan without the conversation that led to them. Refused while calls are unanswered. |
| `(widen)` | End a narrowed context. Models see the whole conversation again, including what happened while it was narrowed. A new user message widens too. |
| `(session-system-prompt)` | The system prompt the session started with (§9.1), fixed for its life: what a narrowed context passes to `narrow-context` to keep the model's standing instructions. |

A strategy talks to the model through tools: a tool's description is an
instruction, its result and a skip reason are feedback. `add-message` is for
when there is no call to answer, such as a model that stopped with work left.

Routing work to another model is `narrow-context`, `add-message` and
`(model-turn name)` in a loop: the other model gets a context the strategy
built, and its tool calls run like any model's. Narrowing and routing are
events in the log, so replay rebuilds every request whichever model it went
to.

These primitives are hidden from the model and refused outside a turn and
from model-originated code. The strategy runs with harness origin, so it cannot
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

Every prompt nibb sends is Scheme. The defaults live in
`internal/loop/prompts.scm`, which loads with the default strategy, before
`.nibb/init.scm`; a project redefines any of them there, usually with raw
strings (§1.1):

| | |
|---|---|
| `(system-prompt repo)` | the control model's system prompt |
| `(plan-system-prompt repo)` | the planner's system prompt |
| `(plan-draft-prompt request)` | the planner's request for a plan |
| `(plan-revise-prompt request plan score)` | the request for a better plan after the judge scored it `score` (0..1) |
| `plan-needed-question` | the judge's yes/no question: does this request need a plan? |
| `plan-score-rubric` | the judge's instruction for scoring a plan |
| `plan-score-levels` | the rubric's 2–10 levels, worst first |

```scheme
(define base-system-prompt system-prompt)      ; keep the default to extend it
(define (system-prompt repo)
  (string-append (base-system-prompt repo) "\n"
    """
    Work unattended: nobody approves your calls.
    Run the tests of every module you change before you finish.
    """))
```

nibb reads them once, after `init.scm` has run: the system prompt is then
fixed for the session like the tool list (ADR 9), and the planner keeps the
procedures bound at that moment. A prompt that is not a procedure or a
non-empty string (a list of strings for the levels) stops the session from
starting.

### 9.2 Compaction

A long turn fills the context with tool results the model no longer needs.
nibb elides the oldest ones past `[window] high_water` on its own; compaction
goes further, as user code over the primitives above, in
`internal/loop/compact.scm`. When a reply's `prompt-tokens` passes
`compact-threshold`, `maybe-compact` asks the model for a handover note (the
task, what it found and changed, how to test, what is left), then narrows the
context to the session's system prompt, the request and that note. The
repository keeps the model's changes, and the log keeps the whole
conversation, so replay and audit see everything the note replaced.

| | |
|---|---|
| `compact-threshold` | prompt tokens past which the context is compacted; `#f` (the default) never compacts |
| `compact-attempts` | replies to the note request that may be tool calls (they are skipped) before compaction gives up; after giving up it waits for the context to grow another quarter of the threshold |
| `compact-summary-prompt` | the request for the note (a prompt, §9.1) |
| `(compact-handover-prompt request note)` | what the compacted context starts with |
| `(maybe-compact request reply)` | compact if `reply` passed the threshold; call it once the reply's calls are answered |
| `(compact-context request)` | compact now; `#t` if the turn continues in the narrowed context |

```scheme
(define compact-threshold 40000)    ; in .nibb/init.scm
```

A strategy that redefines `agent-turn` calls `maybe-compact` itself, where the
default does: after `run-tools`, before the next `model-turn`. Compacting
rewrites the start of the context, so the server re-reads the new, short
prompt once instead of reusing its cache (ADR 9); a threshold well below
`high_water` compacts rarely and keeps the prefix stable in between.

## 10. Not in nibb Scheme

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
