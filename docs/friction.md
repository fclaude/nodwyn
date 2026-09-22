# Friction log

Where `nibb-stable` got in the way while building nibb (ADR 15). One line
per observation, newest first. Reviewed at every promotion; the top items
become side-work in the next phase.

Format: `date · session · what happened · what it cost · idea`

- 2026-09-22 · readability refactor · implemented with an external agent under ADR 16 after reviewing the spec and roadmap; acceptance and fresh race tests passed · no claim of nibb self-authorship · preserve shared credential and integration boundaries while simplifying the code inside them
- 2026-09-22 · P7 · implemented with an external agent under ADR 16, validated with the candidate selfcheck · no claim of nibb self-authorship · model output is now buffered because per-token redaction cannot safely recognize split credentials
- 2026-09-22 · P6 · the doc-coverage test caught three undocumented builtins before they could ship · nothing, it failed the build · ADR 7's mechanical rule keeps paying for itself
- 2026-09-22 · P5 review · the Jev client and its fake agreed on a protocol that does not exist; every test passed · a phase that would have failed on first contact with the API · rule for external APIs: fetch the docs and pin the wire shape in the fake's comments before writing the client
- 2026-09-21 · P3 session (766 messages) · every request re-prefilled 82–96K tokens (40–48 s) — the window sat at budget and elided the next-oldest live entry each request, and a big result ageing out of the protected tail un-elided earlier ones · ~40 s of prefill per tool call · **resolved: sticky elision from the log, high/low-water hysteresis, result caps, task block in the system prompt (ADR 9 amendment)**
- 2026-09-21 · daily use · a pasted prompt longer than 1024 characters was silently truncated — the terminal's canonical-mode line limit, not nibb's · long task descriptions could not be pasted · **resolved: the plain front-end reads prompt lines in raw mode with x/term's editor (bracketed paste, no length limit); pipes still use the scanner**
- 2026-09-21 · daily use · a `y` per edit, four to fifteen per task, makes
  the confirm a reflex · every diff skimmed, none read · **resolved:
  standing write approvals by path prefix (`writes` in allow.toml, `a` at a
  write prompt) and `t` = all writes this turn; the diff is logged and shown
  dimmed instead of prompted (ADR 17 amendment)**
- 2026-09-21 · selfhost run 1 · no way to resume a session after a crash or
  `:q`; the log has everything but the window is not rebuilt from it ·
  a crash mid-task loses the conversation · `nibb --resume <id>` that
  reloads events and continues (P10 territory, but a small version is cheap)
- 2026-09-21 · selfhost run 1 · a 27B model told to change one line
  rewrote a 400-line file with `fs-write` and mangled an unrelated line,
  then spent 10 `shell-exec` calls (sed, python3 -c) repairing it · 15
  confirms and ~25 minutes for a 9-line change · **resolved the same day:
  `fs-edit` (exact, unique, old→new with a diff preview). Re-run of the same
  task: 4 confirms, 10 tool calls, clean diff**
- 2026-09-21 · selfhost run 1 · the model wrote `/fix.py` (absolute path);
  the repo pin denied it, it then wrote `_tmp_fix.py` inside the repo and
  `rm`'d it after · two confirms for scaffolding that should not exist ·
  gone with `fs-edit`; keep an eye on "helper script" patterns
- 2026-09-21 · B2 smoke · every `go test` through `shell-exec` prompted ·
  a prompt per test run makes "y" a reflex · **resolved in B3: standing
  approvals (`.nibb/allow.toml`, `a` answers), ADR 17**
- 2026-09-21 · B2 smoke · cold prompt eval of a small prefix took 13 s
  (another client had the slot); warm turns are 0.2–0.5 s · none once warm ·
  nothing to do; ADR 9 holds
