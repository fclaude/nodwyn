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

## Amendment 2026-10-03: a turn with a plan is told to go on with it

The reading reminder (amendment above) said "write your plan with
`plan-write` now" whether or not the turn had one. Writing a plan again
starts its marks over. In session `20261003T063228Z-73791682`, after the
turn whose plan it lost to a provider's rate limit (ADR 35, amended the
same day), the model had written its plan again and marked three of five
steps. It then read for twelve rounds, every one of them for step 4, and
was told to write its plan. It wrote it again (seq 6042) with "[done]"
typed after the first three steps, since the marks were gone. The
console's plan panel showed no step marked. Earlier in the session the
reminder at seq 3496 had the model rewrite an unchanged plan.

A turn that has a plan (`(plan-recorded?)`) is now told that the plan is
in front of it: make the change for its next unmarked step, read more only
for that step, and write the plan again only if it must change, since that
starts its marks over. Without a plan the reminder is as before, word for
word. This includes the unreviewed reminder of the evidence loop, so on
the benchmark it changes only for turns that have a plan. A run's turns
always do, and there the reminder no longer asks for a plan the gate
already drafted.

## Amendment 2026-10-03: plans name their tests and docs

The worktree sessions of 2026-10-03 (`20261003T143916Z-48f31857` and
`20261003T063228Z-73791682`, reviewed and reverted) planned in stage
labels. The plan recorded at seq 1344 of `48f31857` read "2. WT0:
repository-behind-checkout lookup (git common dir) and key project data on
repository, with tests", and its last step was "Docs (manual, scheme,
protocol, browser, ADRs) and full validation". No step said which test or
which doc, and what followed shows what that cost:

- ADR 34's specific clauses (the stash habit, the `.gitignore`) never
  became steps, and were dropped.
- A test written for "a clean close keeps attachments" passed on the code
  that deleted them.
- The browser check only looked for the new checkbox, so it passed while
  the toggle did nothing.
- A new primitive was missing from the book's and the expert guide's
  tables, and a new working rule had no amendment here.

The plans that worked, the handoffs written after those reviews, all had
one shape: steps that change code, test steps that each say what they check
and why they fail on today's code, docs steps that name the files, and a
last step that runs the checks.

So every plan is asked for in that shape: numbered steps, each starting
with its kind, `Change:` (a file or function and what changes in it),
`Test:` (the test file and name, the behaviour it checks, and "fails
today: …", or "passes today: guards …" for a test of behaviour that already
works), `Docs:` (each file a person or a later session reads that the
change makes wrong or incomplete) and, last, `Check:` (the command that
shows the whole change works). A plan with no test or no doc to change says
so in a step, with the reason (`Test: none — only docs change`).

- `plan-write` checks the shape (`plan-problems`, over the steps
  `(plan-steps plan)` reads, the same reading `plan-step` marks). A step of
  no known kind, no `Change:`, `Test:` or `Docs:` step, a last step that is
  not a `Check:`, or a `Test:` step that says neither "fails today" nor
  "passes today" (or the same "on the unchanged code") turns the plan down,
  unrecorded, with each problem by step number and what to add. After two
  refusals in a turn (`plan-write-refusals`) the next plan is recorded as
  written, answered `Plan recorded: ` and what it still lacks, so a model
  that cannot get the shape right is not kept from the work. Its description
  states the shape with a two-line example.
- `plan-step`'s description says a `Test:` step is done once its test has
  failed without the change and passes with it, and the note names both
  runs.
- The gate's planner is asked for the shape (`(plan-shape)`, built from
  `plan-kinds`, in `plan-draft-prompt` and `plan-revise-prompt`; a run's
  replanning asks for what remains in the same shape), and the explorer
  reports the docs involved and what the repository's agent instructions
  ask a change to include, since the planner sees nothing else. The
  judge's rubric asks for the shape, and a plan whose tests do not say what
  they check, or that leaves out the docs, does not reach its top level.
- The working rules and the reading reminders still say "write your plan
  with `plan-write`" and nothing more: the tool's description and its
  refusals carry the shape, and the rules stay short.
- The kinds, the wording a `Test:` step may use, the refusal count and the
  messages are definitions in `plan.scm`, so a project redefines them in
  `.nodwyn/init.scm`; `(define (plan-problems steps) '())` turns the check
  off. `record-plan` checks nothing: project code is trusted as written.

The check reads what a step says, not what it does: nothing runs a
`Test:` step's test on the unchanged code to see it fail.

This has not been measured on the 121-task cohort. There every instance is
a supervised one-turn run, and in pil's run (`nibb-1fd0696-d-pi121-pil`)
all 121 recorded a gate plan, decision `required`, unscored (no judge), and
none called `plan-write`. So the benchmark sees the planner's draft prompt
(138 characters before the request, now 656), the explorer's task (215,
now 355), `plan-write`'s description in every request's tool list (382,
now 1028), and its check whenever the model writes a plan of its own; the
rubric and its levels (182 characters, now 517) apply only with a judge.

To measure it, run the cohort twice on the same model and instances as
pil (Qwen3.8 27B at Q4, `qwen3.8-27b-96k:latest`, the 121 tasks, one
attempt each): once at the commit before this change and once at this
change's commit. Compare:

- the resolved rate, from each run's tagged eval (`resolved_ids`);
- requests per instance, `llm.req` events per session log (pil: mean 50.7,
  median 41), main context and sub-agents apart;
- how often plans were turned down: `plan-write` results that start "Plan
  not recorded", and those recorded "as written" after two refusals, per
  instance;
