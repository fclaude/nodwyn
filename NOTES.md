## Objective
Design the full architecture of a minimal, self-extending coding-agent harness ("pi-clone") in Go + user-written Scheme, with a binding-based credential/permission system, Jev (TypeSafe) as a calibrated judge at specific seams, a disk/cache/RAM memory hierarchy, and a streaming TUI. Name the harness. No code yet — purely architectural design + naming.

## Important Details
- **Language: Go**. Go owns loop, guard, TUI (bubbletea + crossterm), LLM client, all I/O. Scheme (user-written interpreter embedded in Go) owns orchestration/logic only. Single static binary.
- **Scheme role**: orchestration only. All I/O (HTTP, file, exec, LLM calls) are Go host primitives. Scheme never touches network/filesystem. Only FFI surface = primitive table (name → Go handler).
- **Guard**: one Go wrapper at `apply→Prim` boundary = single exit from Scheme to Go. Inspects full args. Applies to ALL primitives equally (builtin + Scheme-registered). Denial fed back as tool result.
- **Agent loop**: ask LLM → tool call (through guard) → feed result → repeat. Stop on plain answer / hard stop. No sub-agents.
- **Plan trigger**: Jev noul gate ("is this multi-step?") before each message — scoped to task intent/metadata (NOT secrets), logged per message. + code-enforced hard trigger for irreversible/high-stakes actions. (0.77)
- **Jev seams (2)**: Seam A = score draft plan (abstract). Seam C = score ambiguous egress *situation* (class+dest+scope, NEVER the value). Seam B dropped.
- **Jev constraint**: external API, no non-retention guarantee → **Jev must never see content that can hold secrets**. Jev judges *why* (intent/plan/abstract), never *what* (content/args/values).
- **Secrets = credentials, not content**: User calls `credential-register` (masked input, log-excluded encrypted keychain, returns opaque HANDLE). User binds: `(class → dest, positions, methods)`. Harness never sees the value. Out-of-band injection at bound sink.
- **Two-plane split**: Control-plane = harness's own LLM key. Data-plane = user-registered credentials (binding-checked).
- **Binding table = egress authority**: Pass iff class bound to (dest,positions,methods). Unbound/unknown → deny/confirm. (1.0)
- **Local content stack** (all zero-retention, local):
  - L1: regex + entropy + egress allowlist. Fails closed, sub-ms. (0.93)
  - L2: taint on **Scheme values only**. Tag at entry, propagate via interpreter, guard checks at FFI. Scoped to stopping the *model*, not user. (0.54)
  - L3: local feature classifier. Calibrated P(secret). Trains on public corpus + own confirmed events. Optional. (0.68)
- **Substrate split** (0.99/1.0): A = Scheme values (L2 taint). B = LLM context window (free text). L2 cannot track B. Ingress redaction guards B.
- **Ingress redaction as substrate-controller**: L1 shape + L3 filter runs at **EVERY** point text content enters model context. `[REDACTED class]` placeholder. (0.91, 0.71)
- **I/O edges** (final form):
  - E1: external → scheme value = L2 taint
  - E2: external → model context = L1+L3 redaction
  - E3: model/scheme → external = binding check + L1/L3 + L2 flag
  - E4: user/OS → model context = L1+L3 redaction
- **Ladder principle**: deterministic layers (L1, L2) early-terminate; L3 fires only on class-unknown residual. (0.73)
- **Memory hierarchy**: Disk = lossless log + session index (source of truth). Cache = summaries as log entries. RAM = token-budgeted window. V0: no cross-session, no embeddings. (0.90, 0.84)
- **Log invariants**: Log is single source of truth. Index and window are derived, revocable views. (0.90)
- **Jev API**: `POST https://api.typesafe.ai/v1/systemone`. Model: `jev-1.13.0`. Tools: `jev_jev_score`, `jev_jev_noul`, `jev_jev_choice`.
- **User's own Scheme interpreter**: user wrote it to embed in Go.
- **NO CODE YET**: all work is architecture + naming.

## Work State
### Completed
- Full architecture designed and validated across 7+ Jev rounds
- **SPEC.md written to disk** at `/Users/you/Documents/Default Project/SPEC.md` (135 lines, complete spec)
- I/O edges finalized: "every I/O edge guarded by mechanism fitting its substrate" (not "redaction at every I/O")
- Ladder principle confirmed: L1/L3 act on I/O text, L2 acts on scheme tool variables to prevent L3 when we already know
- **Naming questions asked (10), user answered**:
  - Tool of the trade
  - 5–7 letters
  - Point at coding agent / CLI
  - Abstract / coined
  - Playful
  - No scheme/extensibility hint needed
  - No "local/private/yours" in the name
  - High collision worry (avoid taken names)
  - All three: typeable > sayable > distinct
  - 3am feeling = sharp instrument
- **npm collision check run**: most common coined words (talon, klick, krait, vane, grip, quill, scribe, crimp, etc.) are TAKEN. Free candidates found: `gidge`, `knick`, `nibb`, `nibber`, `scribee`

### Active
- **Naming**: shortlist under construction. Free candidates need final user selection
- L2 taint still weakest row (0.54) — accepted, scoped to model-exfil only
- #10 control/data-plane confirm loop at 0.61 — flagged as risk, mitigation noted (name class+dest in prompt)
- Combined "egress closed" at 0.30 → reframed honestly in spec

### Blocked
- (none)

## Next Move
1. **Shortlist 3–5 names** from free candidates (`gidge`, `knick`, `nibb`, `nibber`, `scribee`) or generate new ones that hit: 5–7 letters, playful, sharp-instrument, typeable, free on npm/crates
2. User picks → final name locked
3. Begin build order step 1: Scheme interpreter (Go) + primitive table

## Relevant Files
- `/Users/you/Documents/Default Project/SPEC.md` — full architecture spec, 135 lines, complete
- `https://raw.githubusercontent.com/badlogic/pi-mono/main/README.md` — original pi reference
- `https://docs.typesafe.ai/primitives/choice` — TypeSafe Choice schema
- `https://api.typesafe.ai/v1/systemone` — TypeSafe System One API
