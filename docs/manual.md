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
| 7 | [internal/scheme/prim.go](../internal/scheme/prim.go) `applyPrim` | **the single Scheme→Go exit**: arity check, `gate.Before`, the handler, then the result's taint (`taintResult`) |
| 8 | [internal/guard/guard.go](../internal/guard/guard.go) `Before` | checkers run in order; strictest verdict wins; a standing approval outranks confirm, never deny; every host-reaching decision is logged |
| 9 | [internal/guard/checkers.go](../internal/guard/checkers.go), [allow.go](../internal/guard/allow.go) | the bootstrap envelope: `MetaPolicy`, `RepoPin`, `PathDenylist`, `ArgvTrigger`, `CommandAllow` |
| 10 | [internal/prim/fs.go](../internal/prim/fs.go), [shell.go](../internal/prim/shell.go) | the host primitives; `Meta.Params[].Role` is what the guard reads; `Preview` renders the diff for the prompt |
| 11 | [internal/log/log.go](../internal/log/log.go), [payloads.go](../internal/log/payloads.go) | the source of truth: one JSON line per event, fsync per write, mandatory redaction after the optional `Filter` hook |

### Package map

| Package | Owns | Start with |
|---|---|---|
| `internal/scheme` | the language: values, reader, eval/apply, builtins, primitive table | [docs/scheme.md](scheme.md) then `interp.go` `Eval` |
| `internal/guard` | the one decision point and its checkers | `guard.go` `Before` |
| `internal/prim` | host primitives and the diff; the document tools; the judge's primitives and tools | `prim.go` `Register`, `Checkers`; `docs.go`; `jev.go` |
| `internal/llm` | the provider seam, the Ollama and OpenAI-compatible clients, what a request cost | `llm.go` `Provider`; `cost.go` |
| `internal/window` | the derived context | `Build` |
| `internal/loop` | the loop's primitives and default strategy | the package comment's reading order, starting at `loop.go` `Turn` |
| `internal/core` | the UI-agnostic session façade | `construct.go` `New`, `session.go` `Send`, `confirm.go` `confirm` |
| `internal/budget` | one turn's budgets and checkpoints | `budget.go` |
| `internal/extract` | attachments: text by file type, images checked but never read as text | `extract.go`, `image.go` |
| `internal/mcp` | the client for remote MCP servers; it runs no server and starts no process | `client.go` |
| `internal/policyfile` | atomic writes of the policy files the console edits | `update.go` |
| `internal/keychain` | encrypted credentials, opaque handles and injection; OAuth2 token sets, refresh and acquisition | `store.go`, `oauth.go`, `acquire.go` |
| `internal/binding` | binding loading and authorization lookup | `binding.go` `Lookup`; Scheme call decoding lives in `guard/egress_facts.go` |
| `internal/mcp` | the MCP client, over streamable HTTP or a contained server's stdio | `client.go`, `stdio.go`; sessions use it from `core/mcp.go` |
| `internal/container`, `internal/egress` | running a contained MCP server with podman or docker; the allowlisting proxy it reaches the network through (`cmd/nodwyn-egress`) | `server.go` `Start`, `proxy.go` |
| `internal/redact`, `internal/detect/l1` | text redaction and credential-shape detection | `redact.go`, `l1.go` |
| `internal/event` | what a front-end sees | `event.go` |
| `internal/log` | the append-only log | `log.go` `Append`, `Replay` |
| `internal/config` | XDG paths, config.toml and allow.toml | `Default`, `Validate` |
| `internal/frontend/plain` | line mode and replay | `chat.go` `show` |
| `internal/frontend/tui` | the terminal client ([docs/tui.md](tui.md)) | `model.go` |
| `internal/frontend/rpc` | local daemon sessions, turns and events | `server.go`, `sessions.go`, `events.go` |
| `internal/frontend/run` | supervised and unattended runs ([docs/runs.md](runs.md)) | `run.go` `Driver` |
| `internal/frontend/web` | the browser console: its bridge to the daemon and its pages ([docs/browser.md](browser.md)) | `server.go`, `assets/app.js` |
| `cmd/nodwyn` | flags → core → front-end; nothing else | `main.go` |
| `cmd/nodwyn-schema` | generates `docs/protocol.schema.json` | `main.go` |
| `dev/`, `.nodwyn/` | this repository's own tools and standing approvals | `dev/go.scm` |
| `test/exfil` | the exfiltration scenario suite | `exfil_test.go`, `scenarios/` |
| `test/browser` | the console's checks in headless Chromium | `README.md` |
| `test/fakes/ollama` | scriptable fake of the model server; `serve` runs it for the selfcheck | `ollama.go` |
| `test/fakes/jev`, `mcp`, `oauth`, `drive` | fakes of the judge, an MCP server, an OAuth2 provider and Drive's upload endpoint: no test reaches a real service | each package's comment |
| `test/fakes/mcp`, `test/fakes/container` | a fake MCP server (HTTP or stdio) and a fake container runtime that is the test binary run again | `mcp.go`, `container.go` |

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
bin/nodwyn --show-thinking replay <id>   # with the model's thinking (flags go before the command)
jq -c '[.seq,.kind]' ~/.local/share/nodwyn/sessions/<id>.jsonl
```

Event kinds are listed in [internal/log/log.go](../internal/log/log.go).
`guard.decision` lines carry `origin` (user, model, or harness for the loop
strategy and plan gate), `decision`, `checker`,
`reason` and, for confirms, `answer` and, when recorded, `actor`: who
answered (a daemon client's identity, `judge` or `unattended`). An
`llm.req` has `elided`: the seqs whose content was replaced in the window.

### Where each phase lives

Every phase of [plan.md](../plan.md) up to P10 is built except the optional
P8. This is where to start reading each, and where P8 and contained local
MCP servers would go.

| Phase | Where it lives |
|---|---|
| P3 credentials | `scheme.KHandle`; `Param.Role = "credential"`; `internal/keychain`; `guard.Egress` checks the binding table (`internal/binding`) |
| P4 L1 | `internal/detect/l1`; `guard.TextSecret` scans `Role: payload`/`argv` text |
| P5 Jev + plan gate | `internal/loop/plan.scm`; `internal/prim/jev.go`; `log.KindGatePlan`; `ArgvTrigger` is the hard-trigger list |
| P6 taint | `Value.Taint`, propagated in `taintResult`; `guard.Taint` runs before L1 |
| P7 redaction | `Session.Record` sanitizes payloads; the log writer enforces redaction; `window.Build` derives context from written events |
| P9 TUI | `internal/frontend/tui` over `core.Session`, no core change |
| P10 memory | summaries in `internal/window` (`summary.go`) and `log.KindSummary`; `index-grep` ([docs/memory.md](memory.md)) |
| P8 L3 (optional, not started) | the residual L1 marks as unknown entropy (`l1.Scanner`'s `isUnknownEntropy`); a checker after `guard.TextSecret` |
| W3b contained local MCP servers | `internal/container` runs a server in podman or docker, `internal/egress` (`cmd/nodwyn-egress`) is the allowlisting proxy, `core/contained.go` connects it to a session; `internal/mcp` speaks to it over stdio |

---

## Part 2 — Using nodwyn

### Install

```bash
make build                       # bin/nodwyn (and bin/nodwyn-egress) from the working tree
make promote                     # accept + selfcheck, then ~/.local/bin/nodwyn-stable (ADR 12/13)
                                 # and nodwyn-egress beside it
