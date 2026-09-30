# nodwyn manual

Two parts: how to **read the code**, and how to **use** the bootstrap-era
binary. For *why* things are shaped this way read [SPEC.md](../SPEC.md);
for *what comes next* read [plan.md](../plan.md); for individual decisions
read [docs/adr/](adr/).

---

## Part 1 — Reading the code

### The one-paragraph model

A **front-end** sends the user's text to a **core session**, which calls the
**loop**: a Scheme procedure, `agent-turn`, over a few Go primitives. They
build a context **window** from the session **log**, send it to the
control **model** (`[control]` in `config.toml`) and display the redacted answer after
generation. When the model asks for a tool, the loop turns the request into a
Scheme application `(tool-name arg …)` and evaluates it in the embedded
**Scheme** interpreter.
Every primitive application — from the model, the user, or a `.scm` file —
passes through one Go function, `applyPrim`, where the **guard** decides
allow / confirm / deny before the **host primitive** runs. Everything is
appended to the log first; the window, the transcript and the front-end's
view are all derived from it.

### Follow one tool call through the code

This is the spine. Read these in order and you have read the system.

| Step | Where | What to notice |
|---|---|---|
| 1 | [internal/frontend/plain/chat.go](../internal/frontend/plain/chat.go) `Run` | reads a line, calls `Session.Send`, then only renders events until `status idle` |
| 2 | [internal/core/session.go](../internal/core/session.go) `Send` | one goroutine per turn; [events.go](../internal/core/events.go) records and emits; [confirm.go](../internal/core/confirm.go) handles a prompt plus a wait on `Answer` |
| 3 | [internal/loop/strategy.scm](../internal/loop/strategy.scm) `agent-turn`, [prims.go](../internal/loop/prims.go) `modelTurn`, `runTools` | the loop is Scheme; its primitives rebuild the window from `EventsSoFar()` every request ([model.go](../internal/loop/model.go) `ask`) and give each model call exactly one result ([turn.go](../internal/loop/turn.go) `claim`, [tools.go](../internal/loop/tools.go) `runTool`) |
| 4 | [internal/window/window.go](../internal/window/window.go) `Build` | elides oldest tool results *in place* so the request prefix never changes |
| 5 | [internal/llm/ollama.go](../internal/llm/ollama.go) `Chat` | native `/api/chat` streaming; thinking separated; tool-call args stay JSON objects |
| 6 | [internal/loop/tools.go](../internal/loop/tools.go) `apply` | `Prim.ArgsFromJSON` orders named JSON args into positional Scheme values; `WithOrigin(ctx, OriginModel)` |
| 7 | [internal/scheme/prim.go](../internal/scheme/prim.go) `applyPrim` | **the single Scheme→Go exit**: arity check, `gate.Before`, then the handler |
| 8 | [internal/guard/guard.go](../internal/guard/guard.go) `Before` | checkers run in order; strictest verdict wins; a standing approval outranks confirm, never deny; every host-reaching decision is logged |
| 9 | [internal/guard/checkers.go](../internal/guard/checkers.go), [allow.go](../internal/guard/allow.go) | the bootstrap envelope: `MetaPolicy`, `RepoPin`, `PathDenylist`, `ArgvTrigger`, `CommandAllow` |
| 10 | [internal/prim/fs.go](../internal/prim/fs.go), [shell.go](../internal/prim/shell.go) | the host primitives; `Meta.Params[].Role` is what the guard reads; `Preview` renders the diff for the prompt |
| 11 | [internal/log/log.go](../internal/log/log.go), [payloads.go](../internal/log/payloads.go) | the source of truth: one JSON line per event, fsync per write, mandatory redaction after the optional `Filter` hook |

### Package map

