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

## Amendment 2026-10-16: a reload runs init.scm over what it first found

`(reload)` evaluated `init.scm` again over the bindings its first run left.
The wrap-and-extend pattern docs/scheme.md teaches, `(define base-system-prompt
system-prompt)` before redefining `system-prompt`, then captured its own
wrapper, which called itself without end, and before ADR 7's 2026-10-16
amendment that ended the daemon. The session now records, when `init.scm`
first loads, each global it changed and how that global was bound before; a
reload puts those back, runs the file, and records what the new revision
changes. Globals a turn sets are left alone, and names `register-tool` binds
stay with the tool table. A reload that fails, or whose prompt check fails,
puts every global back as it was, so the failure the optimiser reports leaves
the running strategy unchanged as well as the file. This is a host
correction, not a working rule; the regression is
`TestReloadRunsAWrapperOverWhatItFoundFirst`.

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

## Amendment 2026-10-15: older results can be recalled

Both system prompts said older tool results may appear as `[elided seq=N]`
or cut, and to "re-read a file if you need it again". Nothing brought back
anything else: in the 78 local logs of 2026-09-30 to 10-09 the window
elided 691 results in 8 sessions, compactions cleared three sessions'
earlier context, and 51 shell results lost their middle to the plain-text
rendering (`shell.scm` keeps the first 1,500 and last 6,000 characters).
The line now names `recall`, which brings an event back by its seq as it
was, and `index-search` with scope `"conversation"`, which finds what was
said, run or shown earlier, and keeps `fs-read` for a file as it is now
(ADR 50). The line says what the tools are for; whether models use them is
measured as ADR 50 §13 says, and the line goes with them if they do not.

`look-tools` gains `index-search` and `recall`, which only look, so a run of
rounds spent searching the conversation counts towards the reading reminder
as rounds of reading do.

The same day, with the repository's index (ADR 50, R1), the rule on
reading code names `index-search` with scope `"repo"` for code found by
what it does or under names that would have to be guessed, keeping
`index-grep` for an exact string or pattern: 917 of the 1,614
regular-expression searches in those logs (57%) were alternations of three
or more guessed names, such as `modelForm|credential|host|bind|freeClass|api_key`.
`index-search` also joins the tools of the sub-agents that read the
repository: the plan's explorer, `second-look-tools` and the optimizer's.
Whether models use it is measured as ADR 50 §13 says; the rule and the
tool go if they do not.

## Amendment 2026-10-16: benchmark habits fire only where they fit

Three habits and hints written for the benchmark's runs reached every chat.
The history hint (`checkloop.scm`: a `git log` or `git blame` before the
first code edit gets "call second-look with your check script") answered a
question such as "what changed in the last five commits?" with advice about a
check script, up to twice a session, and the edit flag it depended on was
never reset, so it stayed off for the rest of a session after one edit. The
hint is now given only in a turn that works toward evidence, as the finish
checks are (`(or (unattended?) (plan-required?) (plan-recorded?))`), and the
flag is reset at the start of every turn. The check-loop rule in both system
prompts now says it is for a request that asks for code to change and that a
question needs no script. The worktree section's "run validate first" named a
tool only nodwyn's own `.nodwyn/init.scm` defines: in another project's
worktree the model asked to land its work called it and was told it was
unknown, or skipped the step. The sentence is added only where `(primitive-info "validate")`
succeeds, as `git-rule` does for `git-diff`.

The per-turn resets (`turn-stopping`, `stalled`, `compact-writes-at`, the
check loop's request and edit flag, the sub-agent depth and label) moved from
wrappers of `nodwyn-turn` to wrappers of a new `turn-start`, which the host
applies to the request just before `nodwyn-turn`. A project that redefined
`nodwyn-turn`, as docs/scheme.md tells it to plan its own way, lost them, and
one turn that filled the context twice with nothing written left every later
turn of the session asking the model with no tools. `ran-ok?` is defined once,
in `shell.scm`, which owns it.

Calls the shell habit refuses (a command line sent through `cmd` and `args`)
reach the progress guard and the tool-call budget, through a third argument of
`skip-tools`: twelve identical `ls -la | head` calls with a tool-call limit of
5 ended with no stop after 13 model requests, where the same twelve real calls
stop at the fourth. The strategy's own skips (a sub-agent's repeats, tools off
for a handover note) stay out, since the sub-agent ends itself before the guard.

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

## Amendment 2026-10-15: the whole check stands only for the plan's check steps it runs

