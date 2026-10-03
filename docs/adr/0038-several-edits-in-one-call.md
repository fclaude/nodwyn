# ADR 0038: fs-edit takes several edits to one file in one call

- Status: accepted
- Date: 2026-10-03

## Context
`fs-edit` replaced one piece of text per call. Measured on 2026-10-03 from
`llm.resp` tool calls (agent tools have no `tool.call` event):

**meta/muse-spark-1.3**, the five local sessions with edits (2026-10-02 and
03; one was still running, so the counts are a little above those taken
earlier that day):

- 429 `fs-edit` calls on 97 files. 332 of them (77%) were a second or later
  edit to a file the session had already edited.
- The model never put two `fs-edit` calls in one reply, to one file or to
  two, although 22% of its replies across all its sessions carry two to six
  calls and the loop runs a reply's calls in order.
- 128 of 1,704 replies (7.5%) had one call, an `fs-edit` of the file the
  reply before had just edited alone. Each such request sent a median of
  117,000 prompt tokens (p10 44,000, p90 198,000), 99% from the cache:
  15.5 million in all. That is the most batching could save on this model.
- 23 of 429 calls failed (5.4%): 20 because `old` was not found, 2 because
  it was not unique, 1 cancelled.

**qwen3.8-27b** on the SWE-bench runs on workshop (1,158 sessions):

| model tag | replies | `fs-edit` calls | later edit of a file | replies with 2+ `fs-edit` (same file) | lone re-edit replies | failed |
|---|---|---|---|---|---|---|
| 96k (all runs) | 38,000 | 4,455 | 63% | 526 (426) | 644 (1.7%) | 4.1% |
| 96k, pil only (ADR 32) | 6,134 | 763 | 58% | 85 (57) | 109 (1.8%) | 3.4% |
| 96k-t030 | 3,740 | 525 | 58% | 61 (46) | 58 (1.6%) | 2.3% |
| 96k-t060 | 3,790 | 552 | 63% | 67 (53) | 74 (2.0%) | 3.3% |
| 96k-t120 | 4,985 | 739 | 67% | 94 (72) | 100 (2.0%) | 4.2% |
| q5ks-56k | 13,425 | 1,741 | 63% | 237 (193) | 226 (1.7%) | 3.8% |

Qwen already puts same-file edits in one reply now and then, and re-edits
alone in under 2% of its replies, against 7.5% for muse-spark. Its requests
are small (median 17,000 to 23,000 prompt tokens). The local Ollama reports
no cached tokens. On both models nearly every failure is an `old` that does
not match.

## Decision
`fs-edit` gains an optional `edits` list. It stays one tool, for one file.

- **The JSON form** is `{"path": P, "edits": [{"old": O, "new": N,
  "replace_all": false}, ...]}`, or the old form `{"path", "old", "new",
  "replace_all"}`. A call gives exactly one of the two: both, or neither, is
  an error that says so ("give either old and new (with replace_all) or
  edits, not both; replace_all goes inside each edit"). An empty list, an
  element that is not an object, a missing `old` or `new`, a field given
  twice or an unknown field is an error naming the edit.
- **Order and atomicity.** The edits apply in order, each to the text the
  one before left, so a later edit may match what an earlier one inserted.
  Nothing is written unless every edit applies. A failure names the edit,
  1-based ("edit 2 of 3 failed, so nothing was written"), and keeps
  `applyEdit`'s message, including the whitespace hint that quotes the
  text it was matched against. `replace_all` belongs to each edit.
- **One confirm, one diff, one version, one write.** The preview is one
  unified diff from the file to the text all the edits leave, from the same
  `applyEdit` run over the edits in turn. The handler keeps one previous
  version and writes once.
- **The positional form.** `old` and `new` become optional, so a call that
  names only `path` and `edits` passes ADR 29's rule: the skipped options
  get their empty values, and Scheme sees `(fs-edit path "" "" #f edits)`.
  The old form `(fs-edit path old new [replace_all])` is unchanged. The
  elements are alists with string keys, as `json-parse` makes a JSON object
  (`FromJSON` already turns an object inside a list into one). A symbol key
  is an error, not ignored, so what is written is what the guard judged.
- **Item schemas.** A `scheme.Param` may describe its elements' fields
  (`Items`, Go-only). The tool schema then gives the list an `items` object
  schema, so the model sees `old`, `new` and `replace_all` with their
  descriptions, `old` and `new` required. `Prim.Schema` had emitted
  `{"type": "array"}` with nothing inside. The tool list stays in the same
  order and is the same in every request (ADR 9). It changes once, here:
  `fs-edit` now requires only `path`.
- **The guard reads item roles.** A field in `Items` may carry a role, read
  as a parameter's is (`Prim.EachRole`). Each edit's `new` is a `payload`,
  as `new` is. So the secret scan (`TextSecret`) and the taint check
  (`taintedRoles`) read every replacement, and not the `old` text an edit
  replaces, exactly as they read a single edit. A field also carries the
  taint of the list and of the element around it, since a list parsed from
  tool output carries its taint on the outside. `deepTaint` already walked
  lists. Without this, a credential in a later edit's `new` was written
  without the security confirm a single edit gets: the exfiltration
  scenario 017 fails with the old secret scan ("got allow").
- **Unchanged.** There is still one path, so the protected-path check, the
  pin, the denylist, standing write approvals and `WritePath` are as they
  were. `WritePath` still finds a payload role in `new`. The loop's
  strategies read only an edit's `path` (`edit-call?`, the check loop,
  hygiene, the optimizer), so they count a call with edits as one edit of
  that file. The console shows the call's path and its JSON arguments, and
  the approval shows the guard's diff. The terminal clients show the
  arguments in the positional form and then the diff.