| Package | Owns | Start with |
|---|---|---|
| `internal/scheme` | the language: values, reader, eval/apply, builtins, primitive table | [docs/scheme.md](scheme.md) then `interp.go` `Eval` |
| `internal/guard` | the one decision point and its checkers | `guard.go` `Before` |
| `internal/prim` | host primitives and the diff | `prim.go` `Register`, `Checkers` |
| `internal/llm` | the provider seam and the Ollama client | `llm.go` `Provider` |
| `internal/window` | the derived context | `Build` |
| `internal/loop` | the loop's primitives and default strategy | the package comment's reading order, starting at `loop.go` `Turn` |
| `internal/core` | the UI-agnostic session façade | `construct.go` `New`, `session.go` `Send`, `confirm.go` `confirm` |
| `internal/keychain` | encrypted credentials, opaque handles and injection | `keychain.go` |
| `internal/binding` | binding loading and authorization lookup | `binding.go` `Lookup`; Scheme call decoding lives in `guard/egress_facts.go` |
| `internal/redact`, `internal/detect/l1` | text redaction and credential-shape detection | `redact.go`, `l1.go` |
| `internal/event` | what a front-end sees | `event.go` |
| `internal/log` | the append-only log | `log.go` `Append`, `Replay` |
| `internal/config` | XDG paths, config.toml and allow.toml | `Default`, `Validate` |
| `internal/frontend/plain` | line mode and replay | `chat.go` `show` |
| `internal/frontend/rpc` | local daemon sessions, turns and events | `server.go`, `sessions.go`, `events.go` |
| `cmd/nodwyn` | flags → core → front-end; nothing else | `main.go` |
| `dev/`, `.nodwyn/` | this repository's own tools and standing approvals | `dev/go.scm` |
| `test/exfil` | the exfiltration scenario suite | `exfil_test.go`, `scenarios/` |
| `test/fakes/ollama` | scriptable fake of the model server | `ollama.go` |

### The invariants and the tests that hold them

Read a test when you want to know what a claim actually means.

| Claim (SPEC) | Enforced by |
|---|---|
| The log is the single source of truth | `nodwyn replay` reproduces a transcript from the file alone; `window.Build` takes only events; [log_test.go](../internal/log/log_test.go) round-trips and repairs |
| The interpreter is the only Scheme→Go surface; the guard sits on it | `applyPrim` is the only caller of `Prim.Handler`; [scheme_test.go](../internal/scheme/scheme_test.go) `TestGateIsTheSingleExit` records every crossing including through `map`/`apply`/`sort` |
| Loading `.scm` cannot change evaluation | `TestKernelFixed`: fingerprint unchanged, only the primitive table grew |
| Proper tail calls | `TestTailCalls`: 200k iterations in nine tail positions under an 8 MB stack cap |
| Every primitive is documented | `TestDocCoverage` (builtins) and `TestSessionPrimitivesAreDocumented` (host and loop primitives) fail for any primitive missing from `docs/scheme.md` |
| A loop strategy cannot skip the invariants | [strategy_test.go](../internal/core/strategy_test.go): fabricated or rewritten calls refused, unanswered calls answered, stops uncatchable, provenance kept |
| Agent behaviours are user code, not core features | [userland_test.go](../internal/core/userland_test.go): each behaviour is written as a user's `init.scm`; one that needs Go marks a gap in the core |
| A handle can never become text | `TestHandlePrintsOpaque` |
| The core knows nothing about terminals | [scripts/lint-boundaries.sh](../scripts/lint-boundaries.sh) in `make lint` |
| No path exists for the canary to leak | [test/exfil](../test/exfil/): each scenario asserts the decision *and* greps the canary out of log, output and result |
| Request prefix is byte-stable | [session_test.go](../internal/core/session_test.go) `TestTurnWithToolCall` compares request N's messages with request N+1's prefix |

### Reading a session log

```
bin/nodwyn sessions                      # ids, oldest first
bin/nodwyn replay <id>                   # transcript
bin/nodwyn replay <id> --show-thinking   # with the model's thinking
jq -c '[.seq,.kind]' ~/.local/share/nodwyn/sessions/<id>.jsonl
```