- whether `Test:` steps' tests fail on the unchanged code: for each
  `Test:` step in a recorded plan, the first `shell-exec` that names its
  test file or test before the first edit outside the tests, and its exit
  status. The patch alone cannot show it: the bridge builds it with
  `git diff`, so a new test file is not in it.

## Amendment 2026-10-03: a piped command reports its failure, and leaves nothing running

The shell rule above says to keep long output short with `| tail -n 40`.
A pipeline's status is its last command's, so a check piped that way
reported 0 when it failed. In session `20261003T190332Z-547f0bd6` two runs
of a failing browser check came back as status 0. The model read the text
that time, but the evidence loop's `ran-ok?` counts a piped test run by its
status. A `bash -c` command line through `shell-exec` or `shell-start` now
runs with `pipefail`, so its status is that of its last command that
failed. The rule's text is unchanged. Only bash's `-c` form changes: a
script keeps its own options, and `sh` may be a shell without `pipefail`.
A pipeline into `head` can now end in 141 when `head` stops reading early,
which is why the rule says `tail`.

`shell-exec` ran its command in a process group of its own, but killed the
group only on a timeout. The same session started the browser checks' demo
with `&`, and it kept running after the session ended. A child that kept
the output open made the call wait five seconds and then fail with "WaitDelay
expired". Now the call returns when the program ends, and what it left in
its group is stopped: SIGTERM, then SIGKILL two seconds later, as
`shell-stop` does. A program meant to keep running goes to `shell-start`,
as the rule above already says.

Neither change has been measured on the benchmark. The working rules are
unchanged; `shell-exec`'s own description now says both.

## Amendment 2026-10-03: in a worktree, no stash

A session started in a git worktree of its own (ADR 34) adds
`(worktree-section)` to both system prompts, after `(git-rule)`. Besides
naming the branch and saying that other sessions may work in other
worktrees of the repository, it overrides one working rule there: compare
with the unchanged code on a copy of the last commit (`git archive HEAD |
tar -x -C "$TMPDIR/base"`), never with `git stash`. The failure it comes
from is ADR 34's: every worktree of a repository shares one stash, so the
rule's `git stash; <command>; git stash pop` pops whatever another session
stashed last, bringing its work into this one or taking this one's away.
A session working on nodwyn itself is warned of the same trap in
`AGENTS.md`. Outside a worktree the section is `""`, so the measured
prompts are unchanged byte for byte; `working-rules` keeps its stash
example. The section has not been measured on the benchmark, whose runs
start in a folder of their own.

## Amendment 2026-10-04: plan marks move as the work happens

The plan header asked for each mark "as you finish it", and the model read
that as a summary to make at the end. In session
`20261004T053047Z-1b4c67dc` (meta/muse-spark-1.3 through OpenRouter) the
gate recorded a plan of six steps at 05:36:43 and the model marked steps
1–5 together at 06:34:25–06:34:42 and step 6 at 06:36:53: the last two and
a half minutes of work that had run for an hour. Whoever followed it saw
the console's plan panel sit at 0 of 6 until the work was over, which is
the progress the marks are there to show.

The wording that asks for the marks now says when: `plan-header`,
`plan-recorded-message` (both branches), `plan-step`'s description and the
run turn prompt say to mark each step as its work is done, before starting
the next, never several together at the end; `plan-step`'s description
says why — whoever follows the task sees the plan's progress move as the
work happens — and `plan-write`'s description says its marks follow the
work. The working rules are unchanged, so the prompts measured on the
benchmark stay byte for byte; this wording has not been measured there.
## Amendment 2026-10-04: plans give each step a short name

The plan panel folds each step to its title, the clause before its first
dash, and hides the rest behind that fold. The plans the person
screenshotted on 2026-10-03 had no such clause: steps read "Change: read
internal/frontend/rpc/workspaces.go, the desk state builder and …", the
work itself as the title, cut at the console's 80-character cap, with
nothing to fold to. The kinds were right; the shape said nothing about the
name, so neither the planner nor `plan-write` asked for one.

Now each step gives a short name for what it does, then a dash (—) and the
detail after it; a step that says all it should in its name needs no dash.
The name is what comes before the first boundary the console folds a step
on (a dash with a space before it, or a sentence end or semicolon with a
space after), the kind included, and it stays within 80 characters, the
console's cap, so what `plan-write` accepts is what the panel shows whole.
A step of a known kind whose name runs past that turns the plan down with
the other shape faults: `plan-name-cap` and `plan-name-boundaries` hold
the number and the boundaries, `plan-name-length` measures a step's name,
and `plan-name-problem` answers with the step's number and what to write;
after `plan-write-refusals` the plan is recorded as written, as before.
The planner is asked for the name in `(plan-shape)`, and the judge's
rubric and its top level ask for it too. `plan-write`'s description and
its `plan` parameter say the shape; the example in the description already
carried the dash.

The check reads what a step says, as the others do: nothing runs the
console to see what it would fold. A step with no boundary that stays
within the cap is its own name and is accepted, so `Check: go test ./...`
needs no dash.

This has not been measured on the 121-task cohort; the caveats of the
amendment that asked plans to name their tests and docs apply. The
benchmark sees `plan-write`'s description grow by about 75 characters in
every request's tool list, and its `plan` parameter by about 55; the
planner's draft prompt grows by about 135.


## Amendment 2026-10-05: a question gets no plan, and a reply is read with what it answers

