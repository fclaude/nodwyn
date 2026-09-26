# nibb manual

Two parts: how to **read the code**, and how to **use** the bootstrap-era
binary. For *why* things are shaped this way read [SPEC.md](../SPEC.md);
for *what comes next* read [plan.md](../plan.md); for individual decisions
read [docs/adr/](adr/).

---

## Part 1 — Reading the code

### The one-paragraph model

A **front-end** sends the user's text to a **core session**. The session's
**loop** builds a context **window** from the session **log**, sends it to the
control-plane **model** (Ollama on workshop) and displays the redacted answer after generation. When the
model asks for a tool, the loop turns the request into a Scheme application
`(tool-name arg …)` and evaluates it in the embedded **Scheme** interpreter.
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
| 3 | [internal/loop/loop.go](../internal/loop/loop.go) `Turn` | rebuilds the window from `EventsSoFar()` every request; tool calls become `runTool` |
| 4 | [internal/window/window.go](../internal/window/window.go) `Build` | elides oldest tool results *in place* so the request prefix never changes |
| 5 | [internal/llm/ollama.go](../internal/llm/ollama.go) `Chat` | native `/api/chat` streaming; thinking separated; tool-call args stay JSON objects |
| 6 | [internal/loop/loop.go](../internal/loop/loop.go) `apply` | `Prim.ArgsFromJSON` orders named JSON args into positional Scheme values; `WithOrigin(ctx, OriginModel)` |
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
| `internal/loop` | the agent loop | `Turn`, `runTool` |
| `internal/core` | the UI-agnostic session façade | `construct.go` `New`, `session.go` `Send`, `confirm.go` `confirm` |
| `internal/plan` | the plan gate and bounded revision policy | `gate.go` `Run` |
| `internal/keychain` | encrypted credentials, opaque handles and injection | `keychain.go` |
| `internal/binding` | binding loading and authorization lookup | `binding.go` `Lookup`; Scheme call decoding lives in `guard/egress_facts.go` |
| `internal/redact`, `internal/detect/l1` | text redaction and credential-shape detection | `redact.go`, `l1.go` |
| `internal/event` | what a front-end sees | `event.go` |
| `internal/log` | the append-only log | `log.go` `Append`, `Replay` |
| `internal/config` | XDG paths, config.toml and allow.toml | `Default`, `Validate` |
| `internal/frontend/plain` | line mode and replay | `chat.go` `show` |
| `internal/frontend/rpc` | local daemon sessions, turns and events | `server.go`, `sessions.go`, `events.go` |
| `cmd/nibb` | flags → core → front-end; nothing else | `main.go` |
| `dev/`, `.nibb/` | this repository's own tools and standing approvals | `dev/go.scm` |
| `test/exfil` | the exfiltration scenario suite | `exfil_test.go`, `scenarios/` |
| `test/fakes/ollama` | scriptable fake of the model server | `ollama.go` |

### The invariants and the tests that hold them

Read a test when you want to know what a claim actually means.

| Claim (SPEC) | Enforced by |
|---|---|
| The log is the single source of truth | `nibb replay` reproduces a transcript from the file alone; `window.Build` takes only events; [log_test.go](../internal/log/log_test.go) round-trips and repairs |
| The interpreter is the only Scheme→Go surface; the guard sits on it | `applyPrim` is the only caller of `Prim.Handler`; [scheme_test.go](../internal/scheme/scheme_test.go) `TestGateIsTheSingleExit` records every crossing including through `map`/`apply`/`sort` |
| Loading `.scm` cannot change evaluation | `TestKernelFixed`: fingerprint unchanged, only the primitive table grew |
| Proper tail calls | `TestTailCalls`: 200k iterations in nine tail positions under an 8 MB stack cap |
| Every builtin is documented | `TestDocCoverage` fails for any primitive missing from `docs/scheme.md` |
| A handle can never become text | `TestHandlePrintsOpaque` |
| The core knows nothing about terminals | [scripts/lint-boundaries.sh](../scripts/lint-boundaries.sh) in `make lint` |
| No path exists for the canary to leak | [test/exfil](../test/exfil/): each scenario asserts the decision *and* greps the canary out of log, output and result |
| Request prefix is byte-stable | [session_test.go](../internal/core/session_test.go) `TestTurnWithToolCall` compares request N's messages with request N+1's prefix |

### Reading a session log

```
bin/nibb sessions                      # ids, oldest first
bin/nibb replay <id>                   # transcript
bin/nibb replay <id> --show-thinking   # with the model's thinking
jq -c '[.seq,.kind]' ~/.local/share/nibb/sessions/<id>.jsonl
```

Event kinds are listed in [internal/log/log.go](../internal/log/log.go).
`guard.decision` lines carry `origin` (user or model), `decision`, `checker`,
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

## Part 2 — Using nibb

### Install

```bash
make build                       # bin/nibb from the working tree
make promote TAG=v0.1-bootstrap  # accept + selfcheck, then ~/.local/bin/nibb-stable (ADR 12/13)
```

Use `nibb-stable` for real work and `bin/nibb` only to test a change to
nibb itself. Never run the binary you are editing (ADR 12).