Event kinds are listed in [internal/log/log.go](../internal/log/log.go).
`guard.decision` lines carry `origin` (user, model, or harness for the loop
strategy and plan gate), `decision`, `checker`,
`reason` and, for confirms, `answer`. An `llm.req` has `elided`: the seqs
whose content was replaced in the window.

### Where the next phases plug in

| Phase | Seam already in place |
|---|---|
| P3 credentials | `scheme.KHandle`; `Param.Role = "credential"`; `config.Bindings`; `MetaPolicy` denies `Egress` today |
| P4 L1 | a checker over `Role: payload`/`argv` text; `log.Writer.Filter` |
| P5 Jev + plan gate | `log.KindGatePlan`; `ArgvTrigger` becomes the hard-trigger list |
| P6 taint | `Value.Taint` (reserved), `Call.Origin` |
| P7 redaction | `Session.Record` sanitizes payloads; the log writer enforces redaction; `window.Build` derives context from written events |
| P9 TUI | `internal/frontend/tui` over `core.Session`, no core change |
| P10 memory | replace `internal/window`; `log.KindSummary` |

---

## Part 2 — Using nodwyn

### Install

```bash
make build                       # bin/nodwyn from the working tree
make promote TAG=v0.1-bootstrap  # accept + selfcheck, then ~/.local/bin/nodwyn-stable (ADR 12/13)
```

Use `nodwyn-stable` for real work and `bin/nodwyn` only to test a change to
nodwyn itself. Never run the binary you are editing (ADR 12).

Requires Go 1.27, `staticcheck`, and a
model to talk to: the `[control]` table in `config.toml` (`bin/nodwyn paths`
shows where it lives; everything else has defaults in
[internal/config/config.go](../internal/config/config.go)). There is no
built-in model; without one, nodwyn prints the table to add. See "Choosing
models".

### A session

```bash
cd ~/src/your-repo
nodwyn-stable                       # pins every path to this directory
```

```
nodwyn session 20260921T143846Z-beba937b
repo  /Users/you/src/your-repo
tools fs-edit fs-list fs-read fs-write index-grep shell-exec
:h for commands

you> read main.go and explain the startup sequence
```

Commands at the `you>` prompt:

| | |
|---|---|
| `:q` | quit (writes `session.end`) |
| `:log [n]` | last n log events |
| `:tools` | tools offered to the model |
| `:allow` | standing command approvals in effect |
| `:eval EXPR` | evaluate Scheme yourself (user origin) |
| `:thinking` | toggle showing the model's redacted thinking |
| Ctrl-C | abort the running turn; at the prompt, exit |

Flags: `--repo DIR`, `--model`, `--url`, `--show-thinking`, `--no-confirm`
(every confirm is a no — for scripts and CI), `--yolo` (every ordinary
confirm is a yes — for work nobody is watching), `--no-keychain`,
`--max-tool-calls N` (hard call override), `--budget-wall 2h`,
`--budget-tokens N`, and `--budget-actions N`. The `[budget]` settings and
checkpoint behavior are described in the README. Defaults are 50/500 calls,
20m/4h wall time, 1M/10M reported model tokens and 25/200 irreversible host
attempts (soft/hard). Legacy `loop.max_tool_calls` remains an explicit override.
At a checkpoint, answer `y` to double the soft limit, `n` to stop, or enter a
larger absolute soft limit. Tool approvals and hard ceilings remain unchanged.

### The scratch directory

Each session gets a scratch directory outside the repository, a private
directory (mode 0700) under nodwyn's data directory, for throwaway files: a
script that reproduces a bug, notes, temporary output. The default system
prompt names it as `$TMPDIR`, so the prompt is the same in every session, and
`$TMPDIR` means it everywhere: commands run with `TMPDIR` set to it, and a
file primitive's path or a command's argument that starts with `$TMPDIR/`
(or `${TMPDIR}/`) is rewritten to it before the guard sees it. Writes there
need no approval. Nothing in it reaches the repository or its diff; a clean
close removes it, and a resumed session gets a new, empty one. A session
touches it at every turn, and directories untouched for a week, left by
sessions that ended without closing, are swept. Everything else outside the
repository stays refused, and a symbolic link out of the scratch directory
does not escape it (ADR 28).