The plan gate turned a question into a change. Asked "review all setting
options against the documentation, what makes and what doesn't make sense
anymore?" (sessions `20261005T054223Z-36b27caa` and
`20261005T060116Z-ef99f036`), the judge said it needed a plan, and a plan
in the shape this ADR asks for (amended 2026-10-03) is a plan of changes:
the model, told to follow it, wrote a review document into the repository,
tests and a dozen documents' edits, where the working rule says a question
is answered (amended 2026-10-02). In a chat's later turn the gate read the
person's message alone, so "folding, to reduce visual polution for the
user", a reply to the model's question, was judged and would have been
planned without what it answered (session `20261005T060224Z-2f9eb922`).

`plan-needed-question` now asks whether the request means changing the
repository and says a question, a review, a comparison, an explanation or
a proposal needs no plan; and the gate's judge, explorer, planner and score
read a chat's request after the person's two messages before it
(`plan-earlier-requests`, `plan-earlier-cap`, `plan-chat-request`,
`plan-request-with-earlier`). ADR 19 (amended 2026-10-05) has the cases and
what the judge of a command is told. No working rule changed. The
benchmark's runs plan on every turn without asking the judge whether to,
and read their own task alone, so the 121-task cohort sees none of this.

## Amendment 2026-10-05: the explorer's rounds and the reminder's count are tables by model family

Twelve rounds was one number for models that batch their calls very
differently. In the 73 session logs on this machine (2026-09-30 to
2026-10-05), calls to a round in the main loop were 2.75 for gpt-6.1-sol,
1.85 for gpt-6-luna, about 1.9 for claude-sonnet, 1.37 for muse-spark, 1.22
for glm-5.3-flash, 1.36 for qwen3.8-27b and 1.29 for deepseek-v4.1-flash. Of
the 16 explorer runs, gpt-6.1-sol (5) finished in 7 to 10 rounds at 5 to 6.6
calls a round and claude-sonnet-5-5 (2) in 4 and 10, but 5 of 7 muse-spark
runs (3.4 calls a round) and both gpt-6-luna runs (4 to 5) were stopped at
the twelfth with calls still asked for, and wrote their report from what
they had (session `20261005T153019Z-08490dd4` is one). The console showed
those skipped calls as failures (ADR 21, amended 2026-10-05).

`plan-explore-rounds` is now a table (`(tuned knob)`, `model-family-id`;
docs/scheme.md §9.7): 16 for the muse-spark, glm, qwen, deepseek and
gpt-6-luna families and the default, 12, for the rest, gpt-6.1-sol and
claude-sonnet among them. The uncapped muse-spark runs ended at 10 and 11
rounds, the capped ones needed more than 12, and 16 is a first step, to be
read against the next logs; glm, qwen and deepseek have no explorer run
there and share muse-spark's row because they batch as it does (1.2 to 1.4
calls a round). A number still works for any model and 0 still turns the
explorer off.

`reading-reminder-after` is a table too, with no family row. The reminder
at 12 ended the reading in the next round for 8 of 11 muse-spark turns and
for 16 of 27 glm, 5 of 13 qwen and 0 of 2 deepseek turns, which another
count would not change, and gpt-6.1-sol and gpt-6-luna never read 12 rounds
in a row (their longest runs were 7 and 5), claude-sonnet once. Scaling it
by calls to a round would raise it for the families it works for. A row is
one line when a family's logs show 12 is wrong for it.

The local logs hold 16 explorer runs, six of them from one session, and
show nothing about whether a stopped explorer's report is worse: the main
loop reads 3 to 64 files after the explorer whether it was stopped or not.
No benchmark ran with this.

## Amendment 2026-10-05: a go test that ran is not folded as one that ran nothing

Hygiene's first rule folds a test command that never got to run a test
once a later one does, and it knew a run by pytest's and unittest's words
only ("passed", "failed", "ran "). go test says "ok  <tab>pkg", "--- FAIL"
and "=== RUN", so every go test run was taken for one that ran nothing:
138 of the 151 results folded as "this test command did not run any test
()" in the 75 local session logs of 2026-09-30 to 10-05 were go test runs
that ran (20261002T192800Z-920da7d8 from seq 8654 on, for one), the
failing-before evidence of a fix among them, and each such fold rewrote
the prompt behind the cache (20261005T151325Z-d9519026 seq 4350 and 4642
came back with nothing cached).

- A test command ran tests when it exited 0, since only a command that
  failed may have failed before running one, or when its output says so in
  a runner's words (`hygiene-ran-words`, now with Go's, mocha's, jest's,
  node's and cargo's).
- go test's "[build failed]" and "[setup failed]" say a package ran no
  test, so their "failed" does not count (`hygiene-unran-words`).
- The fold's reason is the end of stderr, or of the output when stderr is
  empty, as go test reports a package that does not build: the folds above
  all said "()".

TestGoTestRunsAreNotFoldedAsRunningNothing runs a passing and a failing go
test, a package that does not build and a pytest run: only the build is
folded, and its fold names "[build failed]". On the unchanged code the two
go test runs are folded instead.

## Amendment 2026-10-05: the reads of a file being edited stay

Hygiene folds a stale read (of a file edited since) two rounds after it, and
any read eight rounds old. Both sent the model back to the file it was
editing: transcript.js was read 49 times in session
20261005T004207Z-f0ddf504, each fold followed by four or five ranged
re-reads (seq 474-492, 560-578, 615-633, 704-722), and "old text not found"
clustered after the folds.

Neither rule now folds a read of a file edited in the last
`hygiene-editing-rounds` (4) rounds: the file is being worked on. A read of
it folds once the edits move on, as before. fs-edit answers with the lines it
changed (ADR 43, amended the same day), so what the stale read no longer
shows the edit's answer does.

