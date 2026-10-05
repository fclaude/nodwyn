# ADR 0019: Jev sees content, never secrets

- Status: accepted (amends SPEC "Jev — where it is allowed" and plan P5)
- Date: 2026-09-21

## Context
The spec said Jev "judges why, never what" and "never sees content". That
was over-rotated from the real constraint. Seam A asks "is this plan good
enough for the user's ask?" — a question that has no answer without the
ask and the plan, both of which are content. The concern behind the rule
was retention: Jev is an external API with no non-retention guarantee, so a
secret value that reaches it is a secret leaked. That concern is about
*secret values*, not about content in general.

The spec already resolved one category error of this shape (a taint on a
value cannot protect a text window). This is the same correction applied
to the judge.

## Decision

1. **Jev is an egress edge.** A call to Jev is model-context text leaving
   the machine, and it is guarded the way every other egress is: the Jev
   credential is a bound data-plane credential (E3), and the payload passes
   through the same redaction controller as the model context (E2/E4:
   L1, later L3, `[REDACTED class]`). No special sanitiser; the composition
   of two existing mechanisms.

2. **Secret values never reach Jev.** Unchanged: invariant 2 holds, and
   redaction is what enforces it. If redaction finds a known-shape secret
   the placeholder is sent; if L3 later flags a class-unknown residual above
   threshold, the call is confirmed or dropped, never sent raw.

3. **What Jev may see is a user policy, not an invariant.** `jev.content`
   in `config.toml`:
   - `none` — Jev disabled;
   - `plan` (default) — the user's ask and the draft plan text;
   - `plan+summaries` — plus the model's own summaries of tool results it
     cites in the plan (never raw results);
   - `full` — plus raw tool results and file contents referenced by the
     plan; opt-in per repository in `.nodwyn/allow.toml` (`jev = "full"`),
     because it is the level at which proprietary code is retained
     elsewhere.

4. **Every call is logged** as `jev.req` (new kind): content level, byte
   count, a hash of the payload, the redaction events it triggered, and the
   score that came back. The user can see exactly what left.

5. **Unchanged:** Seam C remains value-free by construction (class,
   destination, scope); Jev never sees the harness's own control-plane
   credential and is never audited for it; Jev never controls flow.

## Consequences
- Seam A becomes useful: it judges plans against asks, with real text.
- The P5 "payload sanitiser" is deleted from the plan; P5 depends on P7's
  redaction controller for the send path (or ships L1-only redaction for
  Jev first, which is the same function).
- The only content leaving the machine in v0 is what goes to Jev (the
  control plane is local). The knob and the log make that visible. *(No longer so once a remote model is configured: ADR 27.)*
- The spec's "why-not-what" line is restated as: Jev judges intent and
  plans over redacted content; the local stack judges provenance and
  secret shape, and is the only thing that ever sees a value.

## Amendment — 2026-10-01: judged approval