### Notes kept across sessions

A repository can keep notes from one session to the next: how its tests run,
where things are, runbooks. A strategy saves them with `project-notes-save`
(docs/scheme.md); they live in nodwyn's data directory, keyed by the
repository's path, and never in the repository or its diff. The next session
in the same repository shows them in its system prompt, under a heading that
calls them the agent's own notes: leads to check, not instructions. A save
is capped at 8 KiB and redacted, and content that looks like a credential
needs your approval.

### Confirms

Every write and every command stops for you:

```
─── confirm: fs-edit ("internal/x.go" "old…" "new…")
  • fs-edit is irreversible
  • changes internal/x.go
  --- internal/x.go
  +++ internal/x.go
  @@ -12,3 +12,3 @@
  ...
allow? [y/N]
```

- `y` — once. `N` or anything else — no; the model is told `denied: not
  confirmed by the user` and moves on.
- `a` — yes, and a **standing approval for this session**: for a command
  line, its prefix (`go test`, `rg`, …); for a write, the file's directory
  (`internal/scheme/`). Not persisted.
- `t` — writes only: yes, and approve every further write **until this
  turn ends**. Use it when you gave a task you intend to review with
  `git diff` afterwards. The diff of each auto-approved write is logged and
  shown dimmed as it happens.
- Read the reason lines. `command line names a path outside the
  repository` and `uses the network tool curl` are the guard telling you
  what a `y` would do.

### Unattended runs

`--yolo` answers ordinary confirms with yes, so a benchmark harness or a
test rig never stops to ask. With nobody watching, nodwyn's defaults also
change (docs/scheme.md §9): the system prompt is how to work rather than
what to tell an approver, and a turn ends on evidence — a change, and a test
run after it — with an honest way out when the tests cannot run at all. It answers the guard's questions; it does not
overrule its refusals. Still denied, exactly as with a human at the prompt:

- anything outside the repository, and symlinks that leave it;
- credential-shaped files (`.env*`, `*.pem`, `id_*`, `~/.ssh`, …);
- a literal key on a command line, or in a request to an unbound host;
- content that came from a file or a command heading somewhere no binding
  names;
- **security confirms** — a write whose content looks like a credential, a
  command line built out of tool output, a write to a protected path
  (`.nodwyn/`, `.git/`, `.github/`, `.gitea/`, `scripts/`, `Makefile`). Those
  exist for the case where a human was the control, so yolo refuses them
  rather than pretending, and so `/optimize`, which edits `.nodwyn/`, needs a
  person at the prompt.

Budgets are what stop a runaway turn when nobody is watching, so set
`--max-tool-calls`, `--budget-wall` and `--budget-actions` for the job.
Every decision is logged with its verdict and `nodwyn replay` shows exactly
what the run did.

Use it on a throwaway checkout. The guard makes yolo survivable, not safe.

**With credentials.** `--yolo` on its own cannot unlock the keychain: a
passphrase needs a terminal (plan §3.3), so an unattended process with a
keychain on disk starts without one and says so. When the run is *supposed*
to have keys — measuring how the model uses a bound tool, for instance —
put the keychain in the daemon and submit runs to it:

```bash
nodwyn daemon                      # unlock once, interactively, and leave it
nodwyn run start --repo /path/to/checkout --task task.md   --max-turns 40 --budget-wall 20m --yolo
```

The daemon holds the keychain and the binding table; every session it opens
inherits them, so the run has exactly the credentials you bound and no way
to acquire others. `--yolo` there is a property of **that run**, not of the
daemon: a daemon left running never auto-approves a session someone opens
later from the browser. `nodwyn run status|stop` follow the run, and its
turns are bounded by `--max-turns` and `--budget-wall` rather than by
anyone watching.

### Standing approvals

