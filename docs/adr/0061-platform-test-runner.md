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

## Amendment 2026-11-02: Automatic private RAM scratch on macOS

The subsequent storage controls reduced the Mac's full non-race loop from
41.42 seconds on ordinary SSD scratch to 19.99/19.60 seconds on APFS RAM
scratch. HFS+ RAM was similarly fast; a shorter SSD path was not. Source,
assertions, GC and worker budgets were unchanged. The owner requested that
agents receive this environment automatically through the normal commands.

- Full sharded macOS commands allocate a per-invocation 2 GiB HFS+ RAM image
  when at least 6 GiB of free/reclaimable memory is available. HFS+ uses a
  simple whole-device lifecycle; both measured RAM filesystem types passed.
- Only fixtures, Go build scratch and compiled test binaries move. Retained
  logs/results/receipts and durable caches stay outside the image.
- A native attachment receipt, RAM-image metadata, current UID, whole virtual
  device/size and helper PID establish ownership before initialisation. A
  random volume label and helper identity distinguish concurrent invocations
  and reused device IDs. Repository contributors maintain this trusted runner
  with the same authority as the Makefile; timing data cannot name devices.
- Mount the image below a short mode-0700 host directory before writing test data.
  Newly formatted HFS+ images may ignore ownership; a protected ancestor on
  the host filesystem enforces privacy without privileged policy changes.
- Each native operation has a 30-second deadline and a recorded PID/exit.
  Image operations defer handled signals until ownership/cleanup is safe.
  Worker cleanup precedes detach; helper identity/exit and mount removal are
  checked before reporting success. Transient busy detach results get at most
  five identity-checked attempts without force. Cleanup failure fails the command.
- Low memory or unavailable setup falls back visibly to the disk runner only
  after known resources are removed. An uncertain attachment/identity is an
  error. A hard kill/crash cannot run cleanup; retained receipts support
  identity-checked operator recovery.
- `NODWYN_TEST_RAM=off` keeps disk scratch with the same sharding. Explicit
  Go options/filters and unsharded diagnostics retain ordinary Go execution
  without image setup overhead. Race acceptance is unchanged.
- Linux/WSL use existing temporary storage. X1 already has RAM-backed `/tmp`;
  its alternating RAM/SSD control showed only a 6% median difference.

This adds ephemeral OS resources, not a daemon or persistent workstation policy.
The measured integration includes allocation and cleanup time; it does not
claim the earlier pre-mounted RAM timings as automatic-command timings.
