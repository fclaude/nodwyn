# nodwyn — Architecture Spec (v0)

*Written on 2026-09-20 as "pi-clone", before the project had a name (ADR 31). Amended where later decisions changed it, never contradicted.*

A minimal, self-extending coding-agent harness. Single binary. One developer, 3–6 month budget.

**Stack:** Go host + user-written Scheme interpreter embedded in Go. Single static binary. TUI via bubbletea + crossterm. *(crossterm is a Rust crate; in Go bubbletea owns the terminal, ADR 11.)*

---

## Invariants (load-bearing)

1. **The log is the single source of truth.** Index and window are derived, revocable views; a stale summary means recompute-from-log. (Jev 0.90)
2. **Jev judges intent, plans and egress *situations* (class+dest+scope) over redacted content; the local stack judges *what* (secret shape, arguments, provenance).** Jev never sees a secret value. Jev is never audited for the control-plane key. *(Wording amended by ADR 19.)*
3. **Secrets are credentials, not content.** The harness holds opaque handles into a log-excluded encrypted keychain, uses them only at a bound destination, and never sees the value.
4. **The Scheme interpreter is the only Scheme→Go surface; the guard sits on it.** (Jev 0.90)
5. **Every I/O edge is guarded — by the mechanism that fits its substrate.**

---

## The agent loop

`ask LLM → run tool (through guard) → feed result back → ask again`. Stop on a plain answer or a hard stop. No sub-agents for the core loop. *(Sub-agents exist as user code over the loop's primitives, ADR 27; the default loop's habits use them, ADR 32. The invariants below stay in Go.)*

The loop is a Scheme procedure, `agent-turn`, so how a turn goes — plan first, consult the judge, stop a batch at its first failure — can change without changing Go. What a turn must not skip stays in Go, behind the loop's primitives: windows built from the log, only the model's own calls run, one result per call, budgets and the progress guard. The core is enough when the behaviours we want — a plan cycle with a judge, routing work to another model — can be written as user code. *(Added by ADR 26.)*

**Plan trigger:** a Jev yes/no gate (intent-only, logged, not secrets) before each message, *plus* a code-enforced hard trigger for irreversible/high-stakes actions. (Jev 0.77)
Chosen over a pure pull model because the gate is auditable per message and improves as Jev improves.

---

## Egress authority: binding, not content recognition

A secret is never "suspicious" in isolation — `sk-ant-...` to `api.anthropic.com` is *right*; the same bytes to `collect.example.com` is exfil. The rightness is a **binding** question:

> **Pass a secret-bearing egress iff its class is authorized for (destination, positions, methods).** (Jev 1.0)

- **Binding table (user-owned):** `(secret-class → destination, positions, methods)`. Bound → silent pass + logged. Unbound/unknown → **deny or confirm**, never fail open. (Jev 1.0)
- **LLM-as-tool split:** the harness's *own* LLM key is **control-plane** (its identity, standing, not grant/revoke, not Jev-audited for mutation). *Other* providers (OpenAI, local, MCP) are **data-plane** credentials the user binds → LLMs become tools among tools. (Jev 0.61 — the confirm loop is the soft spot, see Risks) A loop strategy may also hand work to them: a configured model, asked by name, over a context the strategy narrowed; its key is bound and checked like any other. *(ADR 27.)*
- The harness does not *classify* a secret; the user **binds** one.

---

## Content-detection ladder (cost escalates; deterministic short-circuits learned)

| Layer | Acts on | When it fires | Cost |
|---|---|---|---|
| **L1** | text I/O | known secret *shape* (regex/entropy/prefix). Sub-millisecond, fails closed. | ~0 |
| **L2** | scheme values (provenance) | a known-class (tainted) value is heading egress; exact provenance tags checked at the FFI boundary. | ~0 |
| **L3** | text I/O | only the **class-unknown shape residual** L1/L2 didn't decide. Calibrated `P(secret)`, feeds the decision with binding — never decides alone. | learned |

**The deterministic layers early-terminate the learned layer: if L1 or L2 already *knows*, L3 is not paid for.** L3 is the expensive option, invoked only on the residual. (Jev 0.73)

- **L1** — regex/entropy/egress-allowlist, fails closed, auditable. (Jev 0.93)
- **L2** — taint: tag a value at registry entry, propagate through Scheme primitives, guard checks the tags. Tags are immutable sorted name sets with no global registry or attribution cap (amended 2026-09-22 for readability). Scoped to stopping the *model's* indirect exfil, not a user (who is the principal). (Jev 0.54, sound-for-purpose)
- **L3** — local, interpretable feature classifier v0 (entropy, char-ngram spectra, base64/URL/JWT structure, prefix, digit-ratio). Small neural context classifier is the documented upgrade path. Trains on the public secret corpus + the harness's own confirmed events (features + boolean, never raw content). Optional — system works without it. (Jev 0.68) *(Not built: plan P8. Until it is, the text layers are L1 alone.)*

---

## I/O edges — each guarded by the mechanism that fits its substrate

| Edge | Substrate | Mechanism |
|---|---|---|
| **E1** external → scheme value | scheme | **L2** taint (provenance tag) |
| **E2** external → model context | text | **L1+L3** redaction (secret → `[REDACTED class]`) before entry |
| **E3** model/scheme → external | both | **binding check** + L1/L3 (+ L2 flag) |
| **E4** user/OS → model context | text | **L1+L3** redaction before entry (catches user-pasted secrets) |

**Response editing:** anything entering the model context is replaced with a `[REDACTED class]` placeholder before entry, so a leaked secret can't be re-emitted even to a bound destination. (substrate split, Jev 0.91) Redaction is a **substrate controller** applied at every context-ingress (E2, E4), not just API responses. (Jev 0.71)

