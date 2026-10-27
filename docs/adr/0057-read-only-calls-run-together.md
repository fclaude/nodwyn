# ADR 0057: a batch of read-only calls runs together

- Status: accepted; implemented 2026-10-27. Amends 18 (where the budget
  checks of a batch fall) and 38 (which said the loop runs a reply's calls
  in order).
- Date: 2026-10-27

## Context

A model often answers with several tool calls at once, and the loop ran them
one after another: each read, listing and search waited for the one before,
though none depends on it. Reading five files cost five file reads in a row,
and a search over a large tree held up everything beside it. A command, a
write or a call that follows another's effects must keep its order; reads
need not.

## Decision

1. **Which batches.** `run-tools` runs a batch concurrently when it has two
   or more calls, every one a tool in `readOnlyTools` (`fs-read`, `fs-list`,
   `fs-find`, `index-grep`, `outline`, `git-status`, `git-diff`, `git-head`),
   and no two of them the same call. Any other batch runs in order, as it
   did: one call, one write or command or `shell-read` or `index-search` in
   it, a tool from an MCP server (its read-only hint is the server's claim),
   a tool a project defines in Scheme, `scheme`. A strategy that passes its
   calls one at a time to `run-tools` (stop at the first failure; a reply
   with an agent tool in it) therefore keeps them in order. The set is by
   name: the primitive's declaration (`scheme.Meta`) has no read-only flag,
   and adding one is a change to Brote. It is short on purpose; a tool is
   added only when it reads and writes nothing, the index and the repository
   included.
2. **A repeated call is not run together.** The progress guard counts runs
   of the same call and stops the turn at the fourth, with the calls after it
   unrun. Calls already running cannot be un-run, so a batch with a call
   twice goes in order and meets the guard as before.
3. **Budget first, in order.** The tool-call budget is reserved for each call
   of the batch, in order, before any starts. A limit that falls in the
   middle runs the calls before it and answers the rest as not executed, as
   it did in order. The stop it raises would cancel the turn's context, and
   with it the calls that fit, before they begin: `budget.Turn.Hold` keeps
   the cancellation back until the batch has run. A checkpoint asks the
   person before anything starts.
4. **Recorded in order, settled in order.** The log holds every call, then the
   results in the order of the calls, whichever finished first. The progress
   guard observes the results in that order, once all have finished, and a
   warning it has goes with the result as before. The calls are applied with
   at most 8 at once.
5. **What a stop does to calls that ran.** A call that failed stops none of
   the others: they are independent. When the progress guard stops the turn
   at one call, the calls after it have already run, so each has its real
   result recorded; answering them "not executed" would be untrue. On
   cancellation each call in flight records an interruption, and a call that
   never began is answered not executed.
6. **Shared state.** The gate of each call charges the budget from its own
   goroutine, so `budget.Turn` takes a lock (held while a checkpoint waits
   for the person, so a limit crossed by two calls asks once). Everything
   else the calls reach must also be safe to call from several goroutines.
   In particular, Scheme checkers share mutable interpreter state and their
   evaluations are serialised across the session's registered predicates
   (amendment below). The log and repository index retain their own locks.
7. **Clients.** A batch's log is calls, guard decisions, then results, where a
   one-at-a-time run's is a call and its result in turn. The console and the
   terminal client pair a result with its own call by id, not with the last
   call; a guard decision goes to the first call of the tool it names that
   has none (the decisions of a batch carry no call id).

## Not built

- A flag for read-only on the primitive, so a tool declares it; it waits for
  a change to Brote.
- Running independent commands, or a read beside a write to another file:
  they need a dependency the loop cannot see.
- Several prompts being answered at once in the clients: a read that needs a
  confirmation (a path outside the folder) asks while its neighbours run, and
  a client that shows one prompt at a time shows them in turn.

## Consequences

- A reply of several reads takes as long as the slowest, not the sum.
- Time in the log is as real: a call's span runs from the batch's start to
  its result, and the statistics' tool time takes the union of the spans.
- A model that sends the same read twice in one reply gets the old behaviour.

## Amendment 2026-10-27: Scheme policy evaluations stay serial

The review found that concurrent reads could enter Scheme checker predicates
at once, although their captured and global bindings are mutable. The guard
now shares one evaluator lock across all its Scheme checkers. Nested calls
still skip re-entering predicates through the existing context marker; Go
checks and the reads themselves remain concurrent. No policy verdict is
relaxed. `TestConcurrentCallsSerialiseSchemePredicates` fails when two calls
overlap their policy evaluations. The session integration check now sets its
ten-call budget through `newRigCalls`, replacing a removed config field that
prevented `internal/core` tests from compiling.