- **The tool's description** says when to use the list: "Several changes to
  one file: one call with edits". The working rules (`prompts.scm`) are
  unchanged: nothing measured yet shows the model needs a nudge (below).

## Rejected
- **A second editing tool** (`multi-edit`), or **a choice per model** of
  editing tool. A model that ignores `edits` uses `fs-edit` as before. A
  choice per model is justified only if a benchmark shows a model family
  does clearly better with its native format, such as `apply_patch`.
- **A unified-diff "patch" tool.** The guard judges one path per write:
  protected paths, standing write approvals, the diff preview and the
  versions store. A patch over several files would need one confirm
  covering several paths, partial application, deletes and renames. Diff
  hunks also break when the file drifts from their context, which exact
  `old`/`new` matching does not.
- **Several files per call**, for the same reasons.
- **The whole `edits` list as a payload**, which would have needed no item
  roles. The secret scan would then read the `old` text too: an edit
  replacing a line that holds a credential-shaped string (a test fixture,
  as nodwyn's own repository has) would become a security confirm, which
  no standing approval waives and an unattended run refuses.
- **`(fs-edit path edits)` as the Scheme form.** The guard reads roles by
  position, so a list in `old`'s place, which has no role, would carry its
  replacements past the secret scan.

## Consequences
- With `old` and `new` optional, ADR 29's rule turns a call that gives
  `old` and `replace_all` but no `new` into a deletion of every occurrence,
  where it used to be the error "missing argument new". Without
  `replace_all`, a missing `new` is still an error ("new is missing"). Of
  8,400 `fs-edit` calls in the logs above, none left out `new`.
- A failing edit fails the whole call, so the model sends the edits that
  would have applied again with the fixed one.
- The terminal clients' header shows the edits form with its placeholders,
  as `fs-edit ("Makefile" "" "" #f ((("old" . "…") ("new" . "…")) …))`; the
  diff below it is the call's.
- Tests: `internal/prim/fs_edit_list_test.go` (order, all or nothing,
  `replace_all` per edit, one form only, one version, the schema, the
  guard); `TestTaintInAListParametersItems` (guard);
  `TestFsEditWithEditsAsksOnceWithOneDiff` (a session with the fake Ollama);
  exfiltration scenarios 016 and 017; the browser check `approve` approves
  a two-edit call to a protected path in the demo and checks the card's
  diff and the file.

## Measuring it (for the user to run)
Neither run below has been started; running a benchmark is the user's
call.

- **A/B on SWE-bench.** The setup of ADR 32's pil run
  (`nibb-1fd0696-d-pi121-pil`): qwen3.8-27b-96k at Q4, the same 121
  instances, one attempt each, with the commit before this change (bc6c27e)
  and the commit that makes it. Run each side twice if there is time: pil's
  86 against Pi's 87 shows how close a difference can be to noise.
- **Measure, per instance and in total:**
  - the resolved rate, from the eval's `resolved_ids`;
  - model requests (`llm.resp`, all and the main context's);
  - prompt tokens (`usage.prompt_tokens`) and prompt-evaluation time;
  - `fs-edit` failures: rate and kind, the new "edit N of M" and contract
    errors included, and how often a failed call's other edits were sent
    again;
  - how often the model used `edits`: calls with it among all `fs-edit`
    calls, edits per such call, and how many replies still carry two
    `fs-edit` calls to one file;
  - lone re-edit replies, defined as above: a reply whose only call is an
    `fs-edit` of a file the previous reply in the same context edited.
- **muse-spark costs nothing extra to measure**: its interactive sessions
  log the same events. Compare the lone re-edit share (7.5%) and the use of
  `edits` in the sessions after this change with the five above.
- **If a model rarely uses `edits`**, this change saves that model little.
  muse-spark never put two edits in one reply, so it may ignore `edits`
  too. The next step would then be a working rule in `prompts.scm`, with an
  ADR 32 amendment naming this measurement, and measured the same way. A
  family that does clearly better with its native patch format is the case
  for a per-model choice, above.