TestHygieneKeepsTheReadsOfAFileBeingEdited reads big.py and bigger.py,
edits big.py twice: big.py's read stays, bigger.py's folds. On the unchanged
code big.py's read folds two rounds after the first edit.

## Amendment 2026-10-05: compaction that goes round in circles stops the turn

Session 20261004T182412Z-747045aa ran qwen-3.8-27b on Cerebras in a window
left at its default 61,440 tokens and compacted 15 times in 24 minutes.
Five compactions in seven minutes (18:38:51 to 18:45:21) made 125 requests
and 103 reads, 88 of them of files read before, and no edit: each handover
note asked the model to re-check its findings, which in 40K of room meant
reading the same files again. 42% of the session's output went into notes,
and it ended on a raw 402 with no report.

The second automatic compaction of a turn with no fs-write or fs-edit asked
for since the first now stops the turn instead (`compact-stop-unchanged`,
compact.scm): a note says why, the model is told to stop and say what it
found, what is done, what is left and what is in the way, and `turn-stopping`
(strategy.scm) makes `ask-model` offer no tools, so that answer ends the
turn; the finish checks (strategy.scm's evidence loop and this repository's
validate reminder) ask nothing more of it. A write between the two
compactions is progress, and the turn compacts again as before.
TestCompactionGoingRoundInCirclesStopsTheTurn covers both.

## Amendment 2026-10-05: a worktree session lands its work with git-land

The worktree section said the changes reach the repository only through the
branch, and to say they are on it. Asked to "commit and push to main", 34
times in the local sessions, models did it through the shell: quoting broke,
signing failed and was turned off, a rebase hung, a branch was pushed where
main was meant (ADR 34, amended the same day). The section now tells the
model to run validate and then call git-land when the request asks for the
work to land, never to commit, rebase or push through the shell, and never
to turn commit signing off.

## Amendment 2026-10-05: long thinking without an edit is told to write it down

In session 20261005T151325Z-d9519026, 14 calls in a row averaged about
19,000 reasoning tokens each, each emitting one read and no text, the
thinking working out the same fix again. The reply's thinking now goes back
to a Claude model (ADR 9, amended the same day), but not to every model, and
a model can think in circles anyway. Three rounds in a row whose reply
thought for 10,000 reasoning tokens or more and whose calls wrote nothing
now get a line asking the model to write the design it reached into a plan
step's note or a plan, then make the edit (`stall-check` in strategy.scm,
`stall-rounds`, `stall-reasoning-tokens`); `model-turn`'s reply carries
`reasoning-tokens` for it. TestLongThinkingWithoutAnEditIsToldToWriteItDown
covers the count.

## Amendment 2026-10-05: calls that do not depend on each other go in one reply

Most models asked for one call a reply, and every reply sends the whole
conversation again: in the 75 local session logs of 2026-09-30 to 10-05,
muse-spark made 1.41 calls a reply, glm 1.18, qwen 1.37, deepseek 1.27,
claude-sonnet 1.64, gpt-6-luna 1.99 and gpt-6.1-sol 2.88. On the same task
(model providers and efforts in the console), gpt-6.1-sol finished in 97
requests, muse-spark in 261 with a narrower result, and qwen3.8-max had 9 of
15 steps after 164 (20261005T022031Z-06eaad19, 20261005T003810Z-cebad9c2,
20261004T185312Z-c3eeb9d4). No working rule said calls could go together.
One now does: the reads and searches the model already knows it needs go in
the same reply, as does a read beside the test it waits for, since they run
in order and come back together. Not measured on the benchmark yet.

## Amendment 2026-10-05: the unchanged code is a copy of the last commit, never a stash

The working rule for comparing a failure with the unchanged code gave
`git stash; <command>; git stash pop` as its example, and a worktree session
was then told, in its own section, never to use git stash (ADR 34). Outside
a worktree a stash is no safer: a stash not popped, or popped onto other
changes, loses work, and this repository's own instructions forbid it. In
the local session logs of 2026-10-02 and 03 models stashed around tests that
had passed and once to rebase, in the person's own checkout
(20261003T002506Z-f564a28c seq 3111, 4515, 5803). The rule now compares on a
copy of the last commit (`git archive HEAD` into `$TMPDIR/base`) in every
session, and says never to use git stash for it. ADR 34's "outside a
worktree the habit is unchanged" no longer holds.

## Amendment 2026-10-05: the explorer and the planner read the agent's last answer

The plan gate's explorer and planner, each in a context of its own, read a
chat's request after the person's two messages before it (amended above),
but not what the agent had last said, which a reply answers, nor the files
attached. In session 20261005T060224Z-2f9eb922 the explorer, shown neither,
said the request "does not identify what 'there' refers to", when a
screenshot and the agent's own question did; in 20261005T151325Z-d9519026
the explorer and planner of "fix all of them" could not see the findings
the agent's last answer listed. They now read the request after the
agent's last answer (cut at `plan-earlier-cap`) and with the names of the
files attached, and are told to say what they cannot tell from a file they
cannot see rather than guess (`plan-model-request`, `(request-context)`,
`plan-request-after-reply`, `plan-request-attachments`). The judge reads
what it read: at `jev.content = "plan"` it is sent the person's asks and the
plan, and the agent's replies are not sent (ADR 19); nor is an attached
file's name. An image itself still reaches no narrowed context (ADR 22):
the planner reads its name only. TestThePlannerReadsTheLastAnswerAndTheAttachments,
TestTheJudgeReadsAChatsEarlierMessages and
TestImagesStayOutOfNarrowedContextsAndTheJudge hold this.