```

`nodwyn-stable version` is the commit it was built from. It is the webview
build where that can be built, so `nodwyn-stable app` opens a window; on Linux that needs WebKitGTK
([the app](app.md), "Linux and other systems").

Use `nodwyn-stable` for real work and `bin/nodwyn` only to test a change to
nodwyn itself. Never run the binary you are editing (ADR 12).

Building requires Go 1.26 or newer (ADR 33) and `staticcheck`. The
selfcheck's model turn goes to a fake model server, so promoting needs no
model, key or passphrase. Using nodwyn needs a model to talk to: the
`[control]` table in `config.toml` (`bin/nodwyn paths` shows where it lives;
everything else has defaults in
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
tools doc-export fs-edit fs-find fs-list fs-read fs-write git-diff git-status index-grep plan-step
      read-definition second-look shell-exec shell-read shell-send shell-start shell-stop
:h for commands

you> read main.go and explain the startup sequence
```

Commands at the `you>` prompt:

| | |
|---|---|
| `:q` | quit (writes `session.end`) |
| `:h` | list these commands |
| `:log [n]` | last n log events |
| `:tools` | tools offered to the model |
| `:allow` | standing command and write approvals in effect |
| `:eval EXPR` | evaluate Scheme yourself (user origin) |
| `:thinking` | toggle showing the model's redacted thinking |
| Ctrl-C | abort the running turn; at the prompt, exit |