**Substrate principle:** L2 guards scheme values; L1/L3 guard text. L2 cannot reach the model's context window (not a Scheme value, no flag to propagate) — that was a category error, now resolved by running the text layers there.

---

## Jev — where it is allowed

*Amended 2026-09-21 by ADR 19: the original "never content" rule conflated content with secret values. Seam A needs the ask and the plan.*

- **Seam A:** score a draft **plan** against the **user's ask** (quality/coherence/fulfillment), over the actual text of both. LLM revises on low score, ≤3 rounds.
- **Seam C:** score an ambiguous egress **situation** on **class + destination + scope**, never the value. *(Amended 2026-10-01 by ADR 18 and ADR 20: in a run started with `--judge`, the judge answers its ordinary confirms. It is shown the task, the plan and the call at the user's `jev.content` level, redacted; the decision is made in Go, and security confirms are never put to it.)*
- **Jev is an egress edge.** Its credential is a bound data-plane credential (E3) and its payload passes the same redaction controller as the model context (E2/E4). What it may see beyond ask+plan is a user policy (`jev.content`: none / plan / plan+summaries / full), logged per call.
- **Never:** a secret value (enforced by redaction, not by withholding content), control flow, or its own loop (control-plane key is exempt).

**Why-not-what principle, restated:** Jev judges intent and plans over *redacted* content; the local stack judges provenance and secret shape, and is the only thing that ever sees a value.

---

## Memory (disk / cache / RAM)

- **Disk =** lossless log + session-scoped structural index. **Source of truth.**
- **Cache =** summaries as log entries (rebuildable, revocable, possibly stale).
- **RAM =** token-budgeted window, surgical pull on demand.
- **v0 scope:** no cross-session, no embeddings/prometheus-style index. (Jev 0.84)
- Retrieval is session-scoped text search (grep); structural outlines and a semantic/embedding index are deferred.
  *(Amended by ADR 50: the model also searches by words the repository, from a cross-session index of words and line numbers kept per project in the data directory and derived from the files, and its own conversation, recalling an event by its seq, from an index kept in memory and derived from the log; earlier tasks and embeddings are designed there, not built.)*

---

## Self-extension invariant

- Loading a `.scm` does **not** modify the eval/apply kernel — the two recursive functions are fixed.
- New capabilities enter only through the **primitive table** (name → Go handler) via `register-tool`.
- Every Scheme-registered tool goes through the same `apply → Prim → guard` path as a builtin.
- Consequence: a Scheme extension reaches the host only by crossing the guard. (its own exfil is its privilege — see Risks)

---

## Risks / explicitly-accepted limits

1. **L2 is scoped, not a security boundary.** It stops the *model's* unintended leak; a user can write `(box v)` or `(wipe v)` to strip the tag. The real egress lines are **binding (E3) + fail-closed + ingress redaction (E2/E4)**.
2. **The user-confirm loop can be uninformed consent.** When an unbound secret egresses, the harness names the *class*, never the value; if the class is also unknown, "confirm" may be a rubber-stamp. Mitigation: name class + destination in the prompt; consider requiring a binding rather than a bare confirm. (control-plane/data-plane row sits at 0.61 partly on this)
3. **L3's value depends on training data.** With a public-corpus baseline it is strong for known-shaped secrets and weak for novel internal tokens — that is exactly the fail-closed residue binding catches.
4. **The combined "egress surface is closed" claim is 0.30** until redaction is promoted to a substrate controller (E2/E4) and E4 (user paste) is covered. Once all four edges are explicitly guarded, the honest statement is: **no path exists for a secret *value* to egress unless bound or explicitly re-confirmed — but "closed" is the user's affirmation, not a property the harness can prove on its own.**

---

## What Jev did NOT decide (deliberate)

- Whether to build a scheme subagent / multi-agent loop — out of v0 scope. *(Built after v0 as user code, ADR 27.)*
- Whether embeddings are worth building — deferred; structural index first.
- Whether the harness should support GUI — no; TUI only. *(Revised by ADR 21 and ADR 30: a browser console and a desktop app, clients of the same protocol as the TUI.)*
- Whether Jev should ever be allowed to see argument content — *revised by ADR 19*: yes, after redaction, at a level the user chooses; raw tool results are opt-in per repository because of retention.

---

## Build order (recommended)

1. Scheme interpreter (Go) + primitive table + apply/apply (the "hands").
2. Guard at `apply → Prim` (the "boundary").
3. Binding table + credential register/revoke masked + encrypted keychain + handles.
4. L1 fast path (regex/entropy/allowlist) — fail-closed.
5. Noul plan gate + hard-trigger.
6. L2 taint (scheme-value propagation).
7. E2/E4 ingress redaction + `[REDACTED class]` placeholders.
8. L3 feature classifier (optional; public corpus baseline).
9. TUI (bubbletea) — streaming the loop, the tool call, the plan, the confirm.
10. Memory: log + session index + summary-as-log-entry.

---

**Every Jev number above is a live read, not a memorized score.**
**The load-bearing line is not "L2 prevents exfil" — it is "binding authorizes, fail-closed denies, redaction keeps context clean, and the user is the authority."**

## Implemented security boundary amendment (2026-10-06)

[ADR 45](docs/adr/0045-project-trust-and-security-boundaries.md) is authoritative for
repository trust, executable identity, credential transport choices, bounded
keychain decoding/persistence, and canonical project identities. Repository-owned
init and approvals load only after external user review. A credential binding does
not itself permit remote HTTP: `policy.allow_insecure_http` is a separate user
choice, false by default. Approved shell commands retain the user's OS privileges;
these checks are not an operating-system sandbox. See [the security reference](docs/security.md)
for the implemented limits and existing-data behaviour.