## Amendment 2026-10-05: a plan written again keeps its marks

`plan-write` revised a plan and started its marks over. In
20261003T143916Z-48f31857 a rewrite that changed only step 6 (seq 5508)
dropped the marks of steps 1 to 5, done over the hour before, and a later
one word for word (seq 6528) was followed by six marks in 30 seconds. Each
mark is a reply that sends the whole conversation, and a lost mark shows
done work as undone, to the model and in the console's plan panel. A plan
recorded without marks in a turn that has marked steps now keeps the mark
and note of every step it keeps word for word, at its new number; a new or
reworded step starts unmarked (`carriedMarks` in internal/loop/prims.go). An explicit `marks`
(a carried run plan) is taken as given. The window shows kept marks under
the plan as "Marked before the plan was written again:", and a carried
plan's as before; only a carried plan had marks until now, so older logs
build the same windows. The reading reminder no longer says that writing
the plan again starts its marks over. TestAPlanWrittenAgainKeepsItsMarks
covers the marks and what the model reads.

## Amendment 2026-10-05: the model keeps the project notes

The system prompt shows a repository's notes as the agent's own, but only a
person could write them, from the console, and none did: the 75 local
session logs of 2026-09-30 to 10-05 hold no save, and no repository had
notes (asked about it in 20261005T170658Z-fe9ea317, the person had never
seen a model use them). `project-notes-save` is now a tool the
model is offered, and a working rule asks it to keep there what a later
session would need and could not quickly find, with the notes it was shown
that are still true. Who writes the notes and why that is safe: they live
in nodwyn's data directory, never in the repository, so a clone cannot
write them; the model can, and a model misled by what it read could leave a
misleading note, which the next session in the same repository, reading the
same files, would meet anyway. The prompt calls the notes leads to check,
not instructions; a save is redacted, capped at 8 KiB and audited, content
that looks like a credential needs a person's approval, and the console
shows the notes and lets a person edit them. TestTheModelKeepsProjectNotes
covers the tool, the rule, a save carrying a token left to a person, and
the next session reading the notes.

## Amendment 2026-10-06: optimiser edits wait for project trust

The optimiser previously treated every reload refusal as a failed edit and
restored the old file. With ADR 45, that would discard an approved proposal
because its new revision has not yet been trusted. It now keeps that edit and
reports that it has not run, while ordinary reload failures still roll back.
This is a host-authority correction, not a new model working rule; the regression
is `TestOptimizeKeepsAnEditPendingTrustReview`, using a scripted model.

## Amendment 2026-10-06: a failing test run is told, and an empty reply is asked again

Two checks in the default loop, built after a review of its finish gate. The
counts below are from the 161 local session logs of 2026-09-21 to 10-05 (185
turns, 59 of which changed a repository file), read from `llm.resp`,
`tool.call` and `tool.result`.

- **A test run that exited non-zero is told once.** The evidence loop set
  `tested` for any test or check run however it ended, and `shell.scm` counts a
  run with failures as a run so the "cannot run the tests" way out stays for
  commands that never ran. A turn could therefore end with its last test run
  red and the evidence satisfied. The loop now tracks whether the latest test
  or check run since the last edit exited non-zero (`run-failed?`, `red` in
  `evidence-loop`), and a turn that ends on one is told
  `finish-failing-message` once: fix it, or compare on the unchanged code as
  the working rules ask, say the failure is not the change's, and finish. A
  run the model was told of is not told again unless it runs and fails again,
  and `finish-reminders` still caps the turn. A check script's failing run
  counts, so `TestCheckScriptIsATestNotAChange` and
  `TestFailingCheckRunsAreNotUnrunnable` now expect the reminder before the
  turn ends. **No logged turn ended this way**: of the 59, 25 ended after a
  green last run, 33 had no `shell-exec` test command after the last edit
  (this loop does not run in a chat, and a run through the `validate` tool is
  not such a command), and the one my count marked red had run `validate`
  green after it. The gate comes
  from reading the code, not from a logged failure.
  `TestAFailingRunBeforeTheLastEditIsNotTold` passes without the change; it
  is there so the reminder never counts a run before the last edit.
- **An empty reply is asked again once.** A plain `(ask-model)`, the one that
  offers every tool, whose reply has no words and no tool call, is asked
  again with `empty-reply-message`, once (`empty-reply-retries`). The planner
  has its own retry (`plan-empty-retries`) and asks that offer no tools are
  left as they were. **No logged reply was empty**: 0 of 205 replies without
  a tool call, in the main context, across the local logs.
- **Not built.** A re-ask for a reply that announces an action and calls
  nothing ("Let me…"): one of the 205, glm ending on "Committing now:"
  (20261004T043621Z-4f1e4886 seq 419), is too few for a rule that reads the
  reply's words. A planner rule to write steps as behaviour and not as code:
  none of the 89 logged plans quotes code. Keeping the last passing patch when
  a turn hits its limit: the repository already holds every edit, and
  restoring an earlier state would delete work the person can see.

## Amendment 2026-10-06: plans say what the code should do, not code

`plan-shape`, the shape the planner and the revision prompt ask for, now adds:
say what the code should do as behaviour, in which cases and with what result,
not as code to type, since whoever carries the plan out reads the code and
writes it, and a function name or signature quoted from memory that the code
does not have misleads them. It answers the failure in this ADR's context, a
plan written without running anything overriding what the model's own checks
showed, from the plan's side: the planner reads the code but cannot run it.
**No logged plan has the fault**: none of the 89 in the local session logs of
2026-09-21 to 10-05 quotes code, so this is a guard that costs a sentence a
plan, not a repair. It was added at the person's request, and is worth
removing if a plan's quality, as the judge scores it, falls. The companion
rule, that a model checks a helper's definition before an edit that calls it,
is not added: it repeats "Read before you write" and cannot be checked.
`TestThePlannerIsAskedForBehaviourNotCode` covers the request.