Requires Go 1.27, `staticcheck`, ripgrep (`rg`) for text retrieval, and a reachable control plane: by default
the Ollama server on workshop over the tailnet (`bin/nibb paths` shows where
`config.toml` lives; a missing file means the defaults in
[internal/config/config.go](../internal/config/config.go)).

### A session

```bash
cd ~/src/your-repo
nibb-stable                       # pins every path to this directory
```

```
nibb session 20260921T143846Z-beba937b
repo  /Users/you/src/your-repo
tools fs-edit fs-list fs-read fs-write index-grep index-symbols shell-exec
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
test rig never stops to ask. It answers the guard's questions; it does not
overrule its refusals. Still denied, exactly as with a human at the prompt:

- anything outside the repository, and symlinks that leave it;
- credential-shaped files (`.env*`, `*.pem`, `id_*`, `~/.ssh`, …);
- a literal key on a command line, or in a request to an unbound host;
- content that came from a file or a command heading somewhere no binding
  names;
- **security confirms** — a write whose content looks like a credential, a
  command line built out of tool output. Those exist for the case where a
  human was the control, so yolo refuses them rather than pretending.

Budgets are what stop a runaway turn when nobody is watching, so set
`--max-tool-calls`, `--budget-wall` and `--budget-actions` for the job.
Every decision is logged with its verdict and `nibb replay` shows exactly
what the run did.

Use it on a throwaway checkout. The guard makes yolo survivable, not safe.

### Standing approvals

Commands you never want to be asked about go in one of two files:

```toml
# ~/.config/nibb/config.toml
[policy]
allow_commands = ["go test", "go build", "git status", "git diff", "rg"]
```

```toml
# <repo>/.nibb/allow.toml   (tracked with the repository)
commands = ["make lint", "make test"]
writes   = ["internal/", "docs/"]     # write prefixes that never prompt
```

`.nibb/`, `.github/`, `.gitea/`, `scripts/` and `Makefile` always prompt,
whatever the list says — they change policy or run in CI. So does a write
whose content looks like a credential (L1): that confirm names the class
and no standing approval waives it.

Prefixes match argv tokens: `"go test"` matches `go test ./...` and
`/usr/local/go/bin/go test`, not `go build`. Denies still apply (the repo
pin, the credential denylist); only the confirm is waived, and every use is
logged as `allow` with reason `standing approval: go test`. Keep them
read-only: nothing that commits, pushes, deletes or reaches the network.

### The judge (Jev)

The plan gate runs once a `jev-key` credential exists and is bound:

```bash
nibb keychain init                       # once
nibb-stable                              # then, at the prompt:
:eval (credential-register "jev-key")    # masked input
```

```toml
# ~/.config/nibb/bindings.toml
[[bind]]
class = "jev-key"
dest = "api.typesafe.ai"
positions = ["header:Authorization:Bearer"]
methods = ["POST"]
```

Before each user message the gate asks Jev whether the message needs a
plan; if so the model drafts one, Jev scores it against the ask (up to
three revisions), and the plan goes into the task block the model sees.
Every Jev call is a guarded egress: bound credential, L1 redaction of
what leaves, a `jev.req` log line with size, hash and the answer. Set
`jev.content = "none"` in `config.toml` to turn it off. Without a
`jev-key` credential nothing is sent and the loop runs without a gate.

`register-check` in `.nibb/init.scm` adds your own tightening-only guard
checkers (deny or confirm, never allow); see `docs/scheme.md` §7.

### Giving the model tools of its own

`<repo>/.nibb/init.scm` is loaded at start (after the prelude). Anything it
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
try things in `nibb repl` or with `:eval`.

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
The planner's separate request is also redacted. CI checks the history writer.

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

### Building nibb with nibb

1. `nibb-stable` in this repository; `:tools` should list `go-test`,
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
| `control plane … unreachable` | tailscale up? `curl http://workshop.example:11434/api/version` |
| first turn takes 10–60 s | another client holds workshop's single slot, or the prefix is cold; later turns are sub-second |
| every request takes 30–50 s | the prefix cache is missing: check `:log` for `llm.req` — a trim (elided count jumped) or a new user turn re-prefills once; if it is every request, another client is evicting workshop's single slot between your requests |
| the model loops on a denied call | the progress guard stops repeated denials; inspect the log before continuing. Budgets also bound each turn, and `--max-tool-calls N` sets a hard call ceiling |
| `hard stop: too many tool calls` | the turn ended; the log has everything; rephrase smaller |
| a tool call errored | `:log 20` — the `tool.result` line has the exact error the model saw |
| a confirm never appeared but a file changed | `:allow` — a standing approval covered it; the `guard.decision` line names which |

## Local daemon

`nibb daemon` starts the U2 session server in the foreground. Use
`nibb daemon --no-keychain` to skip credential-store startup, and
`--socket /absolute/private/directory/sock` to override its socket path.
The protocol supports session lifecycle, send/abort, subscriptions and paged
log catch-up. Approval replies remain unimplemented; turns waiting for one can
be aborted. See [the protocol guide](protocol.md) for the handshake, methods,
permissions and reconnect semantics.