Two messages run a command instead of a turn, in every client (the
terminal, the TUI, the console and a paired phone):

| | |
|---|---|
| `/compact [what to keep]` | compact the conversation now, whatever its size: the model writes a handover note for your latest request, and your next message goes on from the note instead of the whole conversation. Words after it are what the note must keep (`/compact the failing test names`). The log keeps everything |
| `/optimize [n \| ids] [focus]` | learn from this repository's past sessions and improve `.nodwyn/init.scm` ("Changing the loop" below) |

Flags: `--repo DIR`, `--resume ID` (go on with a session; it keeps its
repository unless `--repo` is given), `--model`, `--url`, `--show-thinking`, `--no-confirm`
(every confirm is a no — for scripts and CI), `--yolo` (every ordinary
confirm is a yes — for work nobody is watching), `--no-keychain`,
`--keychain-tty` (ask for the keychain passphrase on the terminal even when
input is a script),
`--max-tool-calls N` (hard call override), `--budget-wall 2h`,
`--budget-tokens N`, and `--budget-actions N`. The `[budget]` settings and
checkpoint behavior are described in the README. Defaults are 50/500 calls,
20m/4h wall time, 1M/10M reported model tokens and 25/200 irreversible host
attempts (soft/hard). Legacy `loop.max_tool_calls` remains an explicit override.
At a checkpoint, answer `y` to double the soft limit, `n` to stop, or enter a
larger absolute soft limit. Tool approvals and hard ceilings remain unchanged.

Line mode is one client of several: `nodwyn tui` is a full-screen terminal
client ([the TUI](tui.md)), `nodwyn app` and `nodwyn ui` show the console in
a window or a browser ([the app](app.md), [the browser view](browser.md)),
and `nodwyn run` hands a bounded task to the daemon ([runs](runs.md)).
`nodwyn -h` lists every command and flag.

### Programs that keep running

`shell-exec` runs a command to completion. For a server, a watcher or a
program to talk to, the model has `shell-start` (asks, like `shell-exec`),
`shell-read` to see new output (optionally waiting up to 60 s),
`shell-send` to type into it (asks every time: typing into a shell is running
commands), and `shell-stop`. At most four run at once, each keeps its latest
1 MiB of output, and all of them are stopped when the session closes. They
run in their own process group; if nodwyn itself is killed, they are not
cleaned up.

### Seeing what changed

In a git work tree (a folder with `.git`), the model also gets `git-status`
and `git-diff` (optionally a path, and `staged` for the index against the
last commit). They are read-only and need no approval: git runs with fixed
arguments, with external diff drivers, text conversion, the file system
monitor and the pager switched off, and credential-looking files left out.
Outside git, kept versions are the undo (`nodwyn versions`, `nodwyn undo`).

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
sessions that ended without closing, are swept. It is the one place in
nodwyn's data directory the tools may use: everything else outside the
repository stays refused, the rest of the data directory included (wherever
`XDG_DATA_HOME` puts it), and a symbolic link out of the scratch directory
does not escape it (ADR 28).

### Notes kept across sessions