## Amendment 2026-10-10: the plan says where, the run decides how

The long-context SWE-bench cohort on workshop ran pi, OpenCode and nodwyn
(`d64209d`) on the same 40 tasks, three times each, with the same model
(qwen3.8-27b at 160k context) and the same task text, 2026-10-07 to 10.
Every attempt was read by hand; the per-task notes are kept with the
benchmark runs, not in this repository. nodwyn resolved the most (73 of 107
evaluated attempts, against 73 of 120 for pi and 66 of 120 for OpenCode), and
lost on two counts. On process: a median attempt took 11.6 minutes and 78
calls, against 5.1 and 44 for pi, half of it before the plan was recorded. On
outcome, on one kind of task: where a single detail decides (which helper,
which literal, which of two fixes), the harnesses without a plan chose it at
the edit, with the definition on screen, and nodwyn chose it in the plan,
from reading alone, and kept it.

- **The plan's choices are guesses; the run decides.** The working rule
  called the plan's author "someone who read the code", and a model quoted
  that back to keep the plan's wrong `offset=None` against its own round
  trip (xarray-6599, 0 of 3, pi 3 of 3). The rule, `plan-header` and
  `plan-findings-message` now say the plan was written before anything ran,
  that a run which contradicts it wins, and that a plan is rewritten, never a
  test or a reproduction made to agree with it (django-15916 edited its
  repro; scikit-learn-13124 and matplotlib-25960 rewrote tests).
  `TestThePromptsLeaveThePlansChoicesToTheRuns`.
- **A detail is settled at the edit, from what decides it.** A new working
  rule asks for the definition called or imitated, the code nearby that does
  the same, and the request's own example in view before an edit that
  settles an open detail, and not memory of an upstream fix. The 2026-10-06
  amendment left this rule out as unmeasured; these runs measure it:
  django-16877 (`conditional_escape`, nodwyn 0 of 3, OpenCode 3 of 3 with
  `escape_filter` read just before the edit), django-16938, django-16899,
  django-11815; and recall lost django-11848 and scikit-learn-12973.
- **A test tells the fix from its nearest wrong version.** Every losing run,
  in all three harnesses, checked its change with the case it was designed
  for (timedelta data starting at 0, plain strings, `'255'` for 255). A
  working rule and the `Test:` kind now ask for an input that tells them
  apart.
- **The explorer lists; it does not choose.** Its system prompt and task
  put the code at the failure site first, ask for every place the change
  must reach, the request's literals copied exactly and the options found
  without a choice. What it listed won scikit-learn-12973 (nodwyn 2 of 3,
  the others 0 of 6), django-15161 and matplotlib-24637; what it decided
  ("The fix is a one-line change … `offset=None`", "`escape` is already
  imported") lost xarray-6599 and django-16877, and rounds spent on release
  notes ran out before the definition that settled django-16938.
  `plan-shape` asks the planner to name an open choice and what decides it
  rather than make it, and to copy the request's literals; the
  behaviour-only rule alone had dropped a detail exploration found
  (`utcnow`, django-11848). A `Change:` reaches every place the request
  covers, and a `Docs:` step names a document that describes the changed
  behaviour, never a ticket number nobody gave: 45 of 107 nodwyn patches
  edited docs (pi 23 of 120, OpenCode 13 of 120), two citing invented
  tickets. `TestTheExplorerAndThePlannerLeaveChoicesOpen`.
- **Only the tests the request changes are updated.** "Update the existing
  tests that encode the old behaviour" was quoted to leave eight tests red
  (django-16938) and read as licence to rewrite pinned tests the request
  never named. It now says which tests, that another test the change breaks
  means the change is wrong, and asks the report to name each existing test
  whose expectations changed. `finish-failing-message` says to fix the
  change, not an existing test's expectations.
- **The unchanged code is the copy's code.** In an editable install the copy
  of the last commit still imported the repository's patched code, and
  unpicking that cost about 25 calls on django-10914 and 50 on django-16938.
  The comparison rule puts the copy first on the import path and prints
  where the module came from. The stash stays forbidden (amended 2026-10-05).
- **Finding the environment names no folder outside the repository.** The
  rule's example listed `/opt/*/envs`, which the guard asks a person about:
  55 of the 223 shell commands refused in nodwyn's attempts did that. It now
  uses `conda env list` and runs the environment's interpreter by its path,
  which the guard lets through (ADR 17, amended 2026-10-10).
- **The evidence loop reads state, not events.**
  - An exit that says no test ran is no outcome: 126 and 127, and pytest's 4
    and 5 (`not-run-statuses`, `pytest-not-run-statuses`). A missing
    `flake8` after passing tests (django-15161) and pytest on a missing path
    after the work was reported (scikit-learn-13124, 35 calls) were told as
    failing runs. `TestAnExitThatRanNoTestsIsNoFailure`.
  - A run on `$TMPDIR/base` that fails after the change's run failed is the
    comparison done (`base-run?`, outcome `compared`), and the failing-run
    reminder is not said (django-13158, about 12 calls).
    `TestAFailureOnTheUnchangedCodeTooIsNotTold`.
  - The failing-run reminder is said once per edit, not again when the same
    tests are run again. `TestAFailingRunIsToldOncePerEdit`.
  - `finish-no-change-message` offers a way out with evidence: a request the
    code already answers ends unchanged once a check has run. All three
    nodwyn runs of django-13513 found the fix in place and were sent back to
    change something, one breaking five tests for a while; pi and OpenCode
    stopped in 2 to 4 minutes. A stop with no check run is reminded as
    before. `TestAnUnattendedTurnMayEndUnchangedAfterACheck`.
  - New files the plan names are not called leftovers: the reminder named
    the test and release-note files the plan asked for in 13 attempts.
    `TestNewFilesThePlanNamesAreNotNamed`.
  - A reply cut off while it weighed options is told to settle them with a
    run, not by which is shorter or which the plan named: told only to think
    less, xarray-6599 r1 wrote "Let me be decisive: … `offset=None` (the
    plan)". `TestACutReplyIsToldToSettleOptionsWithARun`.