Before an unattended turn ends, the plan's steps are set against what
happened (`plan-language-check`, planspec.scm), and the repository's whole
check passing since the last edit (`whole-check-passed?`: make validate,
make check, make test, ...) met every Check step, whatever its command. A
whole check runs the repository's tests, builds and linters, not every
command a plan names: on commit-bench (dsl16, `550e3b9`, c97f9a41, session
20261013T224535Z-ac7eaa5c) the planner's `python3
docs/book/source/generate.py && python3 docs/book/source/generate.py
--check` step was met by a run of make validate (call 3217) that ran no
generator, after two test edits (3187, 3211) had changed the line counts the
book's inventory records. The evidence message (3248) named two test steps
and not that one, and the change ended with the inventory stale, which the
parent's validate did not check.

So the whole check stands for a Check step only when the step's command is
one that checks the code (`check-command?`: a test run, a build, go vet, a
linter, a make target), as the whole check does. Any other Check step, a
generator's `--check`, a browser check or a `go generate`, is met only by a
passing run of its own command since the last edit; until then the evidence
message names it ("step N (Check) <command>: it has not passed since your
last edit"), which costs a round when the model did not run it again, and
the harness no longer marks it done. Test steps are unchanged: the whole
check runs the tests. `TestAWholeCheckStandsOnlyForTheChecksItRuns` fails
on `defffcd` and passes with the change.

Of the 107 Check forms in the planners' final plans across the recorded
commit-bench sessions (257), 101 are commands that check the code, which
this leaves as they were. Of the other six, two are this generator (dsl15
and dsl16, c97f9a41), two `go generate ./internal/frontend/rpc`, one the
bare tool name `validate`, which the validate tool's own run (make validate)
meets, and one `go run ./scripts/validate` (dsl16 on another model,
8e172440), which is what make validate runs: that step would now be said
after a pass of the validate tool alone, a round spent on a step already
met. In that session the model ran it itself (call 1085), which meets it.

- **Not built.** The evidence message is said once a turn, so a Check step
  met when it fires stays met through later edits. The deep study's example
  of this rule, 107251d0 under dsl19, is that case and not this one: its
  plan's check was the planner's `go build ./... && ... && make validate`,
  a check command, and the message had fired (call 3921) before the test
  file that made the inventory stale (3935). For nodwyn's own tasks
  validate now runs the inventory check (ADR 37, amended 2026-10-15); this
  amendment covers repositories, and older snapshots of this one, whose
  whole check does not.

## Amendment 2026-10-15: a compaction gives the plan back

A compaction narrows the context, and the window keeps what follows the
narrowing: the handover note. The turn's plan is a message where it was
recorded, before the narrowing, so it went with the cleared conversation. In
the trusted commit-bench runs of A, dsl16, dsl18m and dsl19 (2026-10-14 and
15), none of the 37 compactions left a plan in the window, though every one
of those turns had recorded one. On 6e18b7ba (dsl16) the model had worked out
all six edits by ten minutes and wrote none before the context filled; its
note said "plan.md is on disk at repo root with all 19 steps", but plan.md is
the repository's own roadmap and the plan was the harness's. After the clear
it concluded the plan "isn't actually in the repository's plan.md file, so I
need to rebuild it", read the code again and spent 13,200 tokens working out
the design a second time.

So `compact-context` adds the turn's plan after the note (`compact-plan`,
compact.scm): the plan as recorded, under `plan-header`, with the latest
mark of each step and its note under "Marked before the conversation was
cleared:", as the window lists a carried plan's marks, and a line saying the
harness keeps it and no file holds it (`compact-plan-message`, prompts.scm).
It is read from the log with `unfinished-plan`, the plan this turn would
leave to the next, and only when the turn has recorded one (`plan-steps`):
a `/compact` between turns adds none, since the next turn carries an
unfinished plan itself. It is a harness message, so the window and its
replay are unchanged; it stays in the conversation after the turn, as a
recorded plan does. The note request now says the plan comes back after the
note, so the note leaves it out and does not say a file holds it, and that a
file holds something only when the work wrote it there or read it there.

A compaction now carries about the plan's size more (a few hundred to 2,000
tokens). `TestCompactionCarriesThePlanWithItsMarks` fails on `d880bb3`: the
compacted request is the system prompt and the note alone, and the note
request says nothing of where the plan is.

## Amendment 2026-10-15: a turn that fills twice gets a write round before its report

The stop of 2026-10-05 is right that a third compaction would repeat the
second, and wrong to write nothing when the model knows the edits. On
6e18b7ba (dsl16) the context filled the second time at 36 of the run's 60
minutes with nothing written; asked without tools for a report, the model
answered that all six edit sites were confirmed, listed every code change as
left, and the run ended with an empty patch. The other runs of that task
(A, dsl18m, Pi) had written their code before their compactions and found
it on disk after them.

So the stop first gives a write round (`compact-stop`, compact.scm): the
model is told "Your note and plan name the edits: make the code changes now;
tests and docs come after", and to report what is done and what is left
after them, and is offered `compact-write-tools` (`fs-edit`, `fs-write`,
`read-definition`, and a repository's `go-build`: those of them the session
has) for at most `compact-write-rounds` (6) requests. Then
`turn-stop-report` asks for the report without tools, and that answer, or
one the model gives sooner, ends the turn as before; the finish checks still
ask nothing more. While the turn stops, `ask-model` skips a call to a tool it
did not offer (as `compact-note` does), and a reply whose every call it
skipped spends its round and is asked again; before, such a call with the
report would have run. A turn that is stopping is not compacted again, so
the writes of the write round do not lead to a third compaction. With
`compact-write-rounds` at 0, or neither edit tool in the session, the turn
stops with the report alone, as before.

Not built: the reading reminder at 4 rounds rather than 12 while nothing has
been written since a compaction. The risk is a hasty edit in a run that
would otherwise have ended with none.
`TestCompactionGoingRoundInCirclesGetsAWriteRound` and
`TestAWriteRoundEndsWithAReportWithoutTools` fail on the commit before this
one; `TestCompactionGoingRoundInCirclesStopsTheTurn` now covers the report
alone, with `compact-write-rounds` at 0.

## Amendment 2026-10-15: a shorter explorer for qwen, and folds at three quarters

Two knobs set by the earlier amendments were read against commit-bench's
trusted runs of A, dsl16, dsl18m and dsl19 (qwen3.8-27b, high water 102,400,
2026-10-14 and 15).

**The explorer.** qwen's row of `plan-explore-rounds` was 16, set on 2026-10-05
with no qwen explorer run to read. The explorer used all 16 rounds in 39 of
48 runs. It read 571 files, 296 of them first after its eighth round, and
the model doing the work read 313 of the 571 again itself, 154 of those 296
among them. The deep study put the explorer at 0.13-0.15M cost-eq a task. The
row is now 8. Expected: about half the explorer's requests and their prefill
go, an estimated 0.05M a task. Risk: the planner sees fewer facts; the 142
files the explorer read after its eighth round and the work never read are
the facts it may miss, so plans and their reviews on the nine are what to
read next.

**Hygiene's pressure.** 94 of the 108 hygiene folds in those runs were
pressure folds ("the context is filling the window", past half the high
water), 63 to 79 of them with the context under three quarters of it. Each
fold makes a caching server read the rest of the prompt again, and the
model read files whose reads had been folded again 57 to 156 times an arm
(some of them to edit them, which a fold does not cause). `hygiene-pressure`
is now 0.75. Expected: most of those folds wait until three quarters, or do
not happen before the turn ends or compacts, and below three quarters a
batch still folds when it pays for its re-read or the cache is not serving.
Risk: requests carry more context, which a working prefix cache serves
cheaply, and a long turn may reach the compaction threshold sooner.

It meets `hygiene-keep-pressure` (0.75, 2026-10-14), which holds the reads of
files the plan will change until the turn edits them. The two are equal: below
three quarters a held read stays and the others fold only when it pays; past
it every due read, held ones too, folds in the same sweep, one re-read of the
prompt where the old values made two (the others at half, the held ones at
three quarters). The held reads fold at the share of the high water they
did, though a context folded less may reach it sooner. Were
`hygiene-pressure` set above `hygiene-keep-pressure`, a held read would lose its
hold before the others fold whatever the cache; a project that raises one
raises both.

`TestTunedValuesFollowTheModelsFamily` and `TestExplorerRoundsFollowTheModelsFamily`
(a qwen explorer is stopped after its eighth round) and
`TestHygieneWaitsForThreeQuartersOfTheHighWater` fail on the commit before
this one: at 0.62 of the high water a fold that does not pay waits, and a
held read stays while the other folds because it pays, where before both
folded because the context was past half. Its case past three quarters, the
held and the other read folding in one sweep, passes on both.

## Amendment 2026-10-15: the finish checks are said once, together

Before an unattended turn ended with a change, four checks could each send
the model back once a turn: the new files git does not track, the plan's
steps without their evidence, check-without's untested parts and the
report's stale citations. Each answer was an edit or a script, which re-armed
the reminders before them ("You changed files after your last test run"),
and a trusted repository's own reminder ("validate has not run") came on
top. In the deep study of commit-bench (2026-10-15, reports 12 and 13):

- on five tasks set against Pi, the rounds after the first finished report
  cost A 0.85M tokens and dsl18m 0.41M, in 21 segments 17 of which edited
  nothing in the repository; on the ten held-out tasks 8 of A's 10 runs were
  sent back after a final reply, adding 193 calls, check-without 1.80M of the
  2.16M;
- on c97f9a41 (A) six chained reminders cost 0.38M with no edit, among them
  "validate has not run" twice, right after `make validate` had run in the
  shell: `.nodwyn/init.scm` sets `validated-state` only when its validate
  tool runs;
- deletions made 7 of the 69 parts check-without named, and on 8c795994 one
  message named nine deleted empty logs "(deleted file, +0 -0 lines)", whose
  answer cost 19 calls and 200k tokens changing only a script in the scratch
  directory.

So:

- **One finish review, once a turn.** When a turn with a change first ends
  and no reminder of the turn's evidence is due (no change, no test run since
  the last edit, a failing run), `finish-review-points` (strategy.scm)
  gathers the new files, the plan's unmet evidence (the steps that have it
  are marked as before), check-without's untested parts and the stale
  citations, and `finish-review-message` (prompts.scm) says them in one
  message: one point as before, several numbered, closed by
  `finish-review-close`, which says it is asked once, asks for the check to
  run again after any change, and says the next answer ends the turn.
  `TestTheFinishReviewIsOneMessage`.
- **The turn is accepted after it.** An answer to the review ends the turn;
  the evidence reminders are not asked again. `TestAnAnswerAfterTheFinishReviewEndsTheTurn`.
- **check-without leaves deletions alone.** A deleted file and a hunk that
  only deletes lines (its added lines, if any, comments or blank) are no
  parts: a check that passes with the deletion put back cannot be extended to
  fail without code that is gone, and asked to, models wrote ablation
  scripts or kept dead code. Documents were never parts (`codeFile`).
  `TestCheckWithoutLeavesDeletionsAlone`.
- **A shell make validate is validate's run.** After a run of `make
  validate` that ran to an exit, `validated-run` sets `validated-state` to
  `(validation-state)`, as the validate tool does, in a repository whose
  `init.scm` defines both; elsewhere it does nothing. The benchmark runs the
  harness against old snapshots whose `init.scm` it cannot change, and every
  snapshot since validate came (`ea9921b`, 2026-10-03) defines both. `TestAShellMakeValidateCountsAsValidated`.

What is kept: every check still runs at the first finish, with the same
findings and wording; the evidence reminders still come first, and the
failing-run reminder still lets a turn end on failures the unchanged code
shares. What is given up: a second chance after the review, so an edit made
in answer to it and not tested again ends the turn untested (the review asks
for the run), and an untested deletion goes unnamed. The analysts estimate
0.1-0.2M tokens saved per task where the checks fire; the three segments of
the study in which a reminder led to a stronger test (A and dsl18m on
1d401bf3, dsl18m on 7513f7eb) would get their message once. Each test above
fails on `d880bb3` and passes with the change.

## Amendment 2026-10-15: the finish review names what the change removed

Three of the benchmark's losses came from what a change took away, each after
the model's own tests had passed (deep study of commit-bench, 2026-10-15,
reports 02, 03 and 04):

- on 67a1f950 dsl16 and Pi fixed the behaviour by rerouting `rootRefusal` and
  deleting `evalOr` as dead code; the hidden test calls `evalOr`, which the
  task never names, so neither patch built against it. check-without named
  the deletion and dsl16 kept it, "rather than reintroduce an unused helper";
- on 94f5cac4 Pi turned the visible case "count 0 is an error" into one for
  -1 before its first test run, and dsl16 followed its planner in the same;
- on ef04de9c Pi deleted the negative case its new JWT pattern broke, and on
  5040f9fe it rewrote the stream cases of the test that then failed as a
  pass-to-pass test.

The working rule that names each changed test and the request's words behind
it was in every one of these versions, and was not followed.

So `change-removals` (internal/prim/removed.go, hidden) reads the change
against HEAD and the finish review names, when the latest run since the last
edit passed (the model's check did not notice):

- **Definitions gone from their package.** A top-level Go function, method,
  type, variable or constant (Python's functions and classes, Scheme's
  defines) that HEAD has in a changed non-test file and no file of the same
  language in its directory defines now; a name in another case counts,
  since a test calls it by name. A name the request gives is left out, whole
  or in words that spell it ("the guard's workspace-kind checker is gone"
  for `WorkspaceKind`, "the first known finding" for `firstKnown`).
  `finish-removed-definitions-message` asks to keep it and have its callers
  go through it with the new behaviour, which a hidden caller and staticcheck
  both accept, or to say which words of the request remove it.
- **Expectations of existing tests.** A line of an existing test
  (`x_test.go`, `test_x.py`, `x_test.py`) that states an expectation, a case
  of a table (a line ending in a comma that holds a literal) or an assertion
  that carries one (testify, Python's `assert`), removed and added back
  nowhere in the change's test files once white space and trailing comments
  are set aside: gofmt's realigned columns and a case moved to another file
  are not removed. A `t.Errorf`'s message and the `if` before it are not
  counted: with them the replay below flagged 84 patches, most of them
  ordinary rewrites. `finish-removed-expectations-message` asks to restore
  each line the request does not change, or to quote the words that change
  it.

`TestTheReviewNamesWhatTheChangeRemoved`, `TestTheReviewIsQuietOnRemovalsTheRequestNames`,
`TestRemovedDefinitionsAreTheOnesGoneFromThePackage`,
`TestRemovedDefinitionsTheRequestNamesAreNotNamed`,
`TestRemovedExpectationsAreCasesAndAssertions`,
`TestChangeRemovalsReadsTheWorkingTree`.

Replayed over the benchmark's patches (each applied to its task's parent,
306 that apply: 233 attempts and 73 original commits): definitions are
named in 12 attempts and 5 originals, among them dsl16's and Pi's `evalOr`;
6 of the 12 remove what the original removes too (e752f151's `errDocsEval`
and `code-workspace?`, 1fd4c3e7's `latex`). Expectations are named in 31
attempts, among them Pi's ef04de9c, 94f5cac4 and 5040f9fe cases; the
originals cannot be counted, since their patches leave out the hidden test
files. So the note stays advisory, and costs a round on its own in at most
about one attempt in six. Not built: running the change's check with the
test files as HEAD had them (report 04's "tests as they were"), which would
name only cases the change breaks, at the cost of a run of the check; and
the planner rule that a `(change ...)` step fixes a function rather than
deleting it (report 02), held for want of more than one case.

## Amendment 2026-10-15: the lines a change adds to its documents stand on the turn's runs

No rule read what a change wrote into its documents and comments, and the
deep study of commit-bench (2026-10-15, reports 05, 07, 09, 10 and 11) found
15 claims in 36 candidate patches that nothing in their run showed:

- the task's own account of its author's fuzz run, "40 s (9.4M executions)
  found nothing", in ADR 25 of three builds: dsl18m and dsl19 never fuzzed
  (dsl19's fuzz runs were refused by the guard), and A's own run, made
  after a reminder, counted 29.5M executions while the ADR kept 9.4M;
- "make validate passed with zero warnings" written before validate ran
  (dsl16, 107251d0; both runs passed later);
- amendment dates guessed: "the day after the last amendment, the natural
  today" (dsl18m), "I'll use 2026-10-03" (dsl19);
- an amendment of ADR 9 pasted twice by one fs-edit whose new text re-typed
  the paragraph before its anchor (A, 5040f9fe);
- 24 Baseten entries of models.json, whose head names models.dev as its
  source, written from memory by dsl18m, dsl19 and A's other run, and
  copied by dsl16 from another agent's catalogue (dc679e46).

So the finish review names them (`change-claims`, internal/prim/claims.go,
hidden; `finish-unshown-claims-message`, one point however many lines):

- **A sentence that reports a run** (in the past tense or "passes with":
  passed, N or zero warnings, found nothing, executions, a fuzz or benchmark
  figure; not a caveat such as "not run here"), in a document or a Go or
  Scheme comment, stands on a run of each command it names (`make validate`,
  `make accept`, `go test`, a fuzz, a benchmark, or any test command) that
  exited 0 in the turn, whose last six lines (of a fuzz run's progress lines,
  the last) hold each figure it gives within 5%. The request's account of
  its author's runs is no such run.
- **An amendment date** the file did not have stands on a command that
  printed it, as `2026-10-15` or `20261015`, or on the request; the message
  suggests `date +%F`. Each date is named once.
- **Three or more lines a document's edit inserted** (120 characters or
  more) that stand word for word elsewhere in it are named, as report 05
  proposed for `editedRegions`; read from the diff at the finish rather than
  in fs-edit's answer, so it is one point of the one message.
- **Entries added by hand to a generated file** (its head says Code
  generated or Generated by, or names a `"source"` address, or a trusted
  `.nodwyn/init.scm` lists it in `generated-files`) stand on a command after
  the last hand edit holding `go generate`, the generator its head names,
  or its source's address or host.

The loop keeps the turn's commands for it (`shown-entry`): each command
line, its exit status (none for a run on the copy of the last commit) and
the first 2,000 and last 8,000 characters it printed, at most 200; the file
tools and the tools that only look are left out, since their answers show
the model's own text. `TestTheReviewNamesClaimsNothingShowed`,
`TestTheReviewIsQuietOnClaimsARunShowed`,
`TestARunClaimStandsOnARunThatPrintedIt`,
`TestRunClaimsNameEachCommandAndLeaveProseAlone`,
`TestAnAmendmentDateStandsOnACommandThatPrintedIt`,
`TestARepeatedParagraphIsNamed`,
`TestHandEditsOfAGeneratedFileStandOnItsGenerator`,
`TestChangeClaimsReadsTheWorkingTree`.

Replayed over the benchmark's patches with each nodwyn run's session log
(its commands, exit statuses and output, and its hand edits): on the 36
diffs of the study 10 runs are named, all for what the reports found: the
fuzz line of A, dsl18m and dsl19, the two guessed dates, A's pasted
paragraph, and the four hand-written catalogues. dsl16's backed validate
line and its own fuzz stay silent. Over the 176 nodwyn attempts with a log,
21 are named; the one false alarm found is a fuzz run read in the
background whose exit no read saw (A, 0a9f700, 3422faf1), which the
harness does not count as a run either. Not built: the server behaviour
stated as known, and a citation the plan made up (report 11's other
cases), which no command's output can settle.

## Amendment 2026-10-15: a dropped part of a condition is named, and plans say which parts stay

On commit-bench's c80b38ac (deep study, 2026-10-15, report 08) dsl19's
planner turned "judge requires ready" into `result.Judge = (state ==
stateReady)`, though the old line it had been given read
`s.options.Config.Jev.Enabled() && ...`. The executor saw the dropped check
four times, named the case where the judge's content is none, and followed
the plan each time; no test had that case, so every check passed and
validate passed. dsl16's planner made the same drop and its executor put the
operand back, "safer". Nothing in the harness looked at a condition the
change removed: the plan's evidence marks a change step done when its file is
written, and check-without takes out whole hunks, and this hunk was needed.

So:

- **The finish review names a dropped part of a condition.**
  `change-dropped-conditions` (internal/prim/operands.go, hidden) reads each
  Go function of a changed non-test file whose body changed: the operands of
  the `&&` and `||` of its old body, and the condition of each old guard (an
  `if` whose block ends in a return), that its new body no longer holds,
  white space and a leading `!` aside. An operand that stands in the body of
  a function of the package the new body calls, one the change added or
  rewrote, is still checked; so are error and ok checks, literals and
  one-letter names, left out; and so is an operand with a word the request
  names (the request is about it). When the latest run since the last edit
  passed, `finish-dropped-conditions-message` says: "Your change no longer
  checks X in F(), and your check passed without it. If the task drops it,
  add a test case where it is false and say so in your report; otherwise
  put it back." Report 08 proposed "your check passes either way"; the
  harness has only run the check without the operand, so it says that.
  `TestTheReviewNamesADroppedCondition`,
  `TestDroppedOperandsAreThePartsTheNewBodyLacks`,
  `TestDroppedOperandsLeaveMovedAndNamedConditionsAlone`,
  `TestChangeDroppedConditionsReadsTheWorkingTree`.
- **A plan step that changes a condition names its old parts.**
  `plan-language-shape` (planspec.scm) asks: "When a step changes an
  existing condition, it names each part of the old condition and says
  whether that part stays." `TestThePlannerSaysWhichPartsOfAConditionStay`.

Replayed over the benchmark's patches: on c80b38ac's original and the four
builds of the study (A, dsl16, dsl18m, dsl19) it names dsl19's
`s.options.Config.Jev.Enabled()` alone; the keychain operands the others
dropped from that line still stand in its `stored` line. Over all patches
that apply (310: a run in progress had added four since the replay of the
removals above) it names something in 24 of 237 attempts and 13 of 73
original commits, most of them conditions the task rewrote on purpose (validate's
book rules on 1fd4c3e7, the pending ask's fields on 48f90ea8), so it stays
advisory. Without the request's words left out it named 53 attempts and 21
originals. Not built: Python and Scheme conditions.

## Amendment 2026-10-16: a changed expectation of an existing test is asked about at the edit

The review of the Muse runs on commit-bench (2026-10-16, eight tasks,
transcripts of dsl20r, dsl16r and Pi) found that both losses of dsl20r and the
one of Pi are one mistake: an existing test fails after the change, and the
agent picks the wrong side.

- **94f5cac4** (dsl20r, at high and at low): the planner wrote "count 0 stays
  and means default", which the request does not state (it says "fs-read's
  start 0 is line 1", and nothing of count). Both runs then rewrote the
  existing row `(fs-read "big.txt" 1 0)`, "count must be at least 1", to
  `1 -1` and `1 -2`; at the high run's round 22 the old failure was printed
  and round 23 called it "failing as expected". dsl16r and Pi kept the row.
  The finish review named the row, as one of six lines of one of four points,
  some twenty rounds after the edits, and the answer defended all six by
  quoting the request's rule for a skipped argument, whose words do not name
  a count given as 0.
- **855e90e7** (Pi, the mirror): an existing test failed after Pi's correct
  wiring, and Pi added an untested `overlapsRepo` skip to production code
  that removes exactly the directories the hidden test expects denied.
  dsl20r met the same failure and fixed the test, as the real change did.

None of the extra verification (review loops, check-without, comparisons)
caught either. The owner's principle is that a harness does not block the
model's choices: it asks, and follows the choice. So both additions below are
a rule and a question, and no edit is refused.

- **A working rule.** Both system prompts say: when an existing test fails
  after the change, decide which side is wrong before editing either; the old
  expectation stands unless the request names the new behaviour for that
  input or case; never add a conditional skip or exception to production code
  (least of all in a guard or denylist) to make an old test pass unless the
  request asks for it, and fix the test's setup when it contradicts what the
  request names; a branch you add gets a test for each side of it.
  `TestThePromptsDecideWhichSideOfAFailingTestIsWrong`.
- **The question at the edit.** After a round whose calls wrote a test file
  (`test-file-path?`: `x_test.go`, `test_x.py`, `x.test.js`, a `tests/`
  folder) that the turn has not been asked about, the evidence loop sets the
  change against HEAD with `change-removals` and, for that file's lines,
  says once (`test-edit-nudge`, `edited-expectations-message`): "Which
  sentence of the request requires the new expectation, for that input or
  case? A rule the request states in general words does not cover an input it
  does not name. If none does, restore the line and change the code to meet
  it; if one does, say which in your answer." It lists each line as
  `path:line: text`, as HEAD had it. The edit is never refused. A test the
  turn added has no line at HEAD, and neither has a case it adds to a table,
  so neither is asked about; a file is asked about once in a turn (the
  evidence loop's state is the turn's), whatever else of it the turn changes.
  The finish review still names every such line when its latest run passed:
  it is the last place to ask, and it is unchanged. The question is about the
  input or case, not the rule: on 94f5cac4 the wrong row's count was given as
  0, and "a skipped argument gets its empty value" does not name it. The
  chat's loop, where a person reads each answer, does not ask.
  `TestAnEditedExpectationOfAnExistingTestIsAskedAbout`,
  `TestTheQuestionOnAnEditedExpectationIsAskedOncePerFile`,
  `TestTheQuestionIsNotAskedOfTestsTheTurnAdded`.

The new tests fail on 204f6f1 (each asserts what that code does not say) and
pass here. The 94f5cac4 patch of dsl20r, applied to its parent and read by
`change-removals`, yields the six lines of the review above: the question
would have been asked three times, at the rounds that edited
`json_test.go` (98, 99), `fs_read_range_test.go` (59, 60: the start row the
request requires and the count row it does not, together) and
`index_grep_test.go` (187, and 188, which was only moved from a map to a
direct assertion, a false alarm of `change-removals` that the finish review
already had). No model was run: whether the rule or the question changes what
a model does is for the next benchmark. Not built: refusing or reverting the
edit; the question for a test deleted or rewritten through the shell (the
finish review names it).

## Amendment 2026-10-16: plans keep unstated behaviour apart, and the review asks for the lines no test ran

Two more losses of the same review (2026-10-16, `muse-review`):

- **94f5cac4**, the plan: "count 0 stays and means default" was written as a
  step although the request never states it, and the exploration's report had
  listed `TestReadRanges` as testing "start 0/count 0 errors" and quoted the
  `count must be at least 1` check. A behaviour the request lacks, planned as
  if it were stated, is what the run then rewrote a test to match.
- **3422faf1** (dsl20r, third replicate): `mergeFindings` held a dead branch,
  `f.Class == ClassUnknownEntropy && curIsBlock`, which tests the wrong
  variable. The plan said only "drop an overlapping unknown-entropy token",
  with no positions; the probes covered a token inside the other and one
  crossing its start, never one crossing its end; the diff review missed it.
  The same test failed in dsl15, an earlier Muse dsl16 build and OpenCode.

So:

- **Plans copy only what the request states.** The planner's prose shape
  (`plan-shape`), the plan language (`plan-language-shape`) and `plan-write`'s
  description say: a step carries only what the request states; behaviour it
  does not state goes on an unnumbered line after the steps, "Assumptions:
  ...", each checked against the existing tests the exploration found before
  a step relies on it, and one a test contradicts is dropped; for each rule
  the request states, the cases are listed by clause, and for a range, an
  overlap or an edge by position: inside, crossing the start, crossing the
  end, equal, empty. In the plan language the line is a part like `names` and
  `context`: `(assumes "behaviour" "the test or code it was checked
  against")`, written out as one unnumbered line (a plan's steps are its
  numbered lines, so the marks and the console are unchanged). Nothing checks
  it. `TestThePlannerKeepsUnstatedBehaviourApartAndListsCases`.