A repository can keep notes from one session to the next: how its tests run,
where things are, runbooks. A strategy saves them with `project-notes-save`
(docs/scheme.md); they live in nodwyn's data directory, keyed by the
repository's path, and never in the repository or its diff. The next session
in the same repository shows them in its system prompt, under a heading that
calls them the agent's own notes: leads to check, not instructions. A save
is capped at 8 KiB and redacted, and content that looks like a credential
needs your approval. The console's task view shows them and lets you edit
them ("What nodwyn knows here"), through the same guarded save.

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
- `b` — keyed requests only: yes, and save this exact binding (class, host,
  method, position) to `bindings.toml`. It is offered only with
  `policy.unbound = "confirm"`; by default a keyed request no binding covers
  is denied outright.
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

**Between turns.** A supervised run (`nodwyn run`) still asks a person
before each next turn. `nodwyn run --unattended` does not ask anything: tool
confirms are declined unless a standing approval covers them (or answered
as `--yolo` answers them, if given too), soft budget checkpoints go on to
the hard limit, and the run goes on after a turn only if that turn marked a
plan step done, stopping otherwise for you to review and
`nodwyn run resume --unattended SESSION`. With `--judge` instead of
`--yolo`, the judge answers each ordinary confirm: it is shown the task,
the plan and the call, and the call runs only if it judges it called for
and safe (probability of allow at least `jev.approve_threshold` in
config.toml, 0.8 by default); security confirms are still declined. The
decision is Go's: no Scheme can approve what the judge refused, and a
project's `(approve-refuse? call)` can only refuse more. See [runs](runs.md).

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
turns are bounded by `--max-turns` and `--budget-wall`. `--yolo` answers the
tool confirms inside each turn; going on to the next turn still waits for a
yes at the run checkpoint, from the browser or the terminal client, unless
the run is `--unattended`.

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
project may redefine. As the model works it marks the plan's steps done or
skipped with the `plan-step` tool, and the console's plan panel (`:plan` in
the TUI) shows its progress; a mark is the model's word, and the transcript
is the evidence.
Every Jev call is a guarded egress: bound credential, L1 redaction of
what leaves, a `jev.req` log line with origin, size, hash and the answer.
Set `jev.content = "none"` in `config.toml` to turn it off. Without a
`jev-key` credential nothing is sent and the loop runs without a gate.

The model can ask the judge too, with `judge-noul`, `judge-score` and
`judge-choice`: Go injects the stored `jev-key`, the binding above decides
each call, and what the model sends is redacted the same way. Because the
model chooses that text, which may be file contents or command output, the
tools are offered only at the level that lets content leave:

```toml
# ~/.config/nodwyn/config.toml
[jev]
content = "full"
```

and only to sessions started after the `jev-key` was stored. The plan gate
does not need them, and works at the default `plan` level.

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

### Tools from MCP servers

A remote MCP server's tools can be offered too. Add the server in the
console, under Connections › Tools ([the browser view](browser.md)): its
address (HTTPS, or plain HTTP to this computer only), a key if it needs one
(kept in the keychain and bound to that host alone), and the folders where it
is offered. nodwyn lists the server's tools, you say what each does (reads,
or changes things) and which are offered, and it pins that tool list; if the
server's list changes, new sessions withhold its tools until you review it
again. The servers are kept in `mcp.toml` in the config directory. The model
sees a tool as `server__tool`, and every call crosses the guard: its
arguments are payloads that leave the machine, a tool that changes things
asks, and an undeclared one asks every call with no standing answer.

A server on this computer runs only in a container nodwyn controls (W3b,
ADR 23); "MCP servers" below says how.

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

Long work is compacted on its own just under the window's high water: the
model writes a handover note and goes on from it, in that turn and the next
ones. Set `(define compact-threshold 40000)` to compact earlier, `#f` to
only trim, and send `/compact` to compact whenever you like
([docs/scheme.md](scheme.md) §9.2).

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

### OAuth2 credentials

A service that uses OAuth2 (Google Drive and Docs, Microsoft Graph, GitHub,
Notion, many MCP servers) gets a credential of kind `oauth2`: a token set
that expires and is refreshed, instead of one value. Register nodwyn as an
OAuth client with the provider (a desktop or "installed" app, redirect
`http://127.0.0.1`), then:

```bash
nodwyn credential add --oauth2 google --client-id ID --scopes drive.file,documents
```