- **The planner is asked again for a reply that is not a plan.** Four of
  107 recorded plans were none (a refused call, or command transcripts of
  20,660 and 45,577 characters in fenced blocks), and two more quoted code in
  fenced blocks. A reply with a call, a line opening a fenced block, or more
  than `plan-max-chars` (8000) characters is asked again within
  `plan-empty-retries`. `TestPlanGateAsksAgainForANonPlan`.
- **Handover notes are short and act first.** Two notes on django-15280 were
  cut off at the output limit after about 59,000 characters of thinking and
  kept one sentence, and a note listed the right fix as "ruled out" because
  it seemed unlikely to match the maintainers'. The note request asks for at
  most 600 words, the decided but unmade edit first, and "ruled out" only
  for what a run ruled out; a note cut off is asked for again, shorter
  (`compact-note-cut-message`), and the handover makes the decided edit
  before re-running the reproduction, which a fix found before the clear on
  django-14351 waited 11 to 13 minutes for.
  `TestACutHandoverNoteIsAskedForAgain`.
- **Not built.** Letting the explorer run code, or planning after the first
  reproduction: the core allows nothing irreversible before a plan is
  recorded in a turn that must plan, and the shell is irreversible, so it
  would change what the gate guarantees (ADR 19). A fast path that skips
  exploration and the plan for an ask that points at one function: the 40
  tasks show no sign in the request that separates those where the
  explorer's listing won (scikit-learn-12973, django-15161, matplotlib-24637)
  from those where it was overhead (django-13315, sympy-15809,
  django-16116); the narrower explorer task above is what the data supports.

Each test above fails on `d208b2c` and passes with the change.
`TestPlannerExploresFirst`, `TestUnreviewedTurnIsNotOfferedAWayOut` and
`TestModelWritesItsOwnPlan` now expect the new texts.

## Amendment 2026-10-11: a check must be able to fail

Two runs of `9c44245` on the long-context cohort, read by hand against the
d64209d runs, showed one failure behind every task that flipped: no run fed the
candidate fixes the input that tells them apart. django-16877 r2 asserted its
recalled output; xarray-6599 never ran timedelta data that does not start at
0; django-14787 r2 read `bound_method called with…` as success; pytest-7205
r2's reproduction used a flag that does not reproduce. The person's own
sessions show the same in work that is no bug fix: Go tests and `node --check`
green while the page went blank on a click (the jev-key console change), and
"done" twice on broken UI with Go tests green (muse-spark). A rule to test the
nearest wrong version (2026-10-10) shows up in plans and rarely in the tests
that run, so the loop now checks what a rule cannot:

- **Each part of the change against the check.** Before an unattended turn
  with a change ends on a check that passed since the last edit, the host runs
  that check again with each part of the change taken out in turn and put back
  (`check-without`, `internal/prim/without.go`): a hunk of a changed code file,
  or a whole new or deleted file, at most 8 and within 5 minutes in all,
  documents, tests and comment-only hunks left out. The parts it still passes without are named, once a turn
  (`finish-untested-parts-message`): either a part is not needed, or the check
  cannot tell whether it works. It needs no notion of the task, only the diff
  and the model's own check, so it applies to a feature, a refactor or a fix
  alike. New files the untracked-files reminder already named are left out of
  the answer. `TestCheckWithoutTakesOutEachPart`,
  `TestCheckWithoutStopsAtItsBudget`, `TestTheChecksUntestedPartsAreNamed`.
- **A cited command must have run since the last edit.** A command the final
  report cites in backquotes, starting with a program the turn ran, that no
  command line run since the last edit matches is named, once
  (`stale-citations`, `finish-stale-citations-message`). A plain "done" cites
  nothing and is not asked. `TestAReportCitingAnOldRunIsAsked`.
- **The rejected option, too.** A working rule asks to run the check once
  against the option a choice rejected: a check that passes there as well does
  not decide it. `TestThePromptsLeaveThePlansChoicesToTheRuns` covers its text.
- **Not built.** Asking every claim in a report for its run: most reports end
  on a plain "done" with the evidence above it, and a reminder on each would
  nag. A check that the rejected option really fails: that needs the option
  written out as code, which the plan only names.