- **The finish review asks for the changed lines no test ran.** When the
  latest run since the last edit passed, a source file (`coverage-extensions`)
  was changed, the lines the change adds hold a branch or a condition
  (`branch-words`, read from `git-diff` and from new files) and no command run
  since the last edit measured coverage (`coverage-run?`), the review adds
  `finish-uncovered-lines-message`: the check passed, which does not show a
  test ran each changed line; run the tests of the changed files once with
  coverage, name the changed lines none executed, and add a test that
  executes each or delete it; if coverage cannot run, read the change against
  the tests. The loop knows which commands passed and none of them says which
  lines ran, so it asks the model to run coverage once rather than parse a
  tool's output. check-without takes out whole hunks and a dead branch lives
  inside a hunk the check needs, which is why 3422faf1's review was quiet.
  Gated on an added branch so a straight-line change does not cost a review of
  its own: none of the earlier finish-review tests adds one, and none changed.
  `TestTheReviewAsksForTheChangedLinesNoTestRan`,
  `TestTestFilesAndBranchesAreToldApart`.

No model was run. The review's point costs a round (the coverage run and what
follows) in each turn that adds a branch and passes; a benchmark run is the
measure of whether that buys the tests it asks for. Not built: reading
coverage output in the loop; a check that an assumption was compared with a
test; the report's other candidates (gate work, sandbox paths, re-reads after
folds).

## Amendment 2026-10-16: the scratch rule says what TMPDIR is, long files are read by the part, and the explorer's reads go with its findings

The review of the nine tasks' dsl20r transcripts (commit-bench, Muse,
2026-10-16, `muse-review-2026-10-16.md`) found two costs the working rules
could name.

- **The scratch directory and the commands that use it.** The model's
  `TMPDIR` was inside the guard-protected data directory: 5 to 17 calls of a
  run (67a1f950, 855e90e7, 7513f7eb) went on tests that failed because their
  temporary directory was a protected path, and on `HOME` and `TMPDIR` set to
  `/tmp` paths (13 of the 23 shell commands refused in the 26 runs named
  `/tmp`) or to a folder of the repository. The directory is now in the
  system's temporary directory (ADR 28, amended 2026-10-16), and the scratch
  rule adds the sentence that follows from it: commands run with `TMPDIR` set
  to it, so a test suite puts its temporary files there without being told;
  do not set `TMPDIR` or `HOME` elsewhere, and do not use `/tmp`.
  `TestPromptsSayWhereScratchIsAndHowToReadLongFiles`.
- **Reading again what the explorer read.** The explorer and the planner are
  folded away, and the model doing the work read again what the explorer had
  read. In the nine high-effort attempts (eight tasks, one of them twice),
  33,000 to 280,000 characters of a run's `fs-read` results were of paths the
  explorer had read, 73 to 100% of the main loop's reads in eight of nine
  runs (37% in the ninth), with the same sizes right after the fold; in 7513f7eb the main loop
  read agents.scm, prompts.scm and egress.go whole again, and docs/scheme.md
  (53,000 characters) nine times, twice whole. Pi, which never folds, reads
  whole files too. Two changes, both information and advice, no forced read:
  - **The lines the findings cite go with them.** The explorer is asked to
    cite places as `path:line` (`plan-explore-task`), and `plan-hand-over`
    adds to the findings message the lines around each place cited, copied
    from what the explorer read as the file has them, under a heading with
    their numbers (`plan-excerpts`: 2 lines before, 12 after, at most
    `plan-excerpts-cap`, 12000, characters in all and 3000 an excerpt; a file
    named without a line only when what the explorer read of it is short). The
    reads are kept by `sub-agent-seen`, which the sub-agent loop fills while
    plan-explore asks for it. The message says to read more of a file in a
    range, and a failure to make the excerpts leaves the findings as they were
    (`TestAFailureToMakeExcerptsLeavesTheFindingsAndThePlan`).
    `TestWhatTheExplorerReadGoesWithItsFindingsToTheModelDoingTheWork`,
    `TestTheExcerptsAreBounded`,
    `TestAReportThatCitesMoreThanFitsGetsTheCapNotMore`. Fewer than half of
    the mentions of a path in the nine explorers' reports carry a line (150
    of 368, the rest a bare name), and a bare one is carried only when short,
    so the excerpts are a floor: they do not replace a read of a file the
    model is going to edit.
  - **Long files are read by the part.** The reading rule names
    `fs-read` with `start` and `count`, and a second rule says to read a long
    file or document (docs/scheme.md, a manual, a large source file) by the
    part needed, found with `index-grep` first, and that what was read or
    given is in front of the model: a range is read again when it changed or
    a result was folded.

Not measured yet: the next run on these tasks is what shows whether the
re-reads fall and whether the 12000 characters pay for themselves; the folds
broke even at best on this review (6e18b7ba). The caps are a first step.

## Amendment 2026-10-16: the closing rounds stop making work

The Muse review of 2026-10-16 (transcripts of nodwyn dsl20r and dsl16r on
eight commit-bench tasks, with Pi) found that the gates and reminders at the end of a
run cost 9-38% of a run's tokens and bought no correctness. Each change below
is one such round:

- **6e18b7ba.** validate regenerated the untracked `docs/expert/expert.pdf`
  (the snapshots the benchmark runs against still built the books), the model
  deleted it, the repository's reminder ("validate has not run since your last
  change") counted the deletion as a change since validate and asked for
  validate again, which made the file again: R96-R104, about 100k tokens, 7%,
  in two builds. In a third the last validate run left the 1 MB file in the
  patch (1,053,931 bytes). On dc679e46 the same happened with
  `docs/book/nodwyn.pdf`, twice.
- **7513f7eb.** About 14 calls (dsl20r) and 30 (dsl16r) went on proving that
  an existing test fails on a pristine copy too, each time after "your last
  test run after your last edit failed"; the model had compared the two sets
  already, on a run the loop did not recognise as the comparison.
- **3422faf1.** A 21-call tail, 22% of the run: the plan named
  `TestExfilScenario019` for a scenario file, the gate wanted that test in the
  file, and the run refactored a test file, regenerated the catalogue and ran
  validate again. dsl16r's `go run ./scripts/validate` through `shell-exec`
  was not counted as validate.
- **The closing check-without rounds** took 9% of a run (dsl20r, high
  effort) and 37-38% in two others (dsl16r, dsl20r at low effort).

So:

- **A deletion of what a validate run left is no change.** The loop keeps the
  untracked files the round's validate runs and `tool-commands` tools made
  (`files-made`: new in `git-status` since the round began, and not written
  by the file tools) and the repository as validate left it (`repo-print`).
  When the only thing that moved since is that some of those files are gone,
  `validate-settle` sets `validated-state` again, so `.nodwyn/init.scm`'s
  reminder is not said; any other difference ends the tracking and the
  reminder stands. The loop cannot change what validate does in a snapshot
  (and `scripts/validate` no longer builds the books, 1fd4c3e, so here it
  leaves nothing), so the validate result carries one line naming what it
  left (`validate-left-note`: delete them unless they are part of the change,
  which needs no new run), where the model can act on it at once, rather than
  the loop deleting a file the model did not choose to delete; and the finish
  review still names a file that stays, saying its deletion needs no new run.
  `TestDeletingWhatValidateLeftIsNoChange`,
  `TestDeletingWhatTheValidateToolLeftIsNoChange`,
  `TestDeletingWhatABackgroundValidateLeftIsNoChange`,
  `TestWhatValidateLeftIsNamedInTheReview`; and
  `TestAnEditAfterValidateIsStillAChange` for the change that is one.
- **`go run ./scripts/validate` is validate.** `validate-commands` lists it
  with `make validate`; a run of either through the shell is a test run
  (`test-command-words`), the whole check (`whole-check-words`), no check
  for check-without to take apart (a shell `make validate` was eligible,
  though the tool's run is not), and sets `validated-state`.
  `TestAShellGoRunValidateCountsAsValidated`,
  `TestAShellValidateMeetsThePlansCheckStep`,
  `TestAValidateRunInTheShellIsNotTakenApart`.
- **A failure the unchanged code has is said once, not proved again.**
  `failure-names` reads the failing tests and packages from a run's output
  (Go, pytest, unittest). The loop keeps those of the runs on the unchanged
  code: any run on `$TMPDIR/base`, and a full test run (no `-run`, `-k` or
  node id) before the turn's first edit, in a turn that began with no tracked
  change. They stay known through later edits, so the comparison is done
  once. A run after the last edit that fails only known tests is not told as
  a failing run; its result says they failed on the unchanged code
  (`failure-known-hint`). When some are known the reminder names them and the
  others (`finish-failing-known-message`: say so in your answer, do not fix
  them). A test is not the unchanged code's to answer for when the request or
  the plan names it, or a file the turn wrote mentions it: the model's own
  test fails on the copy of the last commit as it should, and the test a
  task asks to fix failing again is not done. A package that fails with no
  known test (a build failure, a panic) is not excused either. Not used:
  validate's own "fail on the unchanged code" lines, which are the change's
  tests failing there by design. `TestAFailureTheUnchangedCodeHadIsNotTold`,
  `TestANewFailureIsToldWithTheSharedOnes`,
  `TestAComparisonOnTheCopyIsRememberedAcrossEdits`,
  `TestAFailingTestTheRequestNamesIsStillTold`,
  `TestAFailingTestTheTurnWroteIsStillTold`,
  `TestAFailingValidateAfterAKnownFailureIsStillTold`,
  `TestAFailureBeforeTheFirstEditIsNotTheLastCommitsIfTheTurnBeganDirty`,
  `TestFailureNamesReadWhatTheOutputSays`.
- **A scenario file meets a test step.** A test step whose path runs through
  a `scenarios`, `testdata` or `fixtures` folder is met by that file (the
  repository's, or one the turn wrote whose name starts with the step's path)
  and by a passing `go test` of the folder above; the new file is no stray
  when a step's path starts it (`plan-path-starts?`; only a scenario path, so
  the backup of a planned file is still named). A test the existing runner's
  cases hold under no name of their own is not asked for by name.
  `TestAScenarioFileMeetsItsTestStep`, `TestABackupOfAPlannedFileIsStillNamed`.
