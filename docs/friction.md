# Friction log

Where `nibb-stable` got in the way while building nibb (ADR 15). One line
per observation, newest first. Reviewed at every promotion; the top items
become side-work in the next phase.

Format: `date · session · what happened · what it cost · idea`

- 2026-09-22 · P10 summaries · older context had only irreversible sticky omission markers · external-agent implementation under ADR 16; actual provider requests tested before/after summary revocation and the real v1 fixture preserved · reviewed summaries are now source-bound log entries; revocation rebuilds from source without reviving superseded caches
- 2026-09-22 · P10 replay foundation · historical logs omitted the system prompt, tool schemas and window recipe needed to prove exact reconstruction · external-agent implementation under ADR 16; actual provider requests compared across resume and elision · record the recipe once per runtime and verify request digests before introducing revocable summaries
- 2026-09-22 · P9 parity audit · temporary approval changes were persisted but omitted by the TUI transcript · external-agent implementation under ADR 16; local/RPC scope and process-lifecycle coverage mapped in the TUI guide · render attributed approval requests and complete the line-mode flow acceptance matrix
- 2026-09-22 · persistent binding approval · new destinations required manual TOML editing and session restart · external-agent implementation under ADR 16; local/RPC, concurrent persistence, cancellation and reload checks · prompt-derived exact rows now persist on explicit consent; invalid policy denies and deletion removes authority
- 2026-09-22 · fixed Jev scope · host-only metadata skipped method and position authorization · external-agent implementation under ADR 16; all three primitives tested against mismatched bindings · require POST and header:Authorization:Bearer before transport
- 2026-09-22 · P9 egress confirmations · unbound credential prompts lacked explicit method and position; Jev still omits these fixed facts from guard metadata · implemented with an external agent under ADR 16, with local/RPC canary coverage · show exact available facts and explicit omissions before adding any binding-writing operation
- 2026-09-22 · P9 plan/budget views · implemented with an external agent under ADR 16; local/RPC and terminal checks exercise configured limits, a real checkpoint stop, history isolation and gate-score labels · no claim of nibb self-authorship · inspect recorded planning and budget decisions without treating snapshots as live usage
- 2026-09-22 · P9 tool-call presentation · implemented with an external agent under ADR 16; local/RPC and terminal checks cover nested guards, a declined write, history reconstruction, reused IDs and canary redaction · no claim of nibb self-authorship · tool results are readable beside their arguments and recorded decisions instead of only a byte count
- 2026-09-22 · P9 tool discovery · implemented with an external agent under ADR 16; local/RPC tests cover initialization tools, schemas, safe rendering and inspection without execution · no claim of nibb self-authorship · inspect the model-facing catalog and use familiar plain-mode command aliases without leaving the TUI
- 2026-09-22 · composer promotion · the live model returned READY, but selfcheck replayed a newer unrelated session from the shared store · stable swap correctly stopped · select the ID printed in the selfcheck session banner; regression coverage includes a newer unrelated session
- 2026-09-22 · P9 composer · implemented with an external agent under ADR 16; local/RPC tests and real terminals cover multiline Scheme comments, bracketed paste, resize and separate approval/secret input · no claim of nibb self-authorship · pasted newlines no longer collapse, and prompts/reconnects preserve unsent drafts
- 2026-09-22 · P9 inspectors · implemented with an external agent under ADR 16; local/RPC tests cover fixed history snapshots, isolated bindings, prompt priority and denied friction reads · no claim of nibb self-authorship · log, replay, loaded policy and friction can be inspected without leaving the TUI or bypassing the guarded read path
- 2026-09-22 · P9 first client · implemented with an external agent under ADR 16; real-socket, local-session, masked-canary, cancellation and PTY checks exercise the new frontend · no claim of nibb self-authorship · scrollable approvals and resumable daemon attachment now work in a terminal; inspectors and richer composing remain
- 2026-09-22 · U2 turns/events · implemented with an external agent under ADR 16; fake-model and real-socket tests cover reconnect gaps, waiting prompts, slow clients, abort and close · Busy could become false before final idle delivery, so close now waits for completion · approval replies remain the next slice
- 2026-09-22 · U2 lifecycle · implemented with an external agent under ADR 16; real-socket tests cover disconnected clients, concurrent resume, private permissions, writer locks and process shutdown · no claim of nibb self-authorship · daemon sessions can be owned independently of a terminal; turn/event RPC is the next slice
- 2026-09-22 · ADR 18 budgets · implemented with an external agent under ADR 16; focused tests cover nested host effects, custom choices, denial accounting, cancellation, deadlines, redaction and replay · no claim of nibb self-authorship · long supervised turns now checkpoint before exhausting hard ceilings
- 2026-09-22 · CI validation · archive cache restore cannot overwrite existing read-only Go module files on the restricted host runner · warning and redundant archive work despite passing tests · disable Actions archive caching and reuse the runner's normal local Go caches; extract tool archives without restoring build-machine ownership
- 2026-09-22 · ADR 18 progress guard · implemented with an external agent under ADR 16; fake-service tests cover repeated calls, denied retries, error streaks, skipped batch results and fresh turns · no claim of nibb self-authorship · stops unproductive turns before consuming the full hard budget
- 2026-09-22 · Gitea CI · configured by an external agent under ADR 16; the general runner uses Alpine and the explicit self-hosted label · the GitHub-only workflow did not establish a Gitea gate · run fresh acceptance checks and all four portable build targets for every push
- 2026-09-22 · U1 resume · implemented with an external agent under ADR 16; tested CLI restart, process kill after a real tool write, legacy redaction, partial-tail recovery and exclusive ownership · no claim of nibb self-authorship · conversation survives restart; ephemeral approvals and Scheme state deliberately reset
- 2026-09-22 · readability refactor · implemented with an external agent under ADR 16 after reviewing the spec and roadmap; acceptance and fresh race tests passed · no claim of nibb self-authorship · preserve shared credential and integration boundaries while simplifying the code inside them
- 2026-09-22 · P7 · implemented with an external agent under ADR 16, validated with the candidate selfcheck · no claim of nibb self-authorship · model output is now buffered because per-token redaction cannot safely recognize split credentials
- 2026-09-26 · loop policy · changing how a turn goes (plan first, check with the judge, stop a batch at its first failure) meant editing `internal/loop` in Go · every experiment with a model or judge was a rebuild and a promotion · **resolved: the loop is a Scheme procedure, `agent-turn`, over three primitives that keep the invariants (ADR 26, docs/scheme.md §9)**
- 2026-09-26 · benchmarks · `nibb run` could not complete unattended at all: a run's first write raised a confirm that waited for a client, so the budget killed it · supervised runs unusable without a human watching, which is the opposite of their purpose · **resolved: `run.start` takes `auto_approve`, exposed as `nibb run start --yolo`; the keychain lives in the daemon so a run has credentials without a passphrase prompt**
- 2026-09-26 · benchmarks · `--no-confirm` answers every confirm with no, which is right for CI and useless for a harness that must actually edit and run things; there was no unattended mode at all · SWE-bench and any test rig blocked · **resolved: `--yolo` answers ordinary confirms yes and leaves every denial and every security confirm alone**
- 2026-09-26 · authoring · a multi-line instruction or tool description had to be written as one escaped line · prose in a program read badly enough that nobody wrote any · **resolved: `"""…"""` raw strings with indented-block dedent (docs/scheme.md §1.1)**
- 2026-09-25 · browser console · the UI was unpleasant to use and to look at: no dark mode at all, ~220px of chrome before any content, prose running the full window width, telemetry as loud as the answer, a 100px card per tool call hiding both arguments and result, four-line sidebar rows repeating the repo path, and the same hint shown twice · every session read worse than the plain front-end · **resolved: dark-first operator console — one token set with a real light peer, thin chrome, turn-grouped transcript, one-line tool rows carrying verdict, size and duration, approvals with the buttons above the fine print**
- 2026-09-25 · terminal client · every tool call dumped its indented arguments and its whole result into the transcript, so one 13 kB file read buried the conversation · scrolling past tool output to find the answer · **resolved: compact one-line tool rows, `:details` toggles the bodies, session rows keep a reserved meta column**
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
  **resolved in U1: reloads the conversation from its log without replaying tool operations**
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
- 2026-09-22 · U2 approvals and eval · implemented with an external agent under ADR 16; Unix-socket tests cover peer attribution, explicit scopes, secret containment, cancellation and schema drift · no claim of nibb self-authorship · clients can now complete prompted turns and register credentials without blocking their reply connection; turn-wide write trust no longer bypasses security confirmation

- 2026-09-22 (external agent, ADR 16): searching through general shell commands required broad command approvals; `index-grep` now performs bounded ripgrep retrieval with every selected content read passing the existing guard. Tree-sitter symbols remain separate work.

- 2026-09-22 (external agent, ADR 16): source outlines previously needed whole-file context or shell tools; `index-symbols` now returns guarded Tree-sitter definitions for Go, JavaScript and Python without runtime downloads or a native build toolchain.

- 2026-09-22 (external agent, ADR 16): the daemon had no browser view of saved/live work; U3 now has a paired read-only session/transcript desk with guarded transport boundaries. Interactive approvals remain the next slice.

- 2026-09-22 (external agent, ADR 16): testing the guarded browser friction view exposed an evaluation-result redaction gap: Scheme newline escaping could hide a known secret's token boundary. Session evaluation records and plain chat evaluation displays now scan string leaves before quoting, including nested pairs and vectors; evaluation values remain unchanged. Synthetic canary tests cover frontend events, history, persisted output and linked redaction audits.