Commands you never want to be asked about go in one of two files:

```toml
# ~/.config/nodwyn/config.toml
[policy]
allow_commands = ["go test", "go build", "git status", "git diff", "rg"]
```

```toml
# <repo>/.nodwyn/allow.toml   (tracked with the repository)
commands = ["make lint", "make test"]
writes   = ["internal/", "docs/"]     # write prefixes that never prompt
```

`.nodwyn/`, `.git/`, `.github/`, `.gitea/`, `scripts/` and `Makefile` always
prompt, whatever the list says: they hold code that later runs with your
authority (`init.scm`, git hooks, CI, build scripts). Each write there is a
security confirm, so no standing or turn approval waives it and an
unattended run refuses it. So does a write whose content looks like a
credential (L1): that confirm names the class.

Prefixes match argv tokens: `"go test"` matches `go test ./...` and
`/usr/local/go/bin/go test`, not `go build`. Denies still apply (the repo
pin, the credential denylist); only the confirm is waived, and every use is
logged as `allow` with reason `standing approval: go test`. Keep them
read-only: nothing that commits, pushes, deletes or reaches the network.

### The judge (Jev)

The plan gate runs once a `jev-key` credential exists and is bound:

```bash
nodwyn keychain init                       # once
nodwyn-stable                              # then, at the prompt:
:eval (credential-register "jev-key")    # masked input
```

```toml
# ~/.config/nodwyn/bindings.toml
[[bind]]
class = "jev-key"
dest = "api.typesafe.ai"
positions = ["header:Authorization:Bearer"]
methods = ["POST"]
```

Before each user message the default strategy's plan gate
(`internal/loop/plan.scm`, [docs/scheme.md](scheme.md) §9.5) asks Jev
whether the message needs a plan; if so the model drafts one, Jev scores it
against the ask (up to three revisions), and the plan goes into the task
block the model sees. Its thresholds and prompts are Scheme definitions a
project may redefine.
Every Jev call is a guarded egress: bound credential, L1 redaction of
what leaves, a `jev.req` log line with size, hash and the answer. Set
`jev.content = "none"` in `config.toml` to turn it off. Without a
`jev-key` credential nothing is sent and the loop runs without a gate.

`register-check` in `.nodwyn/init.scm` adds your own tightening-only guard
checkers (deny or confirm, never allow); see `docs/scheme.md` §7.

### Giving the model tools of its own

`<repo>/.nodwyn/init.scm` is loaded at start (after the prelude). Anything it
registers becomes a tool the model can call; nothing it does can bypass the
guard. This repository's [dev/go.scm](../dev/go.scm) is the pattern:

```scheme
(define (run cmd args)
  (let ((r (shell-exec cmd args)))
    (format "exit ~a~%~a~a" (assoc-ref r "status") (assoc-ref r "stdout") (assoc-ref r "stderr"))))

(register-tool "go-test"
  (lambda (pkg) (run "go" (list "test" "-race" pkg)))
  '((doc . "Run the Go tests for a package pattern such as ./internal/log or ./... .")
    (params . (("pkg" "string" "package pattern")))))
```

Rules that matter: `doc` and `params` are required; declare list parameters
rather than variadic ones (the model's tool schema cannot express rest
arguments); the inner `shell-exec` is still irreversible, so pair the tool
with a standing approval. The language is in [docs/scheme.md](scheme.md);
try things in `nodwyn repl` or with `:eval`.

### Changing the loop

A turn is the Scheme procedure `agent-turn`; redefine it in
`.nodwyn/init.scm` to change how turns go. This one runs a batch of tool calls
in order but stops at the first failure, so the model re-plans from the
error instead of running calls that assumed success:

```scheme
(define (run-until-failure calls)
  (unless (null? calls)
    (let ((result (car (run-tools (list (car calls))))))
      (if (assoc-ref result 'ok)
          (run-until-failure (cdr calls))
          (skip-tools (cdr calls) "an earlier call in this batch failed")))))

(define (agent-turn request)
  (let loop ((reply (model-turn)))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (unless (null? calls)
        (run-until-failure calls)
        (loop (model-turn))))))
```