- **Nothing is asked again for a run that stands.** The plan's evidence that
  a command passed is cleared only by an edit through the file tools to a
  file that is not a document or in the scratch directory (as before), so a
  deletion made through the shell never cleared it; what asked again with
  nothing changed was `validated-state`, the repository's own reminder, and
  the three points above are the ways it was: a deletion of validate's
  output, a validate run the loop did not count, and a failure already
  compared.
- **The finish review is made at most twice in a run nobody reads.**
  `finish-review-limit` (2), counted in `finish-reviews` over the session's
  turns; a turn after the second ends on its report. A turn that edited
  nothing has no review, so a later review needs new edits as the first did.
  A chat's turn, which a person reads, is not held to it.
  `TestTheFinishReviewIsMadeAtMostTwiceInARun`.

What is kept: every check still runs; check-without still names the parts of
the change a passing check does not test, once a turn; a model that wants
to compare with the unchanged code still can, and the comparison is now kept.
What is not built: the single-turn benchmark runs of dsl20r already had one
review each (the cap above has nothing to do there; its cost is the work the
review's points ask for, which the points that disappear above reduce and the
check-without point, which stays, does not); a plan's check step that failed
only on known failures (7513f7eb, `make accept`) is still asked for; the
chase of a flaky browser check inside validate (c80b38ac, R61-R84) was the
model's, not a reminder's, and nothing here touches it; the plain chat loop
does not run `evidence-loop`, so none of this applies to a chat that is
neither unattended nor planned. None of this was replayed on the benchmark.

## Amendment 2026-10-16: the original tests are run at the finish, a want string is an expectation, and a skip is deleted, not tested

Two more misses of dsl21 (Muse, effort low, `muse-review-2026-10-16.md`), each
reproduced by a reviewer. They are one lesson from both sides: when an old
test fails after a change, which side is wrong depends on the request. The old
assertion stands unless the request names the new behaviour for that input or
case; the fixture's setup may change, and be named, when the request
contradicts it.

- **5040f9fe** (replicate 2): the request added the response's Content-Type to
  an error. The model reworded the error so that "Responses stream ended
  without a terminal response", a phrase an existing test pins, was split, and
  then edited that test's `want := "..."` to match. The real commit left the
  test alone and appended the Content-Type to the intact phrase, so the
  hidden (original) test fails on the patch. The test-edit question of the
  2026-10-16 amendment did not fire: `expectationLine` read the cases of a
  table and testify's asserts, and a want string beside a `strings.Contains`
  is neither. `validate` said the test passed on the unchanged code and the
  model answered "deliberately widened", without a sentence of the request.
  Nine attempts of the benchmark's runs edited the same `want` line (the
  builds dsl3, dsl6, dsl15, dsl17m, dsl19, dsl21, OpenCode, Pi and tok2).
- **855e90e7**: an old test (`TestResumeAfterProcessKilled`) failed after the
  model's correct change because its fixture uses one folder as repository,
  configuration and data directory. The model added a skip to `denylist()`
  (private directories that overlap the repository root) and left the fixture,
  which removes exactly the directories the hidden test expects denied; the
  real commit changed the fixture. The finish review made it worse: "a skip
  nothing exercises: add a test or delete" and "a branch gets a test for each
  side" led the model to add a test that pinned the skip, and check-without,
  run on the exfiltration suite alone (the last passing command), named four
  hunks no test of it covers, which cost 53 calls of per-hunk revert
  experiments; the last six of those runs were told "Your last 6 test commands
  did not run successfully".

The owner's principle holds: nothing is refused, the harness asks, and follows
what the model decides. So:

- **The original tests are run at the finish.** A grader lays the maintainers'
  test files over the patch; the finish review now does the same for the
  files the turn edited. `original-tests` (internal/prim/original.go, hidden,
  registered in a git work tree) takes the turn's changed test paths and, for
  each Go test file HEAD has and the working tree edited or deleted, runs the
  tests HEAD's version declares with HEAD's version in place of the working
  tree's, against the change as it is: `go test -overlay`, so nothing on disk
  is touched and there is nothing to put back if the run is stopped, within
  3 minutes in all, with `GOPROXY=off` and the allowlisted environment. A test
  that also fails with every changed Go file as HEAD had it (a second
  overlay run, only for the failures) was not broken by the change and is
  left out; a directory whose tests do not build with HEAD's files in place
  (the request changed a signature, or a helper moved) is `unrun` and
  named in the log only. A test the turn added to the file, or in a file of
  its own, is not declared by HEAD and is never run. The point is
  `finish-original-tests-message`, in the finish review, so it is said once a
  turn, never refuses anything and never repeats for a file in the turn: "these
  original tests fail on your change", with three ways out: the change is wrong
  (change the code, appending to what a test pins rather than rewording it); the
  request names the new result for that input or case (the assertion changes;
  say which sentence requires it); the request contradicts the test's setup, a
  fixture (change the setup, not its assertions, and say which sentence
  requires it). The middle one is the owner's rule that the old assertion
  stands unless the request names the new behaviour; the first message left it
  out, and a request that changes a result changes the test that pins it, as
  35 of the 244 resolved attempts that edit an existing Go test did (see the
  replay below).
  Go only: another language's tests would need a copy of the tree or an edit
  in place, and the loop says in its log that it did not run them
  (`original-test-failures`). Who runs what: this runs the repository's code,
  the package of a test file the model edited, as the model's own `go test` of
  that package would, in the environment a shell call gets, with no network;
  it is no new authority, and (ADR 45) trust is not a sandbox here either.
  `TestOriginalTestsNameTheTestsTheChangeBreaks`,
  `TestOriginalTestsOfADeletedFileAndOfOneThatDoesNotBuild`,
  `TestOriginalTestsPrimitive`, `TestTheReviewNamesOriginalTestsTheChangeBreaks`,
  `TestTheReviewIsQuietWhenTheOriginalTestsPass`,
  `TestOriginalTestsAreRunForGoOnly`.
- **A want string is an expectation.** `expectationLine` also reads a literal
  assigned to `want`, `expect`, `expected` or a name that starts with one
  (`wantErr`, `expected_msg`; Go and Python), and in Go
  a `strings` or `bytes` check (`Contains`, `ContainsAny`, `HasPrefix`,
  `HasSuffix`, `EqualFold`) of a non-empty string literal, a switch's `case`
  line left out, which picks the expectation and is not one: the question at the
  edit and the finish review both read them through `change-removals`. Moved lines are not changes, as
  before for a line added whole elsewhere, and now also for a line whose string
  literals all stand on one line the change adds (`kept`): the 94f5cac4 replay
  named a case that had only moved from a map to a direct assertion. The
  question's text adds that to add to what a test pins, the model appends to the
  phrase and does not reword it.
  `TestRemovedExpectationsAreAlsoWantsAndPhraseChecks`,
  `TestExpectationLinesOfWantsAndPhraseChecks`, `TestStringLiterals`,
  `TestAnEditedWantStringOfAnExistingTestIsAskedAbout`.
- **A skip is deleted, not tested.** The working rule says: a branch the
  request asks for gets a test for each side of it; a branch added for any other
  reason is not tested into place, delete it; and a fixture is setup, its
  assertions are not. `finish-uncovered-lines-message` offers, for a branch no
  case reaches (a condition that cannot be true, a skip nothing exercises),
  deleting it or quoting the sentence of the request that requires it, adding the
  test only then, and says a skip or exception added after an existing test
  failed gets no test, since the test would pin it: fix that test's setup or the
  code instead. `finish-untested-parts-message` ends the same way: a part the
  request does not ask for is removed, or defended by the sentence that
  requires it, not given a test.
  `TestThePromptsDecideWhichSideOfAFailingTestIsWrong`,
  `TestTheReviewAsksForTheChangedLinesNoTestRan`,
  `TestTheUntestedPartsPointDoesNotTestASkipIntoPlace`.
- **The check taken apart is the broadest passing run, and a revert experiment
  is not a run that failed to run.** check-without and the coverage point used
  the last passing command, the exfiltration suite on 855e90e7. They use
  `broadest-passing` now: of the commands that passed since the last edit, the
  one that names the most of the changed files' directories (`./...` and the
  whole-check words name all of them), one not narrowed with `-run` or `-k`
  before one that is, the newest of those that tie, so a turn with one passing
  command is as before. `unrunnable-after` counted every failing test run, edits
  between them leaving the count alone. A failing run now ends the count when
  its output shows tests ran (`tests-ran?`: a failing test named, a test or
  package that passed, or a package that says `FAIL` with a time of its own),
  and leaves it where it was after an edit that took part of the model's own
  change out (`shrinking-edit?`: the file's changes against HEAD got smaller,
  measured with `git-diff` from the turn's first command on, since a part is
  taken out to see a test fail only after one has run), which is a revert
  experiment, expected to fail. A run on `$TMPDIR/base` did
  not count already. `TestBroadestPassingPicksTheRunThatRunsTheMostOfTheChange`,
  `TestTheReviewTakesApartTheBroadestPassingCheck`,
  `TestTheLastOfEqualPassingChecksIsTheOneTakenApart`,
  `TestTheCoveragePointNamesTheBroadestPassingCheck`,
  `TestFailingTestsAreNotCommandsThatDidNotRun`,
  `TestRevertExperimentsAreNotCommandsThatDidNotRun`,
  `TestGrowingEditsFollowedByFailingRunsStillCountAsUnrunnable`.

