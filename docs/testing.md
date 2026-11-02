# Development tests

Use Python 3.9+ and the Go release required by `go.mod` (or a newer stable
release). From the repository root:

| Command | Purpose |
| --- | --- |
| `make test` | The complete Go suite without race instrumentation, for iteration |
| `make test-race` | The complete Go suite with the race detector |
| `make accept` | Lint, the race suite, then the separate exfiltration gate |
| `make validate` | Required finish check, including race acceptance for code changes |
| `make test-unsharded` | Ordinary `go test ./...`, for process-state/order investigation |
| `make test-race-unsharded` | Ordinary `go test -race ./...` |
| `make test-runner` | The runner's fixture tests, also run by `test-race` |

Iteration avoids the runner's fixture-test overhead; the race target runs those
checks before its full Go suite, so acceptance still validates the runner too.

Before pushing, run `make validate` after the last edit. An iteration pass alone
is not acceptance. CI still runs `make accept`, and promotion uses the same
race gate. There is no automatically installed Git hook: the repository's
agent instructions establish the before-push rule.

## Automatic platform settings

On Linux and macOS, `scripts/test.py` compiles the slow packages jointly and
runs disjoint groups of tests in independent processes. This lets serial-heavy
packages run concurrently and keeps individual heaps smaller. On macOS,
subprocess launches from a large Go heap can trigger extensive copy-on-write
faults and kernel contention; small worker processes reduced that cost in the
2026-11-02 measurements. No garbage-collector workaround is required.

Core uses up to 16 workers; prim and RPC use up to 8; CLI and validation use
up to 4, each bounded by available logical CPUs. Each worker's GOMAXPROCS is
`max(2, logical_CPUs / package_workers)`. Remaining packages use ordinary Go
package scheduling. Hot test processes run through a queue capped at the logical CPU count
(minimum 2, maximum 32 concurrent processes); Go still schedules the remaining
packages normally. This avoids flooding a laptop and overrunning short
application deadlines. These are worker budgets, not CPU affinity limits.
Explicit runtime settings are respected as described below.

Committed scheduling hints are selected by Go target OS/architecture
(`go env GOOS GOARCH`, so a Rosetta Python does not select the wrong weights):

- `scripts/test-weights/darwin-arm64.json`: measured Apple silicon macOS
  core timings and prim timings.
- `scripts/test-weights/linux-amd64.json`: measured Linux core and prim timings.
- Other Linux/macOS architectures: the same adaptive worker scheme, with
  ordinary weights for every test.

The JSON records the source revisions, toolchain, measurement date and method.
Contributors who can edit the Makefile/tests also maintain these files. They
supply durations only, not commands or access rules. **The current compiled
binary's inventory decides which tests run.** New or renamed tests receive a
fallback weight; stale entries cannot exclude tests. Test, Example and Fuzz
seed entries are included. Each worker must report every assigned top-level
result exactly once, and the other-package command must complete every
remaining package. A missing result is a failure even if the process exits 0.

Both sharded modes execute fresh assertions (`-count=1`); compiler/module
caches remain enabled. Platform-dependent skips remain skips. Each test binary
has a five-minute timeout; the runner has a fifteen-minute outer deadline.
Logs, assignments, results and process-cleanup receipts are written under a
private temporary directory whose path is printed. Failures retain those logs.
The supervisor cleans up only processes it started, checking identity before
signalling them.

## Filters and explicit Go options

Existing `GO_TEST_FLAGS` calls still work:

```sh
make test GO_TEST_FLAGS='-run TestName -count=2'
make test-race GO_TEST_FLAGS='-run TestName -v'
```

Filters, coverage/benchmark options and other explicit flags use ordinary Go
execution so their semantics are not changed by sharding. `-count=1` alone
remains optimised because it is already the sharded default (including CI's
existing command). Explicit GOFLAGS, GOMAXPROCS, GOOS or GOARCH environment
settings also select ordinary Go execution. GOGC, GOEXPERIMENT and other
runtime settings are not silently reset; the default collector needs no
special configuration.

`NODWYN_TEST_MODE=unsharded make test` (or `make test-race`) is another way to
select the ordinary runner. Sharding changes which tests share process-global
state and co-execute; retain unsharded runs for order-dependent investigations.
Windows development uses WSL 2 and therefore the Linux runner. This does not
add native Windows support to Nodwyn.

The browser checks remain `make browser`; Go assertions do not test the console
in a browser. These commands do not call paid model services.

## Integration checks (2026-11-02)

The normal commands were exercised on the same current-source snapshot on
Linux/amd64 (Tarro and X1) and macOS/arm64 (mbpro), using Go 1.27.3. All 58
Go packages completed, including 1,000 named tests/examples in the split
packages. Single warm checks of the non-race command took approximately
12 seconds, 18 seconds and 41 seconds respectively; these are checks of this
revision, not performance guarantees or multi-run medians. Both full race
commands also passed on those platforms. The runner's fixture checks belong
to the race/acceptance path rather than every iteration.

An early unbounded prototype exposed timing-sensitive assertions:
`TestARunStartsWithoutLimitsOfItsOwn` on X1 and
`TestFsEditAlsoOccursAsksTheGuardAboutWhatItNames` on macOS. The RPC failure
was also recorded in the earlier experiment against older, unchanged
application code; focused unsharded repetitions in this integration passed.
The filesystem edit assertion depends on the application's 400 ms sibling
search deadline. These failures were not skipped or rewritten; the bounded
worker configuration passed the complete suites. Retain failures and use the
unsharded commands when investigating scheduling-dependent results.