They are to be measured on the commit-replay set kept with the benchmark runs
(features, refactors and fixes from this repository's history, graded by each
commit's own tests) as well as on SWE-bench, since SWE-bench alone covers bug
fixes. Each test above fails on `9c44245` and passes with the change.

## Amendment 2026-10-14: the repository's own tools are runs the harness sees

A trusted repository runs its checks through tools its `.nodwyn/init.scm`
registers: nodwyn's `validate` (make validate, in the background),
`go-test {pkg}` (go test -race) and `run-make {target}`. The evidence loop,
check-without and the plan's marks counted `shell-exec` runs only, so on
commit-bench (dsl18m, `1e595e5`, trusted, 2026-10-14) they did not see those
runs:

- the end-of-turn evidence message ("has not passed since your last edit")
  fired in every attempt, mostly after a validate or go-test that had passed
  since the last edit: on 3422faf1 steps 3, 4, 6 and 8 after validate passed
  at call 111. About 73 calls, 0.6M tokens and 33 minutes over 10 attempts
  followed it, an upper bound since some of it was real work;
- hygiene's first rule folded validate's passing result as "this test command
  did not run any test ()" in 7 of 10 attempts, since no word of
  `hygiene-ran-words` is in its summary;
- a run whose exit status was another command's marked plan steps passed: on
  3422faf1 steps 3-6 from a run on the copy of the last commit whose tests had
  failed, each ending in `| tail -n 8 || true`, and on c80b38ac a step from
  `| tail; echo exit=$?`.

So:

- **A tool is the command it runs.** `tool-commands` (strategy.scm) maps
  `validate` to make validate, `go-test {pkg}` to go test <pkg> and
  `run-make {target}` to make <target>; a project may add rows. `tool-call`
  makes the shell-exec call that runs that command from the tool call's own
  arguments, never from its output, and `tool-result` reads the status the
  tool's text reports ("exit N" first, or a background run's "pN: exited N").
  `command-line` and `test-call?` see the command, and the evidence loop
  counts such a run toward the turn's test runs, the commands that passed
  (`passed`), the passing check check-without takes apart (`passing`), and
  the commands a report may cite. The plan's marks (`plan-language-round`)
  see them too. A tool's whole check (validate) is not check-without's check:
  it runs for minutes against check-without's five for all its runs, as a
  shell `make validate && make accept; echo exit=$?` did on 3422faf1, naming
  nothing; the check before it stays. Its pass counts as the whole check's
  for the plan's evidence and for the report's citations
  (`whole-check-passed?`), and the repository's own gate test
  (`TestValidateCountsAsARunsTestRun`) still sees a run end after validate.
- **validate's summary says it ran.** `hygiene-ran-words` gains "validate:
  pass", "pass  gofmt" and "tests vs unchanged"; a failing validate already
  said "failed". The fold of a tool's run that ran nothing gives the end of
  the tool's text as its reason, not "()".
- **A masked status is no evidence without output.** A run passed, for the
  plan's marks and its end-of-turn evidence, when it exited 0, was not on the
  copy of the last commit (`base-run?`), and, when its status may be another
  command's (`masked-run?`: an unquoted pipe without pipefail, an `||`, or a
  `$?` in the line), when its output shows tests passing (`--- PASS:`, go
  test's `ok`, `validate: PASS`) and none failing (`run-entry`). A Go test
  step (a `_test.go` file) takes a run only when its output shows the test
  (`--- PASS: <name>`) or its package (`ok ... /<dir>`, not "[no tests to
  run]") passing; a go test of the package with no `-run` counts for every
  test step in it, which is how the go-test tool runs.
  `TestTheRepositorysToolsAreTheRunsTheyMake`,
  `TestARunWhoseStatusHidesItsTestsIsNoEvidence`,
  `TestARepositorysTestToolsAreFoldedByWhatTheyRan`.
- **Not built.** Masking for the failing-run reminder and for check-without:
  check-without already tells a part apart by output as well as status. Test
  steps whose test is not in the file the step names (an exfil scenario run
  by `TestScenarios`). Reading a browser check's own summary (`== credentials:
  ok in 6.1s`): it is this repository's runner, not a test runner's, so a
  browser check piped into tail (on c80b38ac's runs, `run.sh credentials 2>&1
  | tail -n 3`) is no longer a step's evidence, and validate, which runs the
  browser checks for a console change, is. Counting `make accept` as a whole
  check.

Replayed through `run-entry`, the logged calls of 3422faf1 give: the
base-copy run (call 78) no evidence; `generate.py --check; echo exit=$?`
(call 108, which printed `exit=1`) none either; `make validate && make
accept; echo exit=$?` (call 119), whose output shows its passes, evidence; and
each go-test of a planned test's package, evidence for that test.

A go-test run is now check-without's check when it is the latest that passed,
which no trusted run's was before: one package's tests may still pass with a
part in another package taken out, so the untested-parts message may name
parts other runs test, as the dsl18m review found for shell runs. Each test
above fails on `1e595e5` and passes with the change.

## Amendment 2026-10-14: a sub-agent wraps up with the tools it had

A sub-agent out of rounds, or stopped for repeating a call, was asked for its
answer with no tools. A local server lists the tools near the start of the
prompt, so that request shared no prefix with the ones before it and the
whole context was read again. On commit-bench (dsl18m, 2026-10-14) the
explorer's last request re-read 25,000 to 48,000 tokens so in 16 of 18
attempts, about 0.03-0.05M cost-eq (4-7% of a task) and 13-27 s each, and
dsl16 and A did the same.

So `sub-agent-wrap-up` asks as the sub-agent did, offering its tools, after
`sub-agent-wrap-up-message`; a call the model makes anyway is skipped with
`sub-agent-text-only-reason`, and the model is asked once more with no tools,
as before. At most two asks, as before. `TestSubAgentOutOfRoundsStillAnswers`,
`TestSubAgentRepeatingItselfEndsOnlyItself` and
`TestSubAgentWrapUpSurvivesAToolCall` now expect the tools on the wrap-up
request, and fail on `1e595e5`.