A strategy decides which calls run, what the model is told about the ones
that don't, when to ask the model again, and — with `add-message` — what to
say when the model stops with work left. It can reach a keyed judge or model
API with `(credential class)` and `http-request`. It cannot run a call the
model did not make, loosen the guard, or keep a turn going past a budget or a
progress stop ([docs/scheme.md](scheme.md) §9). Every prompt is Scheme
(§9.1), and so is the plan gate with its thresholds (§9.5).

nodwyn can learn how to work in a repository from its own history: send
`/optimize` (or `/optimize 5`, or `/optimize` with session ids, optionally
followed by what to focus on) and a sub-agent reads this repository's past
sessions and edits `.nodwyn/init.scm`, asking before every write; the result
is reloaded for the next turn ([docs/scheme.md](scheme.md) §9.4).

[internal/core/userland_test.go](../internal/core/userland_test.go) holds
larger examples, each a behaviour written as a user would write it:

- a plan cycle: the model must submit a plan, a keyed oracle approves it, and
  the strategy keeps the model on it until every step is done;
- routing: the control model plans, a rule picks the model that implements,
  which gets only the task and the plan (`narrow-context`); if the result
  diverges, a stronger model gets one retry, and the control model reports.

### Choosing models

The control model (`[control]`) answers `(model-turn)`. Any other model a
strategy may use is an entry in `config.toml`, asked by name with
`(model-turn "NAME")`:

```toml
[models.coder]
provider = "openai"                 # any chat-completions server
url = "https://api.openai.com/v1"   # including the version
model = "gpt-5.1-codex"
credential = "openai-key"           # keychain class; omit for a local server

[models.local]
provider = "ollama"
url = "http://localhost:11434"
model = "qwen3:8b"
num_ctx = 81920
```

A keyed model needs its key stored (`(credential-register "openai-key")`)
and bound to the model's host, like any credential:

```toml
# bindings.toml
[[bind]]
class = "openai-key"
dest = "api.openai.com"
positions = ["header:Authorization:Bearer"]
methods = ["POST"]
```

The binding is checked on every request and logged as `egress.check`; an
unbound key is never sent. Configuring a model is consenting that what a
strategy sends it leaves the machine: with `narrow-context` that can be just
a task and a plan, but a strategy that asks a model over the whole
conversation sends the whole conversation. Only you configure models; the
model cannot.

`[control]` has the same shape and is required: it is the model `(model-turn)`
asks, and a keyed chat-completions server works there too.

```toml
[control]
provider = "ollama"               # the default; or "openai"
url = "http://localhost:11434"
model = "qwen3:8b"
num_ctx = 81920                   # at least window.high_water + window.output_reserve
```

`url` and `model` are required; nodwyn has no built-in control model. Unset,
`num_ctx` asks an ollama model for exactly the window's size
(`window.high_water + window.output_reserve`, 77824 by default) rather than
leaving Ollama at its own, usually smaller, default, which would cut the
prompt silently; a chat-completions server decides its own. Set, the window
must fit in it.

