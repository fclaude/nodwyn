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

## Amendment 2026-10-02: a fold waits until it pays for the cache it costs

A fold changes a message in the middle of the prompt, so a server with a
prompt cache reads everything after it again at the full input price. In
the review described in ADR 9's amendment of 2026-10-02, 12 of 34 requests
followed a fold. Given what nodwyn sent, at most 64% of the prompt tokens
could have come from the cache, against 93% had every request kept its
predecessor's prompt. Each fold cut the reusable prefix back to the oldest
read it folded, eight rounds back or more, so the server read 26,000 to
50,000 tokens again each time. `hygiene-min-bytes` also counts a read's raw bytes, while the
window shows a read longer than `max-result-tokens` shortened: the 109,563
bytes of the first fold were about 11,000 tokens in the window.

Keeping F tokens costs F times the cached price in each later request.
Folding them costs the A tokens after them, less F, once at the input
price less the cached price. With cached tokens at a tenth of the input
price, a fold that is half of what follows pays for itself after 9 more
requests; at a fiftieth (meta/muse-spark-1.3), after 49. A smaller fold
takes longer. Without a working cache there is nothing to lose, and the
fold saves the full price of F on every later request.

So a due batch now folds when at least one of these holds:
- the fold is at least as large as the rest of the window after it
  (`(context-tokens result)`, new: the window from a result to its end, in
  the window's units, with reads counted as the window shows them);
- the provider's cache is not serving the context: it read less than
  `hygiene-cache-share` (a quarter) of the last `hygiene-cache-requests`
  (4) prompts in the main context;
- the context is past `hygiene-pressure` (half) of the high water, where
  room matters more than the cache.

A provider that reports no cache, such as Ollama, counts as serving the
context, since this ADR's window is built for its prefix cache. On a local
model reads therefore wait until a batch is half of what follows or the
context passes half the high water. On the 121-task cohort that is a change
from the measured pil configuration and has not been run yet. A run with the
old rule is `(define (hygiene-fold-reason due) "always")` in `init.scm`.
Each fold's log note now ends with the reason.

## Amendment 2026-10-02: what three runs of one small task showed

The same request ("make it easier to configure the jev key in the ui ...
Propose something before working on the changes") went through a
six-turn run, a one-turn run and a chat, and failed each time for reasons
in the harness, not in the request:

- **The model's thinking is not kept.** It is logged, not sent back, and a
  reply that only calls tools says nothing. In the chat's second turn the
  model worked out the same design in nearly every one of 45 requests and
  changed nothing. The working rules now say so, and the model writes its
  plan with `plan-write`, an agent tool that records it (`gate.plan`,
  decision `model`) where the window pins it and `plan-step` marks it.
- **The evidence loop overrode "propose first".** A model that proposed
  and stopped was told "No file has changed yet. Make the change", and
  started implementing what nobody had approved, or wrote the proposal
  into the repository to have a change. When a person reads the answer
  before anything else happens, `(reviewed?)` (a chat, or a supervised
  run's turn that a checkpoint follows), the reminder now says the request
  may ask for no change yet, and after it the turn may end unchanged. An
  unattended run, and a run's last turn (SWE-bench runs one turn), still
  need a change.
- **The explorer's findings reached only the planner.** The model doing the
  work read the same files again within a minute. They are now handed to
  it once the plan is recorded (`plan-findings-message`).
- **The denylist refused the task's own files** (`credentials.js`,
  `credentials.go` matched `credentials.*`), so the model read and edited
  them through the shell, which the person had to approve for the whole
  session, and `git-status` left them out, so the evidence loop said nothing
  had changed after the model changed one. Code no longer matches the name
  globs, and `git-status` names every file (ADR 28, amended).
- **nodwyn's own tests failed when nodwyn ran them**, for reasons the
  model could not tell from its own breakage (ADR 28, amended).

## Amendment 2026-10-02: a change is checked where it runs

The same jev-key request, done in a chat once the harness fixes above were
in (session `20261003T002506Z-f564a28c`, meta/muse-spark-1.3), was
reviewed four times, and each review found what the model's own checks
could not:

- **The page crashed on a click.** One edit named a text `detail` in the
  function that also called the module's `detail()`, so storing the judge
  key or opening its row blanked the Credentials tab. Go's tests and
  `node --check` passed in every round: neither runs the console's
  JavaScript. The repository's browser check for that tab
  (`test/browser/checks/credentials.js`) was not run until the fourth
  turn; once it was run, and extended to the judge panel, it found the two
  defects that were left within minutes.
- **The replies reported checks that could not show what they claimed.**
  "All review items fixed" rested on `node --check`.
- **Unanswered questions were settled silently.** The proposal ended with
  three questions; told "go do it!", the model chose without saying so, and
  one choice (no key field while the judge is off) left Settings linking to
  a panel without a field.
- **It added what nothing used**: a function for a step of its own plan,
  and a field the page did not read.
- **It stashed the work around tests that had passed**, to compare with
  the unchanged code, which the rule asks for only when tests fail.

So the prompts change:

- The working rule on whole test modules adds that a build or a syntax
  check shows code is well formed, not that it works, and that code a
  browser runs is checked by the project's browser or end-to-end checks,
  extended to the states the change adds; with none, the page is reported
  unchecked.
- The comparison with the unchanged code is "only when tests fail".
- `(git-rule)`: the change holds nothing it does not use.
- The interactive prompt: the answer says what changed, how it was checked
  and what was not, and when the user goes ahead without answering a
  choice, the model chooses and says what it chose.

The system prompt's test no longer pins its text byte for byte. The copy
showed that the move of the prompts into Scheme (2026-09-26) reproduced the
Go strings; after that it only made every edit twice. The test now checks
that the session sends what `(system-prompt repo)` builds, that two
sessions of one repository send the same prompt (the prefix ADR 9 caches),
that sections that do not apply are left out, and what the planner's and
runs' prompts must carry.

None of this has run on the 121-task cohort. The working rules grow by
386 characters in both system prompts and `(git-rule)` by 87 in a git work
tree; the interactive prompt grows by 217 more. On the benchmark's tasks
there is no page to check.

## Amendment 2026-10-02: rounds that only read get a reminder, a question an answer

A chat request ("we seem to have support for worktrees, but the UI doesn't
seem to allow me to enable that", session `20261003T052943Z-4e994237`,
meta/muse-spark-1.3) ran for nine minutes and 40 requests, 99 calls, every
one a read, a search or a listing, and was stopped without a word to the
person: every reply's text was empty. Its thinking at the sixth request
already said what the answer was (ADR 34 is proposed, and only the git work
tree check exists); at each of the next 34 it said the same thing in other
words and read more to confirm it. The rule that the thinking is not kept
(amendment above) did not apply: it tells the model to write its plan once
it knows what to change, and this model never decided whether there was
anything to change. A fold at request 34 then took out 33 reads, and it read
five of those files again. The pattern is not one session's: the jev-key
chat (`20261003T002506Z-f564a28c`) made 402 requests with five replies that
said anything, its first edit at the 30th.

Nothing in the loop looked at this. The plain loop asks again while a reply
calls tools, and the evidence loop acts only on a reply that calls none. So
both now count the rounds in a row whose calls all only look
(`look-tools`: `fs-read`, `fs-list`, `fs-find`, `index-grep`,
`read-definition`, `git-status`, `git-diff`; this repository adds its
`grep-repo`, `find-files`, `read-adr` and `git-log`), and after
`reading-reminder-after` (12) add a message: the thinking behind them is
lost, so write it down now, with `plan-write`, and start; when a person
reads the answer (`(reviewed?)`), answer now if the request asks a question
or needs the person's decision. Any other call starts the count again, and
so does the reminder, which comes again after twelve more.

Twelve is the explorer's budget before a plan (`plan-explore-rounds`), and
twice the rounds the worktree turn needed. Counted over the last nine
sessions on this repository, the reminder would have come four times in the
worktree turn (at its 12th request, not its 40th), twice in the first turn
of two of the three "give me 5 things this harness is missing" questions,
which ask for an answer and no change, and in nine of the fifteen jev-key
turns, where it asks for what the prompt already asks: a written plan. Four
sessions never read for twelve rounds in a row.

None of this has run on the 121-task cohort, where the evidence loop runs
too: a task that reads for twelve rounds now gets the unreviewed reminder.

The reminder comes after twelve rounds; the interactive prompt now says
what to do before them. The worktree request expected something that had
only been designed, and the model went back and forth between saying so
and building it. A question is answered; a request that expects something
never built is told so, with what exists, what is missing and what the
model would build, before anything is built; and either is answered as
soon as what the model has read settles it. A request that reports a
defect in something that exists is still fixed. The rule, 357
characters, is in the interactive prompt alone, so the benchmark, which
runs unattended, does not see it.

ADR 9 (amended 2026-10-02) now sends the thinking back on its reply, as the
server reported it. The working rule, the reading reminders and
`plan-write`'s description said "your thinking is not kept"; they now say it
comes back only as the server reported it, often a summary or not at all,
which is true of every provider, and still ask for the plan in writing.