Your browser opens the provider's consent page (`--no-browser` prints its
address instead); nodwyn waits five minutes
for the redirect on a loopback port it opened for that one request, checks
it, exchanges the code (PKCE, so no client secret is needed unless the
provider insists: `--client-secret` asks for it), and stores the tokens in
the keychain under the class `google` (`--class` names another). The
default scopes are each provider's narrowest useful set (`drive.file` for
Google: the files nodwyn created). `--oauth2 custom --auth-url URL
--token-url URL [--revoke-url URL]` covers any other provider, such as an MCP
server's authorization server.

Like any key it goes nowhere until a binding names it, and a binding may
name the scopes it allows, so a token granted more is refused:

```toml
# bindings.toml
[[bind]]
class = "google"
dest = "www.googleapis.com"
positions = ["header:Authorization:Bearer"]
methods = ["GET", "POST"]
scopes = ["https://www.googleapis.com/auth/drive.file"]
```

Use it as any credential: a model's or an MCP server's `credential`, or
`(credential "google")` in a tool you register. When the access token has
less than a minute left, the keychain refreshes it just before it is sent,
only for a use the binding allowed, and the session logs `cred.refresh`
(never a token). A confirm for an OAuth2 credential names its scopes along
with its class and destination. If the provider refuses the refresh (you
revoked the app, the refresh token expired), the credential is marked and
every use is denied with the command that fixes it; nodwyn never opens a
browser in the middle of a turn:

```bash
nodwyn credential                  # list: kind, scopes, expiry, needs login
nodwyn credential login google     # authorize again
nodwyn credential revoke google    # revoke at the provider, delete here
```

`revoke --local` only deletes it here; `login` and `revoke` take the
credential's id or class. `credential add` stores only OAuth2 credentials: a
static key is still registered in a session, with `credential-register`. The
daemon sees these changes without a restart.

### Documents out

nodwyn writes documents three ways: Word, OpenDocument and HTML through
pandoc, PDF from LaTeX through the TeX toolchain you have, and Google Docs
through Drive's API.

**Word, OpenDocument, HTML.** `doc-export` converts a Markdown (`.md`) or
LaTeX (`.tex`) file in the workspace; the target's extension picks the
format:

```scheme
(doc-export "report.md" "out/report.docx")   ; also .odt and .html
```

It needs pandoc 2.15 or later on `PATH` (`dnf install pandoc`, `apt install
pandoc`, `brew install pandoc`); without it the tool says what to install.
The target is a write: the confirm shows source, target and formats, a
standing write approval for its directory covers it, and replacing a file
keeps its previous version. The source may come from a read-only mount.

pandoc reads the document in its sandbox, so an `\input`, an include or a
remote image in it reads nothing. In a `.docx` or `.odt`, images that are
files in the workspace are embedded; a link, a file outside the workspace
or a credential-shaped name is left out and its description kept, and the
result says which. An `.html` page keeps its images by reference, except a
link elsewhere, which the page would load when opened. Raw content written
into the source (OpenXML, HTML or LaTeX passed through) is shown as text,
never interpreted, so a document cannot carry a field, a frame or a script
that fetches something when it is opened. Nothing the model writes reaches
pandoc's command line.

**PDF.** `doc-export` does not write PDF. A TeX engine reads files and can
run programs when a document asks, and no sandbox covers that, so a PDF is
built as this repository builds its book (`make book` runs `latexmk`):
through `shell-exec`, which asks every time unless you give it a standing
approval:

```toml
# <repo>/.nodwyn/allow.toml
commands = ["latexmk -pdf"]
```

The approval covers every command line that starts so, `-shell-escape`
included: give it only where you would run any such line yourself.

**Google Docs.** `google-doc-upload` sends a `.docx`, `.odt`, `.html` or
`.txt` (at most 5 MiB) to Google Drive, which makes it a new Google Doc, and
returns its id and link. Markdown or LaTeX goes through `doc-export` first.
It needs an OAuth2 credential (see above). Register nodwyn with Google as a
desktop OAuth client with the Drive API enabled, then:

```bash
nodwyn credential add --oauth2 google --client-id ID
```

The default scope, `drive.file`, is enough: nodwyn can see and change only
the files it created. Bind the credential to Drive's upload host:

```toml
# bindings.toml
[[bind]]
class = "google"
dest = "www.googleapis.com"
positions = ["header:Authorization:Bearer"]
methods = ["POST"]
scopes = ["https://www.googleapis.com/auth/drive.file"]
```

The tool is offered to sessions started after the credential was stored.
Every call asks, naming the file, its size, the destination and the
credential's scopes, and offers no standing answer; `--yolo` answers it as
any ordinary confirm. Before sending, nodwyn reads the file as text (every
part of a `.docx` or `.odt`) and refuses it if it holds something shaped
like a key whose class is not bound for that host; a package that embeds
other files is refused, since nodwyn cannot read them. Each upload is
logged as `doc.upload`: the file, its size and hash, the destination and the
document's id, never its content or the token.

`[docs]` in `config.toml` changes the defaults:

```toml
[docs]
google_credential = "google"   # the keychain class; "" offers no Google tool
google_upload_url = "https://www.googleapis.com/upload/drive/v3/files"
```

### MCP servers

`mcp.toml` in the config directory lists the MCP servers nodwyn may use
([ADR 23](adr/0023-untrusted-mcp-servers.md)); Connections in the console
adds, reviews and approves them. A server offers nothing until you approve
its tool list, which is then pinned: a changed name, description or schema
withholds its tools until you review it again. Each tool is a primitive
`server__tool` that crosses the guard; one you have not declared a read
asks every call.

A remote server is a URL; nodwyn sends it the arguments of the calls you
allow. A local server is never run as a process of yours: nodwyn runs it in
a container, from an image pinned by digest, with podman or docker:

```toml
# ~/.config/nodwyn/mcp.toml
[[server]]
name = "notion"
transport = "container"
image = "ghcr.io/example/mcp-notion@sha256:…"   # a digest; a tag is refused
network = ["api.notion.com"]                     # [] is no network at all
mounts = ["docs", "out:write"]                   # workspace folders, under /workspace
credential = "notion-key"
position = "env:NOTION_TOKEN"
workspaces = ["/home/me/notes"]
```

```toml
# ~/.config/nodwyn/config.toml (both optional)
[mcp]
runtime = "podman"                 # or "docker"; unset: podman if on PATH, else docker
egress = "/opt/nodwyn/nodwyn-egress"   # unset: next to the nodwyn executable
```

A task never downloads code. Pull the image yourself, by its digest, once
and again after you change it:

```bash
nodwyn mcp                # the servers, and whether each contained image is here
nodwyn mcp pull notion    # or Pull in Connections
```

What the container gets, every time: no host environment (only the
variables named here), no capability, a read-only root with a small `/tmp`,
bounded memory and processes, no setuid privilege, the folders in `mounts`
and nothing else of yours (never the workspace root, never `.nodwyn/`), and
`--pull=never`. `command` and `args` replace the image's entrypoint, with no
shell. nodwyn talks to the server over its standard input and output, so
nothing listens on a port. Its standard error is kept, capped and redacted,
for the task's details; the model never sees it.

With `network = []` the server has no network at all. With hosts, it sits on
a network with no route out; its only way out is nodwyn's egress proxy
(`nodwyn-egress`, built by `make build` next to `nodwyn`), in a second
container, which lets through those hosts alone, on ports 443 and 80 or the
port an entry names (`uploads.example.com:8443`), and refuses the rest. The
server finds it through `HTTPS_PROXY` and its siblings; a client that
ignores them reaches nothing. Every attempt, let through or not, is recorded
in the session log as `mcp.egress`; the task shows the refusals.

A credential goes in once, as the variable `position` names, and only if its
class is bound for every host on the list at that position (any method: the
server makes the requests):

```toml
# bindings.toml
[[bind]]
class = "notion-key"
dest = "api.notion.com"
positions = ["env:NOTION_TOKEN"]
methods = ["*"]
```

Adding the key in Connections writes these rows. A server whose key is not
bound for one of its hosts does not start, and each check is recorded as an
`egress.check`; a binding removed during a task stops further calls. A
variable cannot be refreshed later, so an OAuth2 token is refreshed just
before the server starts and the server gets the token valid then. With no
network a credential has nowhere to go, and is refused.