`window.output_reserve` (16384 by default) is also the cap on each reply,
thinking included: ollama gets it as `num_predict`, a chat-completions server
as `max_completion_tokens`, so a prompt at the window's high water and its
reply always fit the context. A model may set its own cap with `max_output`,
for a remote reasoning model whose thinking needs more room (keep an ollama
model's at most `output_reserve`, or raise its `num_ctx` to match); a
chat-completions server that only knows the older field takes
`output_field = "max_tokens"`:

```toml
[models.reasoner]
provider = "openai"
url = "https://api.example.com/v1"
model = "big-reasoner"
credential = "example-key"
max_output = 65536
output_field = "max_tokens"       # only for servers without max_completion_tokens
```
 A reply that reaches the cap is noted in the
log; the default loop, the planner and sub-agents ask again, telling the
model to be briefer, at most twice (`ask-model`, docs/scheme.md §9). On the
SWE-bench dev set, 99.8% of Qwen replies stayed under 8192 tokens and the
longest was about 17k, a thinking burst.

A session opens without it, to replay or evaluate; its first model request
fails with that table, and is not retried. A key that is missing or not
bound fails the same way.

### Ingress redaction and the remaining boundaries

P7 uses the shared `internal/redact` controller for user text, tool results,
planner requests, log payloads, and frontend events. Recognized credentials
become `[REDACTED <class>]`; `redact.event` records the source event sequence,
class, and offsets within the decoded string, never the removed value.
JSON strings and nested tool arguments are decoded before scanning. The
mandatory log pass runs after custom filters. Egress L1 allowlists do not
turn off ingress redaction.

The log is the context ingress: `Session.Record` appends the writer's returned
event to the live history, and `window.Build` derives messages from that history.
The planner's requests are model turns like any other, in a narrowed context,
so they are built from the same redacted history. CI checks the history writer.

Model content and thinking are buffered until the response completes, then
redacted and displayed. Scanning separate token chunks would miss split keys.
User-origin Scheme still evaluates the original source; `:eval` results are
redacted for display. The standalone `eval` and `repl` commands are interpreter
tools, outside the conversational context boundary.

Paths remain pinned; credential-shaped files stay denied; child environments
remain allowlisted; shell commands still require approval. Recognized secret
shapes are covered, not arbitrary passwords, encodings, or confidential prose.
Unknown entropy remains a residual for P8's optional L3 classifier. Older
stable binaries keep the bootstrap operator restrictions until P7 is promoted.

### Building nodwyn with nodwyn

1. `nodwyn-stable` in this repository; `:tools` should list `go-test`,
   `git-diff`, `grep-repo`, `read-adr`, … from `dev/`.
2. Give it small, explicit tasks: name the file, the pattern to follow, the
   test to add, and ask it to run `go-test` on the package. A 27B model does
   well with a paragraph and badly with a page (plan §3.4).
3. Review every diff at the confirm. `fs-edit` diffs are small; an
   `fs-write` of a whole existing file deserves suspicion.
4. When it gets in the way, hand-edit, and add one line to
   [docs/friction.md](friction.md). That file decides what gets built next.
5. `make accept` before committing; you commit — there is no commit tool.
6. `make promote TAG=v0.N-<phase>` when a phase's acceptance is green.

### When something is wrong

| Symptom | Look at |
|---|---|
| `no control model is configured` | add the `[control]` table it prints to `config.toml` ("Choosing models") |
| `control model … unreachable` | is the `[control]` URL right and the server up? For Ollama, `curl <url>/api/version` |
| first turn takes 10–60 s | another client holds the model server's slot, or the prefix is cold; later turns are sub-second |
| every request takes 30–50 s | the prefix cache is missing: check `:log` for `llm.req` — a trim (elided count jumped) or a new user turn re-prefills once; if it is every request, another client is evicting the server's slot between your requests (a single-slot Ollama, for example) |
| the model loops on a denied call | the progress guard stops repeated denials; inspect the log before continuing. Budgets also bound each turn, and `--max-tool-calls N` sets a hard call ceiling |
| `hard stop: too many tool calls` | the turn ended; the log has everything; rephrase smaller |
| a tool call errored | `:log 20` — the `tool.result` line has the exact error the model saw |
| a confirm never appeared but a file changed | `:allow` — a standing approval covered it; the `guard.decision` line names which |

## Local daemon

`nodwyn daemon` starts the U2 session server in the foreground. Use
`nodwyn daemon --no-keychain` to skip credential-store startup, and
`--socket /absolute/private/directory/sock` to override its socket path.
The protocol supports session lifecycle, send/abort, subscriptions and paged
log catch-up. Approval replies remain unimplemented; turns waiting for one can
be aborted. See [the protocol guide](protocol.md) for the handshake, methods,
permissions and reconnect semantics.
