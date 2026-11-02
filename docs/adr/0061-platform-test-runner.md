# ADR 0061: Iteration omits race instrumentation; acceptance retains it

- Status: accepted; implemented 2026-11-02
- Date: 2026-11-02
- Amends: ADR 13 and ADR 37

## Context

The full race suite took approximately 55 seconds on a Linux desktop and
247 seconds on an Apple silicon Mac in the 2026-11-02 investigation. The
Mac spent extensive kernel CPU time on copy-on-write faults when Go launched
subprocesses from a large heap. A fixed-buffer launch control and smaller
independent test heaps demonstrated the mechanism. Several long packages also
ran most top-level tests serially. Splitting them reduced the complete race
suite to roughly 19 seconds on that desktop and 42 seconds on the Mac.
Those measurements used earlier source revisions; they are not timing promises
for later changes.

The owner requested automatic platform settings, including measured macOS
weights, then requested a separate faster iteration command without race
instrumentation, with race checks retained before pushes. Changing only the
Makefile's test target would silently weaken the validator: ADR 37 invokes
acceptance's parts directly. Both entry points must retain the race gate.

## Decision

- `make test` runs the complete non-race suite. `make test-race` runs the
  complete race suite and first runs the runner's own fixture tests. Iteration
  avoids that additional harness-check overhead.
- `make accept` depends on lint, test-race and exfil. The validator's parallel
  acceptance implementation calls test-race before exfil; a failed race run
  stops that path. Promotion and CI keep using acceptance. Before a push,
  agents run validation after the last edit; no Git hook is silently installed.
- A standard-library Python runner splits the slow packages into independent
  processes and uses checked-in OS/architecture timing hints. Repository
  contributors maintain those hints under the same trust as tests/Makefile;
  the files only affect scheduling and cannot select executable commands.
- Current Go test inventories, including examples and fuzz seeds, determine
  coverage. Weights never select or remove tests. Missing/duplicate outcomes,
  test failures and incomplete package results fail the command.
- Explicit Go filters/options use the ordinary Go runner. An uncached single
  pass remains optimised. Unsharded targets remain available because splitting
  processes changes global-state and test co-execution interactions.
- Concurrent hot processes are bounded by the host CPU count (the
  logical CPUs, minimum 2, maximum 32); smaller heaps must not mean flooding
  a laptop with runnable processes.
- Process deadlines, identity-checked cleanup and retained failure logs are
  part of the runner. The default GC remains in use; no host policy changes.

## Consequences

Iteration no longer pays race-detector overhead, while finishing/promotion
retain that check. Python 3.9+ becomes a prerequisite for the normal test
command. Timing data can age without losing tests; its provenance remains in
the JSON. Workers use more independent processes, which can expose scheduling
assumptions in existing tests. An observed failure is reported and investigated,
not excluded to obtain a timing result.

See [testing](../testing.md) for commands, overrides and platform selection.