A contained server's calls go to `mcp:NAME`; a confirm names it and what it
can reach (`the contained server notion (network: api.notion.com)`). A key
written into the arguments is refused unless its class is bound for every
host on the list. The containers stop when the task closes. If nodwyn is
killed instead, their input ends: the proxy stops, and so does a server
that ends with its input, as MCP servers do; anything left shows in
`podman ps --filter label=nodwyn.mcp`, and the networks remain until
`podman network prune --filter label=nodwyn.mcp`. `transport =
"host-process"` is refused.

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

**What a request cost.** After each reply the TUI, line mode and the
console show its tokens, and when the provider says so, how much of the
prompt came from its cache and what the request cost:
`12,400→800 tok · 78% cached · $0.012`. OpenRouter reports both in every
response, cost in USD included; nothing has to be asked for. A server that
reports no cost can be priced in its model entry, per million tokens:

```toml
[models.coder]
price_input = 3.0          # USD per million prompt tokens
price_cached_input = 0.3   # prompt tokens read from the cache; unset: price_input
price_output = 15.0        # output tokens, reasoning included
```

A cost from prices is an estimate, shown with ≈: it does not know a
provider's surcharge for writing to its cache, so a reported cost always
wins. Ollama reports neither, and its line reads as before; so does an older
log. The TUI's `:budget` adds the turn's spend over every model request (the
planner's, sub-agents' and compaction's included; the judge reports no
cost), and a budget checkpoint says what the turn has cost so far. Spend is
shown, never a limit: the token budget still counts cached tokens in full.

No keyed request follows a redirect (`http-request`, Jev, MCP), and no model
request does, keyed or not: the binding was checked for the host the request
went to, and a redirect would take the key and the body (for a model, the
whole window) to wherever the server points. The request fails instead and
names that host, `the server redirected to HOST; nodwyn does not follow
redirects for keyed requests` (or `for model requests`, which are not
retried); an MCP call fails with the redirect's status. If that host is
where the request belongs, send it there: set the model's `url`, or the
address your tool sends to, and bind the key to that host.

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

The window is sized for the control model's context. Its defaults
(`window.high_water` 61440 and `low_water` 30720, in the window's estimate of
bytes/3) suit a small local context. When the context is larger, as
`num_ctx` says or, for a chat-completions server that leaves it unset, as the
server's model list says (OpenRouter's `context_length`, vLLM's
`max_model_len`; asked once per process, without a key), the high water grows
to three quarters of the context less the reply cap, and the low water to
half of that: a 1M-token model trims past 774144 and compacts just under it.
The last quarter is room for compaction's handover note (docs/scheme.md §9.2),
so a long task is summarized before anything is trimmed. The session log
notes the size it chose, or that the server did not say; set `num_ctx` then.
Setting `high_water` or `low_water` in `[window]` keeps the window at your
size. A larger window sends more with every request: on a paid model, a
request near the high water costs that many prompt tokens, less what the
provider caches.

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