Every new test fails on 95834ba (dsl21) or cannot compile there: the three
`TestOriginalTests...` of the primitive and `TestStringLiterals` call functions
the old code lacks, and `TestBroadestPassingPicksTheRunThatRunsTheMostOfTheChange`
calls Scheme functions it lacks. `TestTheReviewIsQuietWhenTheOriginalTestsPass`
asserts an absence, and fails there only through the row that moved into a
direct check, which the old code asks about.
The replay (each patch applied to its task's parent and read by the new code, no
model run; the 291 distinct patches of the benchmark's runs that edit an
existing Go test file): the 5040f9fe patch of this miss yields the `want` line
at responses_test.go:280 from `change-removals` and, from `original-tests`, the
hidden test itself, `TestResponsesEventStreamRejectsMissingOrFailedTerminal`.
`change-removals` names expectations in 37 of the 291 patches before and in 50
after, and 98 lines before against 87 after: 12 patches stop being named (a map
every attempt of 107251d0 extended and rewrapped, table rows whose literals all
stand on a line the change adds, 94f5cac4's `index-grep` case that moved from a
map to a direct assertion), 25 are named
that were not, nine of them 5040f9fe's `want` line and the others phrase checks
and want strings that were changed. `original-tests` names failing tests in 51
of them: 35 of the 244 resolved attempts (14%) and 14 of the 39 unresolved
(36%), 2 of the 8 not graded. The tasks it names are the known misses: all 8
attempts of 94f5cac4 (`TestReadRanges` among them), and for 5040f9fe the three
patches whose error no longer holds the pinned phrase (this one, not graded,
and OpenCode's and Pi's, both unresolved on exactly the test named,
`TestResponsesEventStreamRejectsMissingOrFailedTerminal`) against none of the
20 resolved ones. The resolved
attempts named are the ones whose request changes a result, so the test that
pins it changes: 107251d0's 17 (the Responses round trip), 855e90e7's 6 (they
edited the fixture of `TestResumeAfterProcessKilled` as the real commit did,
and it fails as the last commit had it, which is the point's third way out),
67a1f950's 6, 7513f7eb's 2 and four more. The point therefore costs a round
for about one attempt in seven that edits a Go test (one in six counting the
unresolved), and asks the model for the sentence of the request. 7 patches (2%) are unrun. The dsl21 patch of 855e90e7 is not named:
it did not edit `resume_test.go`, the skip was in production code, and that is
what the third and fourth points address (the finish review's own text led to
the test of the skip, and check-without on the exfiltration suite to the 53
calls).

Not built: running the original tests of another language (pytest would need a
copy of the tree); naming failing tests of files the turn did not edit (the
model's own runs show those; 855e90e7's resume test was not edited, and the
skip is what the third and fourth points ask about); recognising a revert
experiment by what the model says; refusing or reverting any edit. Not
measured: whether the new points change what a model does is for the next
benchmark.

## Amendment 2026-10-16: the review stops asking for the lines no test ran, bounds the tests it asks for, and the question on an edited test is answered in a form and read

dsl22 (a000653, Muse, effort low, 32 runs; `muse-review-2026-10-16.md` and its
digests) resolved 31 of 32, as dsl20r did, for about 17% more cost, and still
failed 94f5cac4. Two findings.

**Cost: 6e18b7ba took 167 calls and 2.38M cost-eq (uncached input, a tenth of
cached input and five times the output), against 0.83M for dsl20r.** The
first 65 calls cost 0.59M. The finish review came at 400 s with three
test-writing points, and the model spent R66-R160 answering them, which the
model's own reasoning summaries and its coverage runs attribute:

| calls | cost-eq | what asked for it |
|---|---|---|
| R1-R65, before the review | 0.59M | |
| R66-R98 (33) | 0.52M | the plan's evidence (three test steps: two passed on the unchanged code, one was not written) and check-without's six small hunks, four of one line: a concurrency test, a PATH-shadowing git wrapper to assert a flag |
| R99-R160 (62) | 1.12M | the coverage point (`finish-uncovered-lines-message`): the model ran `go test -coverprofile`, mapped the zero-count blocks to the changed lines and wrote tests for the branches of its own defensive code, the waiter and retry paths of `Restore`, the empty-list branch of `versions.list` ("extending the RPC versions test to cover the store fallback and empty-list branches") |
| R161-R167 (7) | 0.14M | validate and the report |

- **The coverage point goes**: its trigger (`uncovered-lines-due?`: the latest
  run passed, a source file changed, the added lines hold a branch word, no
  command measured coverage) and its message, with the helpers only they used
  (`added-source-lines`, `branch-words`, `branchy?`, `coverage-run?`). It was
  added on 2026-10-16 for 3422faf1 (a dead branch that tested the wrong
  variable), and that miss is the planner's to prevent, which the same
  amendment's cases by position and assumptions do at the cost of a prompt, not
  of a run: they stay. In 6e18b7ba the point took 62 of 167 calls and 1.12M of
  2.38M cost-eq, for tests of code the request did not ask for.
  `coverage-source?` stays as `source-path?`, which `changed-dirs` (and so
  `broadest-passing`) reads.
- **The tests a review asks for are bounded.** `finish-ask` names at most
  `finish-ask-limit` (3) items of each kind: the parts check-without found
  (the largest first), the plan's test steps that are missing or pass on the
  unchanged code ("make it fail there"), and the dropped conditions; says how
  many more there were; and keeps every item it considered in `finish-asked`,
  so a later review of the run (`finish-review-limit` is 2) does not name it
  again, the ones past the limit included: the model answered the review
  once. `finish-untested-parts-message` asks for a case only for a part that
  implements what the request asks for, says "do not test anything else
  because of this message", and says a flag, a rename, a refactor, a skip or an
  exception added to make a test pass gets no test: remove it, or defend it with
  the sentence of the request that requires it. The review is still made once
  a turn.
  `TestTheReviewDoesNotAskForTheChangedLinesNoTestRan`,
  `TestTheUntestedPartsPointNamesTheLargestThree`,
  `TestAnItemTheReviewAskedAboutIsNotAskedAboutAgain`,
  `TestThePlansUnwrittenTestsAreNamedThreeAtATime`,
  `TestTheUntestedPartsPointDoesNotTestASkipIntoPlace` (changed).
  `TestTheReviewAsksForTheChangedLinesNoTestRan` and
  `TestTheCoveragePointNamesTheBroadestPassingCheck` are gone with the point;
  `TestTestFilesAndBranchesAreToldApart` is
  `TestTestFilesAndSourceFilesAreToldApart`.

**94f5cac4 again: the question was asked three times and waved away.** The
test-edit question fired at 73, 85 and 89 s (the ends of R26, R30 and R31).
The model answered the first (R27) with "restoring the type-check case", and
then (R28, R31) with the request's rule for a skipped argument (`A skipped
optional argument before a later one that is given now gets its type's empty
value ("", 0, #f, ())`), which it applied to `(fs-read "big.txt" 1 0)` "count
must be at least 1": a row the request never mentions (the plan had invented
"count 0 means default"), and the hidden (original) test keeps it. The patch rewrote it
to `1 -1`. A prose answer the loop did not read cost nothing to give.

- **The answer has a form, and is read.** The question asks for one line for
  each changed row: the row, then a sentence copied word for word from the
  request that names the input or case the row tests, or `none` (restore it and
  change the code). The next reply with words is set against the request by
  `edit-support` (`edit-answers`, strategy.scm), which judges text and nothing
  else: a quote is a run of three words or more of the reply that stands in the
  request in the same order (case, punctuation and white space aside), and it
  supports a row when it shares a distinctive token with the row's string
  literals, its input and the message it expects, and with the line that
  replaces it (`change-removals` pairs the lines a change block removes and adds
  by the words they share, and says the replacement in `new`). A token is a word
  of three letters or more, or a number from 2 up (0 and 1 are in every sentence
  about an index or a count), lightly stemmed, that is no stop word and no part of
  the name of a function the row calls (a sentence that names `fs-read` says
  nothing of which case of it the row tests); a row whose literals give none is
  read by its identifiers' words. A row with no token to judge by is left to the
  review's other point, as before.
- **A rewrite that cuts or splits a phrase needs more.** 5040f9fe's pinned
  phrase: the request has the error name its Content-Type, which adds to the
  phrase, and the attempts that appended " (Content-Type: text/event-stream)" to
  it passed the maintainers' test, while the one that cut the want string to
  "ended without a terminal response" and OpenCode's, which put `(content type
  "text/event-stream")` inside it, failed it. When the last string literal of the
  replacement keeps the phrase whole at its start or its end, it is an append and
  no more is asked. When it cuts two words or more, the quote must also hold most
  of the words cut ("responses stream"); when it puts two words or more inside
  the phrase, all the words put, as a run. The sentence about the error
  ("A stream that ends without a terminal event names its Content-Type in the
  error") supports the append and not the cut or the split.
- **A row without a supported quote gets one firmer message**, when the reply
  quoted the request at all and the row is still unsupported
  (`edited-expectations-firm-message`, once for each row, so the second wrong
  quote is not told again): it names what the request does not mention (the
  input and the expectation, as the request is read for a quote; the words cut or
  put), says the default is to restore the old expectation and change the code
  because the last commit's version of that test is what a grader runs, and that
  the row is kept only by quoting the sentence that names its input or case, and
  saying why it requires the new result. A reply that quotes nothing is no
  answer yet; the reply that ends the turn is read for the rows it supports and
  gets no message (the review is firm enough, and a message there would be
  another round). A row the model restored is answered.
- **The finish review lists what is still unsupported as its first point, with
  the original tests** (`finish-unsupported-edits-message`: the rows, and the
  `finish-original-tests-message` failures, the grader's side of the same rows,
  in the same point). The rows whose answer was read and supported are not
  named again in the removed-expectations point.
- **Nothing is reverted or refused.** The edit stands; the model may decide
  otherwise, against the evidence.
  `TestQuotesAreRunsOfTheRequest`, `TestTheTokensOfARow`,
  `TestTheRepliesOf94f5cac4AreReadRowByRow`,
  `TestRewritesArePairedByWhatTheyShare`,
  `TestAnAppendedPhraseIsSupportedAndACutOrASplitIsNot`,
  `TestARewrappedMapTheRequestAddsToIsSupported`,
  `TestARowWithNoTokenIsUnchecked`, `TestEditSupportPrimitive`
  (internal/prim, with the requests, patches and replies of the benchmark in
  `testdata/support`); `TestAnAnswerIsReadAgainstTheRequest`,
  `TestAReplyThatQuotesNothingIsNoAnswerYet`,
  `TestTheAnswerThatEndsTheTurnIsRead`, `TestTheFirmerMessageIsSaidOnce`
  (internal/core); `TestAnEditedExpectationOfAnExistingTestIsAskedAbout`
  (changed).

Tuned on the benchmark's own patches and replies, not on a model run.
`TestSupportReplaysCommitBench` (set `NODWYN_COMMIT_BENCH` and `NODWYN_REPO`;
skipped without them) reads the 304 attempt patches of the benchmark's runs
(the gold patches left out) for the rows `change-removals` names and sets each
against its task's request, twice: with every sentence of the request as a
possible quote (the most the request can do for a row) and with the sentence a
model would most likely quote, the one that shares the most tokens with the row.

- 53 patches name 94 rows. Some sentence of the request supports 81 of them;
  the natural quote supports 80. Of the 48 rows the real commit also changes
  (its test diff, read from the repository), 41 are supported either way; the 7
  that are not are a4dbffc7's two rows in four attempts (one has only the second),
  where the attempt put its own sentence in the phrase ("a step you reword starts
  unmarked") and the real commit cut "since that starts its marks over"
  (supported by "The reading reminder no longer says a rewrite starts the marks
  over").
- 5040f9fe: 9 patches rewrite the `want` line, and the natural quote flags the
  two that fail the maintainers' test with a phrase it can read, the cut one
  (`want := "ended without a terminal response"`, dsl21's second replicate, not
  graded) and OpenCode's split one (unresolved); the five that appended to the
  phrase (dsl3, dsl6, dsl15, dsl17m, dsl19, all resolved) are supported. tok2
  (resolved) and Pi's (unresolved on that test) restructured the test so that no
  line replaces the old one, and are supported. Some sentence of the request
  supports the cut one: the title, which holds "Responses" and "stream" and is
  about neither the error nor the test, so the any-sentence count flags one row
  fewer than the natural quote.
- 107251d0: 5 patches, 5 rows (a map rewrapped around "instructions" in four,
  one `{Role: RoleSystem, Content: "sys"}` row), all supported. 94f5cac4's other
  correct rows (`(fs-read "big.txt" 0)`, the `index-grep` context, the two
  `count needs start too` cases) are supported in all 9 of its patches.
- 94f5cac4's `(fs-read "big.txt" 1 0)` row, rewritten to `1 -1` (or `1 -2`) in 5
  of those patches, is supported by the natural quote: the sentences that
  mention "count" do so of another message ("count needs start too"), so a model
  that quoted that sentence for it would pass. None did. Set against the real
  replies of dsl22 (R27, R28, R31, R32) the check reads: R27 quotes nothing; R28
  supports the two `count` cases of `json_test.go` (the rule's "argument" is a
  word of their message); R31 supports `(fs-read "big.txt" 0)` with
  `fs-read's start 0 is line 1` and does not support `1 0`, for which it quoted
  the rule for a skipped argument; R32 supports the `index-grep` row. Set against
  dsl21's three replies to the same question, all four rows it rewrote are
  supported. So the one row the check tells the model about in either run is the
  one the hidden test keeps.
- Flagged and not discussed above: the 14 rows of the 94 flagged are a4dbffc7 7,
  c4cde303 4, 5040f9fe 2 (above) and 94f5cac4 1. c4cde303's is a
  `strings.Contains` moved inside an `if` block by a refactor (its literals stand
  on two added lines, so `kept` does not hold it), and 94f5cac4's is dsl20r's
  `{"path":"p"}`: `1,` number table. `change-removals` named both before.

Not built: a second firmer message, a message to the reply that ends the turn,
reverting or refusing any edit, and a stricter reading of "count" (the rule is
one shared token; two made the real `count`/`start` cases of
94f5cac4 unsupported by the sentence the model quoted for them, and could not
tell `count needs start too` from the row it is not about). Not measured: whether
the model changes what it does with the form and the firmer message, and
whether three tests a review is enough; for the next benchmark.

## Amendment 2026-10-16: the benchmark stack merged with main

The benchmark stack `claude/dsl23` and main changed the same loop on two
branches. The merge keeps both, with these choices:

- **Per-turn resets the stack added are made in `turn-start`**, where main's
  amendment "benchmark habits fire only where they fit" moved the others:
  the write round's `turn-stop-tools` and `turn-stop-rounds`
  (strategy.scm), the plan language's state for the turn (planspec.scm
  had wrapped `nodwyn-turn`, so a project that plans its own way kept the
  last turn's plan steps), and `sub-agent-seen`, which a stop inside the
  explorer left collecting every later sub-agent's calls.
- **The explorer's rounds are the sub-agent's `rounds` option** (main), what
  it read is still collected for the excerpts it hands over (the stack),
  and it has `index-search` as well.