When the judge answers a run's confirms (ADR 18, "Judged approval"), what it
is shown follows this ADR's levels. At `plan` (the default): the task, the
turn's plan, and the call's shape: the tool, the guard's reasons, and its
target, the path a write names or the command line a program runs. At
`full`: also what the call would do (the write's preview or content, the
call's arguments). Everything is redacted (L1) before it leaves, and
logged as `jev.req` with the harness's origin.

## Amendment 2026-10-05: what the judge is told about a call

Session `20261005T054223Z-36b27caa`, a chat with `commands = "judge"`, sent
four runs of the project's own tests, the steps its plan named, to the
person, at probabilities of allow of 0.44 to 0.63 against 0.8; the person
allowed each. The judge was told an agent worked "without a person watching"
when one was at hand and a refusal only asked them; every command came with
"shell-exec is irreversible", which every command is; it saw the plan as
first recorded, never which steps were done, so two identical asks minutes
apart sent byte-identical state; it saw `bash "$TMPDIR/check.sh" tests`,
not that the script was the session's own; and it never heard that it, or
the person, had answered the same command earlier in the turn.

- **The question says who is there.** A session a person can answer asks
  whether the call may go ahead without asking them, says a call the judge
  does not allow is put to them, and counts the project's own builds and
  tests, or a script the agent wrote for that, as ordinary work. A run's
  judge (`--judge`) is asked as before: nobody is watching, and a refusal
  is a no. Both keep the list of what a person would want to decide.
- **The default reason is left out.** "TOOL is irreversible", the guard's
  reason for every irreversible tool, is not sent; a more specific reason
  (a declared one, or another checker's) still is.
- **The plan comes with its marks.** Each step is shown done or skipped,
  with the model's note, the first unmarked one as next, the rest to do; a
  carried plan brings the marks its earlier turns made.
- **The same call's earlier answers this turn are shown**: the judge's,
  with its probability, and the person's, keyed by the tool and exactly
  what it acts on (the command line, a write's path). They are not a
  standing approval: the judge still decides, and the next turn starts
  with none.
- **A scratch script is named as the session's.** A command line that
  names a regular file in the session's scratch directory (ADR 28), as
  `$TMPDIR/…` or by its path, tells the judge that the file is there, its
  line count, and that only this session's tools and commands write there.
  Its text is a file's contents, so it is shown only at `full` (point 3),
  cut at 4 KB; at `plan` the judge learns whose it is, not what it says.
  Everything still goes through L1 redaction and `jev.req`.

## Amendment 2026-10-05: what needs a plan, a chat's earlier messages, and looking

Three chats on 2026-10-05, on the build before the amendment above, show
the judge was asked the wrong questions and shown too little.

Sessions `20261005T054223Z-36b27caa` and `20261005T060116Z-ef99f036` asked
"review all setting options against the documentation, what makes and
what doesn't make sense anymore?". Both times the judge put the need for a
plan at 0.82: its question asked whether the work took several steps or
touched more than one file, which reading every setting does. Every plan
has the shape of a change (ADR 32, amended 2026-10-03), so the plans added
`docs/settings-review.md`, tests that passed on the unchanged code and
edits to a dozen documents, and scored 0.71 and 0.75 against 0.7. The
person closed the first session after 17 minutes and asked again; the
second built the same change in 15. They had asked for an answer.

Session `20261005T060224Z-2f9eb922`'s second message, "folding, to reduce
visual polution for the user", answered the model's question about its
first ("that is still verbose, compacting should be possible there").
Shown alone, the judge put the need for a plan at 0.4 for a change to the
transcript's rendering, its styles, its browser checks and four documents,
and the command that looked for the tools it needed,
`command -v go node npm npx python3; …`, at 0.41.

Of the 19 commands the judge was asked about across the three, 15 went to
the person, who allowed every one they answered: among them `date -I`
(0.68), `git diff --check; git diff --numstat; pdfinfo …` (0.72) and a
script rewriting `$TMPDIR/check.sh` (0.65). Three times the person answered
by allowing every call for the rest of the session.

- **A plan is a plan of changes.** `plan-needed-question` asks whether
  carrying out the latest request means changing the repository in
  several steps, in more than one file, or irreversibly, and says that a
  question, a review, a comparison, an explanation or a proposal asks for
  an answer and needs no plan, however much reading it takes.
- **A chat's later message is read after the ones before it.** The plan
  gate's judge, its explorer, its planner and its score read the request
  after the person's last two messages before it (`plan-earlier-requests`,
  each cut at `plan-earlier-cap`, 2000 characters), and the question says a
  reply to the agent's question or a correction carries on the request
  before it. The judge of a command is shown the same two messages before
  the task. A run's turns each repeat the run's task and are read alone.
  The earlier messages are the person's asks, which `plan` already lets
  leave (point 3); the model's replies are not sent.
- **Looking is ordinary work.** The question a session a person answers
  puts to the judge allows a command that only looks or reports (listing,
  finding or reading files, versions, the date, git status, diff or log)
  whether or not the plan names it, and counts writing in the session's
  scratch directory with the builds, tests and scripts it already counted
  as ordinary work. A run's judge (`--judge`) is asked as before.

Not changed: the score's rubric. A low score for a plan made for a question
would only bring revisions, and the gate records the best plan whatever
its score; whether a request needs a plan is the noul's to say. None of it
was measured against the judge itself, which no test calls: the fake judge
shows what is sent, and the sessions above are the cases to replay.