Any model may also set its sampling: `temperature` (0 to 2) and `seed`.
Unset, the server decides. They go with every request to that model (in
ollama's `options`, or as chat-completions fields) and are recorded with each
`llm.req`, so `nodwyn replay --window` rebuilds the request exactly.
`temperature = 0` with a `seed` makes replies as repeatable as the server
allows; many servers are still not bit-for-bit deterministic, especially
across hardware or batching.

```toml
[control]
provider = "ollama"
url = "http://localhost:11434"
model = "qwen3:8b"
temperature = 0
seed = 42
```

**Images.** Files are attached from the console's composer
([the browser view](browser.md), "Attaching"). An image can be attached
only when the control model takes images. Ollama says whether a model does (`ollama show MODEL` lists `vision`
among its capabilities), and nodwyn asks it once per session. A
chat-completions server does not say, so you do:

```toml
[control]
provider = "openai"
url = "https://api.example.com/v1"
model = "some-vision-model"
credential = "example-key"
vision = true                     # it reads images; openai only
```

Attaching an image (PNG, JPEG, GIF or WebP, decided by its content; at most
10 MB and 8000 pixels a side) asks a security confirm of its own. It names
the file and the control model's host, says whether that host is this
machine or another one, and says that the image cannot be redacted: whatever
it shows reaches the model as it is, and nothing in it is read as text.
`--yolo` never answers it yes, and `--no-confirm`, a judged run and an
unattended run decline it. The log keeps
the image's type, size, digest, dimensions and host, never its pixels; the
copy is in `.nodwyn/attachments/`. An image goes only to the host it was
confirmed for: a request to another configured model, at another host,
says the image is not for it, and the judge, the planner and sub-agents
never get it. The window counts an image as about 85 tokens plus 170 per
512-pixel tile once it is scaled to fit 2048×2048 and a shorter side of
768 (a 1200×800 screenshot is about 1,105). If its stored copy changes or
is deleted, later requests say so instead of sending it, which is also how
to take an image out of a conversation.

The console's Settings page writes `[control]` for you. Saving the same model
on the same server keeps what you tuned in the file (`num_ctx`, `keep_alive`,
`max_output`, `output_field`, `temperature`, `seed`, `vision` and the
`price_*` entries); choosing another model starts from its defaults.

The line-mode `nodwyn` refuses to start without it, printing the table to
add, and checks at start that an Ollama control server answers. A daemon
session (console, TUI, runs) opens without it, to replay or evaluate; its
first model request fails with that table, and is not retried. A key that is missing or not
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
Unknown entropy remains a residual for P8's optional L3 classifier.

### Building nodwyn with nodwyn

1. `nodwyn-stable` in this repository; `:tools` should list `go-test`,
   `grep-repo`, `read-adr`, `git-log`, … from `dev/` (and the builtin
   `git-status` and `git-diff`).
2. Give it small, explicit tasks: name the file, the pattern to follow, the
   test to add, and ask it to run `go-test` on the package. A 27B model does
   well with a paragraph and badly with a page (plan §3.4).
3. Review every diff at the confirm. `fs-edit` diffs are small; an
   `fs-write` of a whole existing file deserves suspicion.
4. When it gets in the way, hand-edit, and add one line to
   [docs/friction.md](friction.md). That file decides what gets built next.
5. `make accept` before committing; you commit — there is no commit tool.
6. `make promote` when a phase's acceptance is green.

### When something is wrong

| Symptom | Look at |
|---|---|
| `no control model is configured` | add the `[control]` table it prints to `config.toml` ("Choosing models") |
| `control model … unreachable` | is the `[control]` URL right and the server up? For Ollama, `curl <url>/api/version` |
| first turn takes 10–60 s | another client holds the model server's slot, or the prefix is cold; later turns are sub-second |
| every request takes 30–50 s | the prefix cache is missing: check `:log` for `llm.req` — a trim (elided count jumped) or a new user turn re-prefills once; if it is every request, another client is evicting the server's slot between your requests (a single-slot Ollama, for example) |
| the model loops on a denied call | the progress guard stops repeated denials; inspect the log before continuing. Budgets also bound each turn, and `--max-tool-calls N` sets a hard call ceiling |
| `hard stop: tool_calls budget reached` | the turn ended; the log has everything; rephrase smaller, or raise `--max-tool-calls` |
| a tool call errored | `:log 20` — the `tool.result` line has the exact error the model saw |
| a confirm never appeared but a file changed | `:allow` — a standing approval covered it; the `guard.decision` line names which |

## Local daemon

`nodwyn daemon` starts the U2 session server in the foreground. Use
`nodwyn daemon --no-keychain` to skip credential-store startup, and
`--socket /absolute/private/directory/sock` to override its socket path.
The protocol supports session lifecycle, send/abort, subscriptions, paged
log catch-up, answering prompts (`session.answer`, `session.secret`,
`session.approval.create`, `session.binding.create`), runs, and what the
console needs besides: model and keychain setup, workspaces, attachments,
file versions and MCP servers. See [the protocol guide](protocol.md) for the handshake, methods,
permissions and reconnect semantics. `nodwyn app` starts one itself when none
is running ([the app](app.md)).