- **A compaction gives the turn's plan back once**, as the message after
  the note (the stack's `compact-plan`, the way commit-bench ran it). Main's
  `plan-carry`, built on its own branch for the same failure, stands down
  then, and records the plan again only when that message cannot be made
  (`compact-plan` gives `#f`): `TestCompactionCarriesThePlanWithItsMarks`
  holds the first, `TestCompactionRecordsThePlanTheLogCannotGive` the
  second, and `TestCompactionKeepsTheTurnsPlan` both. Both at once put the
  plan twice in a compacted context, a few hundred to about 2,000 tokens
  more.
- **The finish review's diff readers use main's bounded `myers`**
  (removed expectations, the change without each part, the documents'
  added lines), and leave out a change too large to diff, as they leave
  out a file too long.

## Amendment 2026-10-16: what the evidence loop was given on 2026-10-12, recorded

The commits of `claude/dsl23` made on 2026-10-12 cited an amendment of
that date that was never written. This records what they decided, from
their messages, and the code and documents that cited it now cite this one.

- **A check is compared by what it prints** (33b6b87). `check-without`
  runs the check once with the whole change, and counts a part untested
  only when the check exits 0 and prints the same without it: a check
  ending in `; echo EXIT=$?` passed without every part on commit-bench.
- **The commands a report cites are read as the shell reads them**
  (33b6b87). They are matched against the commands the turn ran, read by
  `guard.ShellCommands` (`shell-commands`), so heredoc lines and grep
  patterns are no programs; runs on `$TMPDIR/base` never go stale, and a
  passing whole check (make validate, make test, ...) covers the check
  commands a report cites.
- **A background test run counts** (33b6b87, c768750, 11e0c51). A
  background run that exits after the last edit is a test run, and only
  its own calls are read for its exit. `check-without` runs it again as the
  shell-start call the model made, never as one rebuilt from the shell's
  output, which carried that output's taint and was refused by the guard
  as a command built from tool output (commit-bench, 556d6d12).
- **Slow checks go to shell-exec, and a check's exit status is not
  masked** (33b6b87): the system prompt says both, and shell-read may wait
  600 seconds.
- **A plan step is marked with the next step's first call** (1fbbac4). On
  commit-bench (nodwyn 0a9f700, nine tasks) 69 of the 103 replies that
  marked a step held nothing else, each a request that sends the whole
  context again: plan-step, the plan header and a run's turn prompt ask for
  the mark in the same reply as the next step's first call, with a
  one-line note.
- **A name the request lists is taken as spelled** (c75286f). On
  commit-bench (556d6d12) the request named three headers; the planner read
  session-id as the X-Session-Id the code already sent and planned the
  other two, and the main loop shared the belief without testing it, so
  the change failed the maintainers' test. plan-shape asks that every name
  the request lists appear in a step as spelled, with a Test: step of the
  request's spelling when the planner thinks it is already there, and the
  working rules ask the main loop to show such a belief with a run.

## Amendment 2026-10-16: the plan as data of 2026-10-13, recorded

The commits of `claude/dsl23` made on 2026-10-13 built the plan language
(planspec.scm) and cited an amendment of that date that was never written.
This records what was built and what stayed, from their messages, and the
code and documents that cited it now cite this one.

- **The planner writes its plan as Scheme forms** (e73155c): change, test
  (with how it fails today and the command that runs it), docs, check,
  none, names and context, read with `read-data` and written out as the
  numbered steps the window and the console show. A reply whose `(run ...)`
  sits inside `(fails-today ...)`, a parenthesis short, is read form by
  form with the parentheses added (75503e3). A plan the model writes again
  in prose replaces the forms (78a353a), and each form is kept under the
  new plan's step of its kind that names its file, test or command; a form
  the rewrite dropped is dropped (e592a64).
- **Every name the request gives is in the plan**, as something the change
  produces or only uses, or the plan is asked for again (e73155c).
  `request-names` takes the names that read as code and leaves out words in
  capitals and capitalised names without a separator: the planner had been
  asked again for AGENTS.md, ChatGPT and NODWYN_RUN_COMPLETE (75503e3). A
  phrase is a quote, and a name HEAD already holds whole (`head-has-name`)
  is no name the change must produce: on 5040f9fe the model, told the
  change lacked such names, wrote them into its ADR (6ff12b7). A name the
  plan leaves out and HEAD has goes into the plan's context without asking
  the planner again (7595b15).
- **Before an unattended turn ends, each step is set against what
  happened** (e73155c, `plan-evidence`): an unchanged file, a test not in
  its file or not run since the last edit, a check not passed since, a name
  the change does not contain, said once. A step that names a directory is
  met by an edit in it (78a353a), and an edit to a .md, .tex, .rst or .adoc
  file leaves the runs before it standing (75503e3).
- **The harness marks the steps as the work happens** (5f0739e): after each
  round of the main context's calls, a change or docs step whose file a
  call wrote, a test step whose file was written and whose test a passing
  command named, and a check step whose command passed.
- **A planned test that says it fails today is run once on the unchanged
  code** (5f0739e, 7f0ed56, `test-before`): on a copy of HEAD with the test
  file and the change's other test files and test support put in; one that
  passes there does not show the change, and is said so. With the test file
  alone, two tests that drive the change through a fake it rewrote passed
  on the copy (dsl13, 107251d0).
- **A background run is read to its end** (3391516): shell-read waits up to
  a second after new output for the rest or the exit, since make validate's
  PASS came with "running", the model read no further, and the loop never
  saw the run end (dsl6, 556d6d12).
- **The documents that describe what the change alters are named when it
  edited none** (43d920d, 28c9ed4, `docs-mentioning`): those that hold a
  function the plan changes, and the ADRs named for a package the change
  alters. On 3422faf1 the planner wrote `(none docs ...)` and the reference
  amended ADR 25, which describes the Scanner.Scan and TextSecret the
  change altered.
- **New files are marked for the next commit as the model writes them**
  (7d48641, 96a304d, `git-intend`): every new file of a planned turn, so a
  generator that lists the tracked files counts it. The book's catalogue,
  regenerated last, had missed a new test file (dsl8, 107251d0) and a new
  exfil scenario (3422faf1). Files untracked before the turn are left
  alone.
- **Dropped the same day** (550e3b9): the harness running the plan's tests
  after each edit (961f0e3) and its check before the end (3391516),
  check-before on a clone of HEAD (b7befea) and the missing-program hint
  (817b637). dsl6, which had them, scored 75.5 in blind review against
  dsl3's 77.5 on the same three tasks, for 18% more tokens; check-before
  never fired, since make validate passes on the unchanged code. qwen's
  explorer went to 8 rounds (3e91d63) and back to 16 (be6a8f2) when a blind
  review scored the 8-round builds lower; the amendment of 2026-10-15 ("a
  shorter explorer for qwen") made it 8 again.

## Amendment 2026-10-16: the original tests in any language, and what eight SWE-bench misses showed

dsl23 on SWE-bench Verified's 121 clean tasks (Muse, effort low) resolved 98
against Pi's 104; eight tasks Pi resolved and dsl23 did not were read one by
one. The second replicate then showed most of them were not stable: dsl23
resolved four of the eight, and Pi missed three that it had resolved. Two
held (django-15916, django-16877), the rest were the loop's own gaps, each
with a task it cost in at least one run. The loop's guards for them were Go's
alone: `original-tests` ran Go tests and said in the log that it ran no other.

- **`original-tests` takes the check, not a language** (`internal/prim/original.go`).
  The evidence loop gives it the program and arguments of the passing run that
  runs the edited test files (`original-check`; a run that names none of them,
  or none that passed, is not repeated, and the log says so). The test files
  the turn edited are written as HEAD has them and the ones it made are taken
  away, the check runs, every file goes back to its bytes and its time (a
  bytecode cache keyed on a source's time and size must not take one version
  for the other); if it fails, it runs once more with every changed file as
  HEAD had it, and only a test that fails with the originals and not there is
  named. Names come from the output (pytest, unittest and Django, `go test`,
  cargo, jest, vitest, rspec: `failureIDs`); a failure whose output names no
  test is told by its exit status. The Go overlay is gone. pytest-5787 and
  scikit-learn-13124 rewrote an existing test, in Python.
- **A run that failed is held against the last commit** (`regression-failures`,
  `finish-regressed-tests-message`): the same command, no file put back, and
  with every changed file as HEAD had it; the tests that fail only with the
  change are named. psf-requests-2931 broke an existing test, ran the whole
  file, and put 81 failures down to the environment, its own among them.
- **A run's output is read in every ecosystem** (`passing-lines`,
  `failing-output?`, `failure-line-name`): "3 passed", "OK", "test result:
  ok.", "1 failed", "FAILED (failures=1)", cargo's, jest's and rspec's lines.
  Until now only Go's and validate's counted, so a Python run piped through
  `tail` was no run that passed.
- **The plan's run and check match by what they run** (`plan-language-ran?`):
  flags, numbers and launchers do not count, so `-v 1` against `-v 2`, or
  `python -m pytest x` against `pytest x`, are the same run. The review said a
  check "has not passed since your last edit" for the first on two tasks.
- **A task template's fields are not names the plan must hold**
  (`request-names`): the key of a "- key: value" line (`instance_id`,
  `base_commit`), a product's name (`SWE-bench`), and a name with a version
  inside (a platform string). The planner was asked again for them on all
  eight tasks reviewed.
- **The expectation question reads more test files** (`expectationTest`):
  tests by name or folder, in Go, Python, JavaScript and TypeScript, Ruby,
  Rust, Java, Kotlin, C#, PHP; `expect(`, `assert_eq!`, `assertEquals(` as
  well as Go's and Python's assertions. Django's `tests.py` was not read.
- **The harness never measures a path outside the repository** (`repo-relative?`):
  `edit-sizes` called `git-diff` on the path of a write the guard was about
  to refuse, the guard refused the harness's own call, which a strategy cannot
  catch, and django-11400 ended with `denied: path /tmp/check_order.py` at its
  first write to /tmp after a run (one of 121 tasks).
- **A habit line**: before finishing, say the request's title as a claim and
  check its smallest form (django-15916, where the probe printed `Base2:
  False` for the plain ModelForm and the run moved on).
- **The plan prompt's example is Python's and says a test's run is the
  repository's own command.**

Not built, and why: `change-dropped-conditions` stays Go's, since it reads Go
syntax; the explorer is not given `git log` (one task, a new primitive and the
documents that come with it; the failed-run point above covers requests-2931
from the other side); and the plan-form changes proposed for django-11400 and
django-14534 (Pi missed both in the second replicate, so they are not shown to
be the loop's). The bridge drops new test files from the submitted patch for
both harnesses (django-16877: a new file at the path the hidden test creates
makes the evaluation's `git apply` fail), in `swebench_nibb_bridge.py`, not
here.

## Amendment 2026-10-17: the default loop has a lean profile and a full one

The working habits above were measured on a benchmark, and the same benchmark
now says most of what the loop adds around the model does not pay. On
SWE-bench Verified's 121 clean tasks (Muse, effort low, replicates of each
arm), the loop of dsl23, with these habits, the explorer and the planner,
resolved 98 and 98; Pi, which has none of them and four tools, resolved 104,
106 and 103. The lean ladder took the loop apart one rung at a time, each
rung a switch in the Scheme files, and measured what each cost:

- **`plan-minimal`**: a run records the one line `plan-minimal-text` and neither
  explores nor plans. The explorer and the planner were 31% of a task's cost
  (1,081 requests in 121 tasks for the first, 245 for the second).
- **`lean-quiet`**: nothing the harness says while the model works or as it
  ends: no finish review, no reminders, no question on an edited test, no hint
  on failures the unchanged code has. Of dsl23's 4,385 requests in 121 tasks,
  707 came after the finish review began, 17% of its cost. A turn that changed
  nothing is still told so once (`lean-empty-reminders`): shell2 on
  matplotlib-25960 ended its turn with no edit and no patch.
- **`lean-prompt`, `lean-tools`**: a system prompt of about 250 tokens where
  dsl23's is 12,240 characters, and the tools the control model is
  offered named in a list. Pi's first request is 2.2k tokens against nodwyn's
  15.7k. Last, `lean-tools` went from six tools to two: on the 121 tasks with
  two replicates each, `shell-exec` and `fs-edit` alone resolved 103 and 102
  where `fs-read`, `fs-list`, `index-grep`, `fs-edit`, `fs-write` and
  `shell-exec` resolved 102 and 101 (Pi 104, 106, 103), at about 20% lower
  cost per task: the model reads with `sed` ranges, and its tool results total
  32,600 characters a session against 51,500. `fs-write` added to the pair was
  called 5 times in 242 sessions, and the code index (`outline`, `tests-for`
  and the commands of the binary, `lean-cli`) in about 13%, neither with an
  effect on the score.
- **Tools that say what to do** (from six readers' pass over 136 transcripts):
  an `fs-edit` error echoes a valid call, names every line an ambiguous `old`
  occurs on and says where a longer one stops matching; its answer counts the
  lines added and removed (a swapped line was "no change"); `fs-write` says how
  many lines it wrote and warns when most of a file went (one run's rewrite
  dropped 117 of 426 lines and the ImportError showed three calls later);
  `index-grep` searches a large tree's source files instead of refusing it (33
  of 748 calls in the transcripts); `shell-exec` notes exit 141 (matplotlib-25960:
  `grep ... | head` ended 141 and was not noticed) and a read that printed
  nothing (django-14534: a `sed -n` range past the end of the file cost four
  calls of 20); the denial of a `/tmp` path gives the scratch directory's own
  path. The reading nudge counts a `shell-exec` call that only reads
  (`lean-shell-look`): shell2 made 21 read-only calls on matplotlib-25960, was
  never nudged, and ended with no edit.
- **Experiments, offered to no one by default**: the `scheme` tool, a program
  that calls the other tools as procedures through the guard; `outline` and
  `tests-for`, with `fs-read`'s `symbol`; and `lean-cli`, the same index as
  commands of the binary run through `shell-exec`.

**Decision.** The lean loop becomes the default of an unattended session, and
the loop before it stays, as the default of one a person is in. `[loop]
profile` in `config.toml` is `"auto"` (the default), `"lean"` or `"full"`;
`NODWYN_PROFILE`, when it is not empty, overrides it, for a test or one run.
`auto` is lean when nobody approves the session's calls (`(unattended?)`:
`--yolo`, a run's `auto_approve`, judge or unattended) and full when a person
does. The lean prompt says "You work unattended: nobody approves or watches
your calls", which is false in a session a person answers, and everything
above was measured on runs nobody watched; the interactive loop is neither
shorter for its person nor better for it, as far as anyone has measured.
The profile is resolved when the session starts and fixed with its prompt and
tools (ADR 9); a resumed session resolves it again. The person chooses it, in
`config.toml` or the environment: a repository cannot, since nothing in the
repository is read for it. Its `init.scm`, which loads only after the person
trusted that revision (ADR 45), may redefine the switches below as it may
redefine any prompt, and has no more authority for it than for that.

The Scheme files keep the lean values, since lean is what the binary does by
default, and `internal/loop/full.scm` is evaluated after them and before
`.nodwyn/init.scm` when the profile is full, so a project's `init.scm` has the
last word under either and can mix the two switch by switch:

```scheme
(define lean-prompt #f)
(define plan-minimal #f)
(define lean-quiet #f)
(define finish-reminders 2)
(define finish-review-limit 2)
(define lean-empty-reminders 0)
(define lean-shell-look #f)
```

The three numbers are computed from `lean-quiet` when `strategy.scm` loads, so
setting `lean-quiet` alone does not bring them back. In the Go host
`prim.Options.Lean` carries the tool surface the same way: only a lean session
registers `scheme`, `outline` and `tests-for`, gives `fs-read` its `symbol`
parameter, counts the lines an edit added and removed, has `fs-write` say what
it wrote, lets `index-grep` search source files alone in a tree of more
than 3000, gives `fs-edit` its `line`, its closest text and its note of other
copies, and `shell-exec` its view of a long stream and its saved files. A full session's catalogue, tool schemas and answers are the ones
it had. `TestFullProfileTurnsOffEverySwitchTheDefaultsTurnOn` fails when a
switch named `lean-...` is added to the defaults and not to `full.scm`, and the
tests of `internal/core`, `internal/frontend/...` and the rest that were
written against the previous loop run it (`test/fakes/home` sets
`NODWYN_PROFILE=full` for them); origin/main's own test files pass unchanged
against it.

**Tools that name the occurrence, show the closest text and keep long output
(lean profile alone).** On 484 shell-only SWE-bench sessions (Muse, a small
cheap model; `shell-exec` and `fs-edit`) the tools' answers cost the model
calls it did not need to make. `prim.Options.Lean` carries the changes, so a
full session's tools, schemas and answers are origin/main's, and `lean-prompt`
carries the one in the shell habit:

- *`fs-edit` names no tool.* Of 73 `fs-edit` errors in the two-tool arms, 24
  were "old text not found" whose generic variant ended "read the region again
  with fs-read" (and one more "read them with fs-read and copy them exactly"),
  though `fs-read` is not offered; the model switched to `sed -n` and
  recovered in 3.9 calls on average (1.6 in the six-tool arm). A miss now
  shows the closest text: the file line that shares most words with old's first
  line (at least two words and half of old's; of equal lines the one whose next
  lines resemble old's more, then the first), with the lines around it, five in
  all, numbered, bounded by `editHintBytes`; or says that no line resembles it
  and the text may be in another file.
- *`fs-edit` takes a `line`.* Another 23 errors were "old text occurs N times"
  (counts like 33 and 40) with no way to say which, though the model knew the
  line from `grep -n`. `line` (1-based; also in each edit of `edits`, where it
  counts the text the edits before it leave) chooses among the occurrences:
  the one that holds the line, else the nearest, the earlier on a tie. It is
  ignored for an old that occurs once and with `replace_all`, and it is the
  last parameter, so a positional `(fs-edit path "" "" #f edits)` keeps its
  meaning. The ambiguity error says "pass line to choose one, or replace_all".
- *`fs-edit` says where the replaced text also occurs.* django-16938 needs the
  identical change at `django/core/serializers/xml_serializer.py:158` as at
  `python.py`, and every failing session edited one and never saw the other.
  After a successful edit without `replace_all` of at least two non-blank lines
  or 40 non-space characters, the answer ends `the replaced text also occurs
  at: path:line, … (and N more); check whether the same change applies there`,
  at most three named: "occurs", not "edit", since the same text is not always
  the same fault. The search takes the edited file as it now is, its folder,
  then the repository's source files in path order, skipping hidden entries,
  `vendor`, `node_modules`, binary files and files over 1 MiB, at most 5,000
  files and 400 ms; when it runs out it says what it had found. The edit's own
  new text is left out (an insertion after an anchor holds the old text),
  tracked through the edits of a list. A file is named only if the model's own
  read of it would be allowed: the credential denylist and the guard,
  including a checker of the user's, for each file that matches (a nested
  `fs-read`, as `index-grep` does).
- *`shell-exec` shows a long stream as a view with a file behind it.* In the
  same sessions 84% of test runs were piped to `| tail -N` or `| head -N`, and
  in 30% of the runs that executed (362 of 1,208) the output then showed no
  verdict: Django's parallel runner prints only "Destroying test database ..."
  lines at the end, and the model ran the suite again with other flags, a call
  and minutes each. The lines were never lost to the tool, only to the pipe.
  The principle is a pager's: a long output is not poured into the model's
  context and is not trimmed by the model; the tool shows a bounded view and a
  handle to the rest. On the model's own call, a stream (stdout and stderr
  judged apart) of more than 8 KiB or 150 lines is returned as its first 20
  lines, a marker line where the others are, and its last 50: at most 6 KiB, a
  line cut at 300 characters, two views sharing the 6 KiB. The marker names the
  missing lines and the file: `[... lines 21-310 of 330 not shown; full output
  is saved at $TMPDIR/out/7.stdout: sed -n '21,120p' $TMPDIR/out/7.stdout shows
  a range, grep -n PATTERN $TMPDIR/out/7.stdout searches it ...]`.
- *The saved files.* `<scratch>/out/N.stdout` or `N.stderr`, N being 1, 2, 3
  ... for the session, shared by a call's two streams and taken only by a call
  that needs one (a counter under a mutex, files made with `O_EXCL`, so
  primitives that share a scratch directory do not overwrite one another). A
  file is made when the stream passes a threshold and holds the whole stream up
  to 8 MiB, written from a tee in front of the existing 256 KiB `capped`
  writer, which keeps its behaviour, so what it drops from the middle is on
  disk; a file that stopped says so, with its last line. Without a scratch
  directory there is no handle and nothing changes. `$TMPDIR/out/N.stdout` is
  read with `sed -n` or `grep -n` without a question: the scratch directory is
  not outside the repository to the argv trigger, as a test shows through the
  guard's checkers, with `$TMPDIR` written by the shell and as an argument of a
  command without a shell (which the host expands).
- *What the view leaves alone.* A command line that reads a file or limits its
  own output (`readLikeRE`: `cat`, `head`, `tail`, `nl`, `sed -n`, anywhere in
  the line) is not shown as a view: whoever asks for a range of a file wants
  that range, and a pipe through `tail` or `head` works exactly as it did;
  nothing of the model's command is rewritten or inspected. Only the model's
  own call gets a view: a program of the `scheme` tool (whose point is to split
  and filter the whole result), a strategy and the person's own call get the
  stream as before. A view is under what the window cuts (a result over
  `max_result_tokens`, 3,072 tokens by default, 9 KiB, is shown head and tail),
  as a test shows through the window's own `Build`, for the JSON the tool
  returns and for the plain text the shell habit gives it, which clips at 7,700
  characters.
- *The note reaches the model.* `shell.scm`'s `shell-plain` rendered a result
  from `status`, `stdout` and `stderr` alone, so the `note` field (exit 141,
  and a read that printed nothing, both added above) was in the log and the
  clients and never in what the model read: matplotlib-25960's `grep ... | head`
  and django-14534's `sed -n` past the end of the file had the notes in their
  results all along. With `lean-prompt` on, the note follows the output on a
  line of its own, `[note] ...`; with it off the text is what it was. No new
  switch, so `full.scm` is unchanged. The saved-file handle is in the stream's
  own text, not in the note, so it reaches the model whatever the rendering.

**Kept in both profiles**, because each is an improvement that origin/main's
tests do not contradict: the finish review that runs the original tests in
any language and reads a run's output in every ecosystem (the amendment of
2026-10-16), the plan language's matching of a run by what it runs, the
request-name filters, the expectation question's wider reading of test files,
`repo-relative?`, the guard's scratch-directory path, `fs-edit`'s and
`shell-exec`'s clearer errors and notes, and the working rule on the request's
title. The one test of origin/main they supersede is
`TestOriginalTestsAreRunForGoOnly`, whose Go-only behaviour that amendment
removed.

