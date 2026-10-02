# ADR 0032: The default loop works the way pil did

- Status: **accepted**
- Date: 2026-09-30

## Context
On a SWE-bench Verified cohort run locally (the 121 tasks whose gold patches
validate, Qwen3.8 27B at Q4, one attempt each), nodwyn's defaults were tuned
through Scheme strategy files over the unattended-defaults branch. The
combination that held up, called pil, resolved 86 of 121, against 87 for
Pi, a separate harness, on the same model and tasks. Each part came from a
failure seen in the logs:

- the model sent command lines through `shell-exec`'s `cmd`/`args` (8–15
  malformed calls per task on some tasks) and read results as JSON strings
  with escaped newlines;
- test output and reads of code that turned out not to matter filled most
  of the main context;
- scripts that searched a file for a definition and printed it, where one
  call would do;
- tests run with the system interpreter, then worked around with patched
  standard libraries or installed packages;
- a plan written without running anything overriding what the model's own
  checks showed;
- checks retyped each time, paraphrasing the request's example, and a
  compaction note that kept "it does not reproduce" and dropped the check
  it rested on;
- compaction at half the window stopping runs that would have finished.

The parts lived in a benchmark harness's strategy files, outside the
project, while the project moved on to the console, workspaces and the
rename.

## Decision
pil becomes nodwyn's default, for every session, interactive or unattended:

- The unattended-defaults branch is merged: the unattended prompt, evidence
  to finish on, sub-agents that end only themselves, tool offers per
  request, and `fold-result`.
- `working-rules` (prompts.scm) joins both system prompts: shell command lines, the project's environment, reading with
  the file tools, a plan as a first guess, the check loop, following the
  request's conclusion, widening in general form, whole test modules,
  comparing failures with the unchanged code, and finding regressions file
  by file.
- The working habits load with the default loop (docs/scheme.md §9.6):
  `shell.scm`, `hygiene.scm`, `readdef.scm` (the `read-definition` tool),
  `envhint.scm` and `checkloop.scm` (the `second-look` tool).
- Handover notes keep each finding's evidence and name the check script,
  and the compacted context re-checks the note before building on it.
- Compaction is on by default, a twenty-fifth below the window's high water,
  read through a new primitive, `(window-limits)`; `(model-info [name])` and
  `(model-names)` give strategies the configured models' properties too.

## Consequences
- The system prompts are longer by about 3,900 characters, and every
  session offers two more tools.
- The interactive prompt gains rules measured only unattended; the check
  loop in particular asks for a script before changing code.
- Long interactive sessions now compact instead of only trimming; a project
  that prefers trimming sets `compact-threshold` to `#f`.
- A project's `.nodwyn/init.scm` written for the old defaults still loads,
  and wraps the habits as they wrap the defaults.
- A leave-one-out ablation of pil's parts on the same tasks is queued; a
  part that does not pay its way there is a candidate for removal.

## Amendment 2026-09-30: compaction measures what the window trims by

Compaction compared the server's `prompt-tokens` for the last request with a
threshold taken from the high water, which is in the window's own estimate
(bytes/3). The two differ: on meta/muse-spark-1.3 through OpenRouter the
estimate ran about a quarter above the server's count (53,339 against
42,661 for one request), so the window crossed its high water and elided 20
messages while the server's count was near 43k, far under the 58,983
threshold. Compaction never ran, and old reads were dropped rather than
summarized. The last request's size also lacks the tool results just added:
that request was already under the threshold in the window's own units, and
one round of reads took the next one past the high water.

Now `maybe-compact` measures `(context-tokens)`: the window's estimate of
the context the next `model-turn` would send, before any trim, in the units
of `(window-limits)`, whatever the provider's tokenizer. The handover note's
request is asked with `'((headroom . #t))`, so it may run past the high
water up to the window's ceiling, the room the model's context has left
above it, and a round that jumps past the high water is still compacted
rather than trimmed (`llm.req` `high_water` records that request's trim line;
window-v10).

The window is also sized for the model: a 60k window on a 1M-token model
compacted long before the model needed it. When `[control]` gives
`num_ctx`, or a chat-completions server's model list gives the context
length (OpenRouter's `context_length`, vLLM's `max_model_len`), the high
water grows to three quarters of the context less the reply cap, the low
water to half that, and the last quarter is the ceiling's room for the
note. `[window]` water marks set in config.toml are kept as set. A context no
bigger than the window, such as a local model sized by it, has no such room:
there a round that jumps past the high water still trims before the note is
written, unless the reply reserve is given up for it.

## Amendment 2026-10-01: the evidence loop reads the scratch directory and git

The check loop asks for one script in `$TMPDIR`, but the evidence loop knew
only `test-command-words` and counted any `fs-write` as a change: writing
the check reset "tested", and `bash $TMPDIR/check.sh` never set it. In the
pil run on the 121 tasks, 18 of the 23 "You changed files after your last
test run" reminders (12 tasks) came after the model had run its scratch
check since its last edit to the repository. Now a write into the scratch
directory is not a change, a command that runs a file written there is a
test run, and a failing check, which fails until the work is done, never
counts toward the way out for tests that cannot run.

In a git work tree the loop also asks git (`git-status`, read by the
strategy, not offered as a model call): something changed when a tracked
file's status differs from the start of the turn, or the file tools wrote a
file git now lists as new or listed already. An edit through the shell
counts; a write of the same content, or a file a run leaves behind, does
not. Files new since the turn began that git does
not track are named once (`finish-new-files-message`). In the same run, 22
tasks left such files: changelog entries, new test roots and `*_tmp.py`
repro tests. The benchmark's patch is `git diff`, which leaves untracked
files out, so this note does not move that score; it is for repositories
where what is left in the tree is the change. Both system prompts gain a
rule for programs that keep running or may hang (`shell-start`) and, in a
git work tree, `(git-rule)`: look at the change with `git-status` and
`git-diff` before finishing.

Hygiene was measured on the same run before changing it, and stays as it
is. Reads folded in 22 of the 121 tasks: 137 folds (65 of them stale),
0.87 MB. 106 later reads went back to a folded passage, mostly as ranges,
0.28 MB; counted as bytes times the requests they stay in the context, the
folds keep 66% of what they save after those re-reads. A request right
after a fold took a median 3.2 s of prompt evaluation against 0.4 s
otherwise, about 30 such requests over the run. Long read-only sessions
differ: on a repository review with meta/muse-spark-1.3 (32 rounds, no
edits) 24 of 28 re-reads followed a fold of that file, 16 of them whole
files, since nothing is edited and every read ages out.