**Not built.** The lean tool list hides what a project adds: its own tools
(`register-tool` in `init.scm`, such as this repository's `validate`), MCP
tools and the delegate tool are not among `lean-tools`, and a project adds
them with `(define lean-tools (append lean-tools '("validate")))`; the
measurement had none. The profile is not a setting of the console. Lean was
measured on Python repositories, on one benchmark and one model family;
`auto` keeps it away from sessions a person is in until a measurement says it
does no harm there, and `profile = "lean"` is the way to try it.

Also not built, of the tools above: stripping a trailing `| tail -N` from the
model's command and returning the last N lines exactly, and a scanner for test
runners' verdict lines (unittest, pytest, go test, cargo, jest) to quote in a
note. The owner rejected rewriting the model's command; the view gives the same
lines and a handle without reading the command, and a test of a Django-style
run (the verdict, then twenty "Destroying test database" lines) finds its
verdict among the last 50 lines. `recall` still renders a shell result
without its note.

## Amendment 2026-10-17: the lean profile says only what it must

The owner's aim for the lean profile is to be as generic as possible, to lean
on the model as much as possible and to give it useful tools and nothing else:
every sentence the harness adds must have earned its place in the
measurements, and none of the behavioural nudges has (FINDINGS-0a9f700.md,
"Full 121-task passes"; tool-review-2-2026-10-17.md; the readers of the
afternoon of 2026-10-17). What the evidence says:

- Two tools, `shell-exec` and `fs-edit`, resolve as many tasks as six, at
  about 20% lower cost. The old shell2 build, which never sent the model a
  harness message, resolved 103 and 102; the builds with the read-streak
  reminder, the no-change reminder and the profile resolved 99 and 99 (A) and
  101 and 99 (B). The readers put every single loss down to variance, but the
  arms with nudges sit about three tasks lower over two replicates each.
- The read-streak reminder: of 47 firings read, about 8 weakly helped, 20 were
  neutral, 15 false alarms, and up to 4 possibly hurt; it tells the model to
  use `plan-write`, which the lean profile does not offer.
- Every lean request carried the plan block, "Plan for the current task (work
  through it, and the harness marks a step done ... mark one with plan-step ...
  write the plan again)", over a one-line plan; `plan-step` and `plan-write`
  are not offered.
- The environment hint was added in 99% of sessions and plausibly mattered in
  2 or 3 of 20, about half a call per task; Pi has none.
- Of the prompt's instructions, "act rather than announce" led to a
  placeholder edit before any reading, "nothing unrelated" was never cited,
  "finish with a short report: how you checked it" was followed but invites
  overclaiming, and "check with the project's own tests" is a useful detector.
  Prompt sentences change behaviour strongly (`git log -S` from 3% of sessions
  to 61%) and the score not at all, at 13% more cost.
- `fs-edit`: the recurring miss is a literal backslash-n in `old` (5 sessions,
  3 or 4 calls each, `fs-edit` given up for a Python script); errors named
  `fs-read`; the note of other copies was right once in 8; the closest text
  showed the right lines 3 times of 4; `line` was used 4 times in 242 sessions.
- A write to `/tmp/x` was refused in 10 to 22% of sessions, 2.2 calls each;
  the model checked `$TMPDIR` on its next call 15 times of 19 whatever the
  refusal said, and read it as a sandbox's wall.
- `shell-exec`: 80% of test runs are piped through `tail` or `head`; the view
  with a saved file fires rarely and is never read back, but costs nothing.

**Decision**, for the lean profile alone; `full.scm` turns every new switch
off, so a full session is origin/main's, as origin/main's own test files show
when laid over the branch and run with `NODWYN_PROFILE=full`:

1. *The system prompt* (`lean-system-prompt`) is about 90 tokens: who and
   where, that nobody approves or answers the calls, one phrase per tool of
   `lean-tools` (so a project that adds `fs-read` is told of it), paths and
   throwaway files (`$TMPDIR` or `/tmp`), "Do what the request asks, then
   check it by running something that exercises the change", and "When you
   are done, say what you changed, what you ran and what it showed, and
   anything you could not check". The sentences on `outline`, `tests-for`,
   `scheme` and the code-index commands stay, said only when those are
   offered.
2. *No plan is shown* (`lean-plan-hidden`). `gate.plan` gains `hidden`: the
   one-line plan is recorded, so the core still finds a plan before anything
   irreversible and the person sees it, and the window shows the model none,
   where it is recorded, carried to a run's later turn and brought back after
   a compaction. The run's turn prompt says nothing of the plan's steps.
3. *Harness messages*: no reading reminder (`lean-no-read-reminder`), no
   environment hint (`lean-no-envhint`); the no-change reminder is kept, once,
   in general words: "No file has changed. If the request needs a change, make
   it; if it does not, say what you ran that shows the code already does what
   is asked." (`finish-no-change-lean-message`, under `lean-quiet`). The
   empty-reply message is unchanged.
4. *`shell-exec`* describes itself in a few generic sentences: the program and
   its arguments, `bash -c` for shell syntax, status, stdout and stderr, a long
   stream shown as its first and last lines with the whole in a file whose path
   the marker gives, the time limit. The python -c advice and the sentence on
   `tail` and `head` go; the view, the saved files and the visible notes stay.
5. *`/tmp`* is a scratch directory too for a lean session nobody approves. The
   core gives such a session `prim.Options.Temp`, `/tmp` with its links
   resolved (`guard.TempDir`, `/private/tmp` on macOS), and the repository pin
   and the argv trigger allow a path in it as they allow the scratch
   directory: written, read and run. The change is narrow. It is made only for
   the lean profile and only when nobody approves the calls (a person still
   decides about `/tmp` in any other session); a path is judged with its links
   resolved and without a parent component, so a link out of `/tmp` or
   `/tmp/../etc` is outside; another session's scratch directory under `/tmp`
   (`nodwyn-scratch-*`) is outside; a file with a credential's name (the
   denylist's globs) is outside; nothing else outside the repository is
   allowed, and the repository's protected paths are judged as before, by
   checkers this does not touch. Where `Temp` is set nobody could approve a
   path outside, so the argv trigger refuses one outright instead of asking,
   and the refusal names it and says nothing ran ("the command line names
   /etc/hosts, outside the repository, $TMPDIR and /tmp, so it was refused and
   nothing of it ran"); the pin's says the call was refused and nothing was
   done. The files never reach the repository's diff. The guard's tests cover
   the allowed and the refused cases, and the exfiltration suite passes.
6. *`fs-edit`*: its schema is path, old, new, replace_all and edits again, in
   both profiles (`line` goes, with its code); no error names a tool; an `old`
   that matches nowhere as written but holds `\n`, `\t` or `\"` sequences, and
   matches exactly once with one level of escaping undone, is applied so, with
   `new` unescaped the same way, and the answer says "old held literal \n
   sequences; they were matched as real newlines"; the closest text is shown
   only at a resemblance of 0.5 or more (the cases the tests show score 0.57
   and up; a line that shares two of old's words among four others, 0.44, is
   not), otherwise the error says there is no close match; the note of other
   copies is given only for an `old` of two lines or 40 non-space characters
   that is not all import lines or decorators, looks only in the files git
   tracks, outside `build`, `dist`, `vendor` and `node_modules`, and says
   nothing when it finds more than three other places, runs out of files or
   time, or the repository is no git work tree.

**Not built here**: the run that measures it. The plan is SWE-bench Verified's
121 tasks, Muse at low effort, through the same bridge: the revamp four
replicates, and the old shell2 build two more so that both have four (the
difference's noise is about 1.2 tasks); measured are the tasks resolved, cost
and calls per task, and the mechanisms (scratch refusals, `fs-edit` misses and
their recovery, escape repairs, notes of other copies, test runs piped through
`tail`, no-change reminders, empty patches); then Qwen on the nine
commit-bench tasks after the shell2 build's nine. `recall` still renders a
shell result without its note.

## Amendment 2026-10-17: the lean profile after its first commit-bench session

Commit-bench task 6e18b7ba is nodwyn's own repository at an older commit,
trusted, run with Muse under the lean profile on 2026-10-17. The model made
the change in 4 minutes and then spent 50 on `validate`, which the
repository's AGENTS.md asks for. Three things stood in its way:

- The repository registers its own tools in `.nodwyn/init.scm` (`validate`,
  which runs `make validate` through `shell-start` and reads it with
  `shell-read` until it exits) and the `dev/*.scm` files it loads (`go-test`,
  `go-build`, `go-vet`, `go-fmt-check`, `run-make`, `git-log`, `grep-repo`),
  and the lean profile offered none of them: all 104 requests offered
  `shell-exec` and `fs-edit` alone. Under the full profile (dsl23, dsl24) the
  model called `validate` 5 to 8 times and finished in 8 to 13 minutes.
- `go run ./scripts/validate 2>&1 | tail -n 60` and `go test -count=1
  ./internal/prim/ ./cmd/nodwyn/ 2>&1 | tail -n 10` each came back
  `{"status":"timed out after 10m0s","stdout":"","stderr":""}`: `shell-exec`
  killed them at its ten minutes, and `tail` prints at its end, so nothing
  came back for 20 minutes. The model then started the check itself with
  `setsid nohup bash -c '...; echo EXIT:$? >> /tmp/vd.done' &` and polled it
  with `sleep 115; ls /tmp/vd.done || echo RUNNING; tail /tmp/vd.log` ten
  times.
- The guard refused `sleep 110; ls /tmp/vd3.done 2>/dev/null || echo RUNNING;
  D=$(ls -td /tmp/nodwyn-scratch-*/nodwyn-validate-* 2>/dev/null | head -n1);
  ...` as naming a path "outside the repository, $TMPDIR and /tmp", though the
  session's `$TMPDIR` was `/tmp/nodwyn-scratch-20261018T030629Z-60251800-878901457`.
  The pattern was judged as written: its first component, `nodwyn-scratch-*`,
  read as the name of another session's scratch directory, which `/tmp`'s
  allowance excludes. `ls -td /tmp/nodwyn-validate-*` had been allowed.

**Decision:**

1. *`shell-exec` leaves a lean session's command running at its time limit*
   instead of killing it and returning nothing. The model's own call (not a
   program's of the `scheme` tool or a strategy's, which compute with the
   result) is run by `runHolding`: at the limit the command is kept in the
   session's `prim.Procs`, both streams go on into the saved-output files the
   view uses, `$TMPDIR/out/N.stdout` and `N.stderr` under the call's number
   (made then if the call had none: a command piped into `tail`, which is
   never shown as a view, keeps up to 8 MiB of each stream for this), and
   when it ends whatever it left in its process group is stopped, as for
   every command, the files are closed and its exit status is written, whole
   and renamed into place, to `N.exit`. The result is `status` "still running
   after 10m0s", the view of what it printed by then (empty under `tail`),
   and a note: "the command was not stopped and is still running: its output
   goes on into $TMPDIR/out/N.stdout and $TMPDIR/out/N.stderr, and its exit
   status is written to $TMPDIR/out/N.exit when it ends (the session stops it
   if it is still running when the session ends)". `Procs.Close`, which the
   session calls when it closes (a daemon closes its sessions when it is
   stopped), stops each such command's process group and waits for its files
   before the scratch directory is removed; a command whose session is
   already closing, or whose file cannot be made, is stopped at the limit as
   before, and an abort stops it at once. The lean description's last
   sentence says so: "A command still running at the time limit is left
   running, and the result names the files its output and exit status go
   to." The full profile kills at the limit as it did, in the same code.
   Not built: a bound on how many commands a session may leave running; each
   costs the model ten minutes of waiting first.
2. *A glob pattern in a command line is judged by where it can match*, in
   both profiles, since the refusal was a false one. A word with `*`, `?` or
   `[` in a path component reaches its leading directory (the path up to the
   first such component, cleaned) and every path it matches when the call is
   judged (`filepath.Glob`, a relative word's in the repository root); each
   is judged as that path written out would be, its links resolved as a
   plain path's are, and the word is inside only when all of them are inside
   the repository, the scratch directory, or `/tmp` where that allowance
   applies (`guard.ArgvTrigger`, `globPlaces`). Nothing else is loosened. A
   match through a link in `/tmp` or in the repository that points outside is
   outside, which a pattern used to hide (`cat /tmp/l*/hosts` was judged by
   `/tmp` alone); another session's scratch directory is outside when a
   pattern matches it; a pattern in `/tmp` named as a credential file
   (`/tmp/*.pem`) is outside, as the name is; a word with a parent component
   is judged as written, as every path with one is (`/tmp/../etc/*` is
   outside either way, and cleaning `src/../*` to the root would hide a link
   that `src` is); an option (`-*`) and a word Go cannot read as a pattern are
   judged as before. Every path of a word is judged, a flag's value after `=`
   included: the trigger used to judge only the first one outside the
   repository, and let the rest go when that one was in `/tmp`. The
   commit-bench line is the test, run in an unattended lean session whose
   `$TMPDIR` is under `/tmp`; it is refused on the unchanged code.

**Tried and not kept**: offering the tools a trusted project registers
itself, the first of the three. It was built on 2026-10-17: the lean offer was
`lean-tools` and then every tool `register-tool` recorded while the project's
init file ran, and a session of nodwyn's repository was offered `validate`,
`go-test` and the rest. In the commit-bench run its sessions were shorter, 39
model calls a task against 43, and their patches less complete: 84% had tests
against 91%, 53% changed the docs against 59%, and a blind review scored them
lower. Several sessions ended right after the repository's `validate` said
PASS. The lean profile offers `shell-exec` and `fs-edit` alone, as before (a
project that wants more offered names it in `lean-tools`), and the model runs
the repository's checks through `shell-exec`, which the first decision keeps
from losing a long check's output at the time limit.

A lean session's end on a relay's 404 the same evening (21:49, the only
model request that failed among thousands) is ADR 41's amendment of
2026-10-17: such a request is now sent again, in both profiles.

**Measured so far** (2026-10-17, Muse, effort low): task 6e18b7ba was run
again. The landed lean build's first attempt had run under the evening's
heaviest load (ten commit-bench attempts, twelve SWE-bench containers and the
local model's queue on one host); two more attempts under ordinary load
resolved it in 6 and 8 minutes. The build with these two changes and the
project tools resolved it 3 times of 3 in 5 to 9 minutes, and no command of
its 34 sessions reached shell-exec's limit. This build, without the project
tools, is not measured yet; both changes apply to the 121 SWE-bench tasks as
well, where a check piped into `tail` past ten minutes and a pattern in
`/tmp` can show them.

## Amendment 2026-10-18: the lean prompt carries the repository's AGENTS.md

ADR 36 says a repository's root `AGENTS.md` rides in the system prompt, and
the manual said both system prompts carry it. Since the lean ladder's third
rung (2026-10-16) the lean prompt is a third one, `lean-system-prompt`, and it
never called `(project-instructions-section)`: an unattended lean session, the
default for `nodwyn run` with `--yolo`, `--unattended` or `--judge`, had none
of the repository's instructions in its prompt, and learned of them only if
it read the file itself. Nothing decided that; the amendments of 2026-10-17
do not mention the file.

**Decision:** the lean prompt ends with `(project-instructions-section)`, the
same heading and text as the full prompts, after its last fixed sentence, so
the fixed part stays the provider's cached prefix and a repository without
the file gets the lean prompt byte for byte as before. The section's first
newline is dropped, since the lean prompt's lines already end in one. The
32 KiB cap, the redaction and the reading rules are the primitive's and
apply as in the full prompts; a project that redefines the section to `""`
gets the fixed lean prompt. The full profile's prompts are unchanged.

Why: the bar of 2026-10-17, that every sentence must earn its place in the
measurements, is for what the harness adds. `AGENTS.md` is the repository
speaking, its maintainers' rules for how a change there is built, tested
and validated, which ADR 36 put in every prompt with no setting to turn it
off; a coding agent that reads `AGENTS.md` is what the convention expects.
nodwyn's own file adds 10,340 characters, about 2,600 tokens, to a lean
prompt of about 650 characters; a repository without the file pays nothing.
Its effect is being measured on commit-bench, where a task such as 6e18b7ba
is nodwyn's own repository at an older commit.

**Not carried**: the project notes (`(project-notes-section)`). They are the
agent's own notes from earlier sessions, which the full prompts call leads to
check, not the maintainers' word, and whether they help a lean session has
not been measured; a project that wants them appends the section in its
`init.scm`. Also unchanged: the cap's last line says to read the rest of a
long file with `fs-read`, which the lean profile does not offer; the model
reads it through `shell-exec`.

## Amendment 2026-10-18: this repository's completeness rules are its own (experiment E)

On commit-bench, this repository at each task's parent run trusted and
unattended, the lean profile's patches leave out more of what AGENTS.md asks
a change here to carry than the full loop's: of 96 lean patches 88% had a
test and 61% a document, against 100% and 78% under the full loop, with
correctness and the blind reviews' overall scores about equal. The lean loop
says nothing as a turn ends (`lean-quiet`), and was measured on SWE-bench,
whose repositories ask for none of it. A test, a document, a decision record
and a regenerated source catalogue are this repository's rules, not every
repository's: they belong in its own customisation, which a session loads
once the person trusts the revision (ADR 45), not in the loop nodwyn ships.
The commits the tasks were made from carry them as follows: all 73 a test,
63 a document, 50 a change under `docs/adr`, and 18 of the 19 whose parent
has a source catalogue regenerated it.

What was found first, of the validate reminder `.nodwyn/init.scm` already
had: it runs in a lean session too, since the file redefines
`default-agent-turn`, which `agent-turn` calls in either profile, but the
lean profile does not offer `validate` (`lean-tools` is `shell-exec` and
`fs-edit`, and the file adds nothing to it), so the reminder names a tool the
model cannot call. The model runs `go run ./scripts/validate` through
`shell-exec` instead. The evidence loop counts such a run as validate's
(`validated-run`), but the reminder's own continuation (`validate-continue`,
the chat loop) does not, so the second reminder follows a validate run made
after the first whatever it found: in the lean run
cb-landed-d635ad3-or-muse13c-el-trusted-home-r3 the reminder came twice in
each of 3 of its 9 sessions, and on 48f90ea8 the second came after a validate
run with no edit since ("No code changes this turn; re-ran validate").
Neither is changed here.

**Decision.** `.nodwyn/init.scm` gains a finish check of its own, in both
profiles, after the validate reminder and last before a turn ends. It reads
the change with `git-diff` against the commit the session's worktree started
from, else the one HEAD named when the turn began, and adds the files git
does not track. When the turn changed code (a `.go` file that is not a test,
or a `.scm` file outside `test/`, in the change now and not, or not so, when
the turn began) and the repository has an AGENTS.md, which the message cites,
the turn is told in one message each of these the change lacks:

- a test: no `_test.go` file and nothing under `test/exfil/` or
  `test/browser/checks/`, as scripts/validate counts tests;
- a document: nothing under `docs/` but the generated catalogue and wire
  schema, and no Markdown file;
- a decision record: nothing under `docs/adr/`;
- the regenerated source catalogue, where `docs/book/source/catalogue.tex`
  exists: it is not in the change, and a Go or Scheme file was added or
  deleted, or the lines added and removed differ for the Go files, the Go
  test files or the Scheme files, which is what `generate.py` counts.

The message ends "Add what the change needs, or say in your answer why it
needs none of it.": not every change needs each, and a third of the commits
above changed nothing under `docs/adr`. The turn then goes on as the chat
loop does. It is said at most once a session, and not while the turn is
stopping (`turn-stopping`). A diff cut at the tools' output limit is not
judged; a directory git does not track is one entry, its contents unread
(one of the 73 commits adds a directory), and an untracked file the turn
edited again is not seen, as with the validate reminder.
`scripts/validate/repo_finish_test.go` runs the repository's own init.scm in
a full chat and a lean run: what is missing is said, and only that; nothing
is said of a whole change or where there is no AGENTS.md; and it is said once
in a run of two turns.

The section uses nothing else in init.scm, only the loop the binary carries,
so the measurement can lay the same text over the older revisions the tasks
start from, whose init.scm lacks it: commit-bench's run.py `--extra-init`
copies the file into the attempt's `.nodwyn/`, loads it from the end of the
parent's init.scm (or an init.scm of its own where the parent has none) and
keeps both out of git's view, so neither the model's `git diff` nor the patch
holds them.

**To be measured** (experiment E): the lean profile with the check against
the lean profile without it, the same build, tasks and model: how often the
patches carry a test, a document, an ADR change and the regenerated
catalogue, the reviews' scores, cost and calls per task, how often the check
is said, and what the model does after it.
