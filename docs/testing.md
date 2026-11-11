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

## Gitea Actions

Gitea selects `.gitea/workflows` before `.github/workflows`. Its check runs the
same acceptance gate, release-builder tests, vulnerability scan and portable
builds as GitHub, and also builds the website. It selects `linux-alpine` by
default; the Gitea repository Actions variable `NODWYN_LINUX_RUNNER` can select
another Alpine-based runner. That runner needs Bash, Git, Node, a C compiler,
Python with `venv` and `pip`, Perl, `desktop-file-validate`, `gio`, and public
HTTPS access. These tools are installed in the runner image ahead of the job.

The Gitea check confines the sharded, uncached acceptance suite to as many CPUs
as the runner container's cgroup quota (`/sys/fs/cgroup/cpu.max`) allows, since
the sharder sizes itself from the CPUs it may run on and a quota does not limit
those; without a readable quota it uses two. Its log says how many it used. It
was fixed at two until 2026-11-11, which left half of a four-CPU runner idle: the
race suite took 407 s there, against 194 s on GitHub's four. It does not run the browser checks or build the macOS app;
GitHub's CI runs those, and GitHub alone publishes the website and releases.

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

## Automatic macOS RAM workspace

The normal full `make test` and `make test-race` commands now create one private
2 GiB HFS+ RAM image per invocation on macOS, with at least 6 GiB of free or
reclaimable memory. Fixtures, Go build scratch and compiled test binaries use
that image. The checkout, durable module/compiler caches, verbose logs,
inventories and cleanup receipts remain outside it. Reported suite time
includes image setup and teardown. Linux/WSL use their existing temporary
filesystem; no image or new mount is created there.

The native macOS tools need no sudo or machine-wide configuration. Before
initialising the newly attached device, the runner verifies the RAM URI,
current user, whole virtual-disk identity, expected size and image-helper PID.
Each invocation has a random volume name and independent device receipt.
Its host parent uses a short `/private/tmp/nw-*` path, avoiding fixture path
truncation and local-socket path limits.
Because a new HFS+ image can ignore ownership, it is remounted **beneath the
runner's short mode-0700 host directory** before fixtures or binaries are written.
Access is therefore enforced by the host filesystem outside the image.

Workers finish or are cleaned up before the image is detached. SIGINT,
SIGTERM and SIGHUP unwind through cleanup; signals during a bounded image
operation are deferred until ownership has been recorded. Native commands have
30-second deadlines; transient busy detaches get at most five identity-checked
attempts, without force-unmounting. Cleanup rechecks image identity rather than detaching a
possibly reused device ID, checks the helper's identity/exit, and removes the
empty private mount point and host directory. A cleanup failure fails the command. The final
success line appears only after teardown.

Insufficient memory or an unavailable tool/setup operation falls back visibly
to disk after any known image has been cleaned up. An ambiguous device identity
or uncertain attachment does not silently pass. `workspace.json` retains the
image/device/helper details, tool PIDs/exits and cleanup status;
`workspace-tools.log` retains native-tool errors beside the normal test logs.
A full RAM volume is a test failure, not a reason to discard an attempt and
silently rerun it. No test, timeout, assertion or skip changes.

For disk-backed diagnosis while retaining normal sharding:

```sh
NODWYN_TEST_RAM=off make test
NODWYN_TEST_RAM=off make test-race
```

Filtered/explicit Go commands and the unsharded diagnostic commands continue
to use ordinary Go without allocating an image. In automatic RAM mode the
runner scopes TMPDIR/GOTMPDIR to its own children; the disk opt-out preserves
caller-supplied temporary paths. A hard kill or host crash cannot execute
cleanup: use the retained receipt and `hdiutil info -plist` to verify the
recorded image identity before detaching a surviving image. Normal completion,
test failure and handled interruptions remove it automatically.

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

## macOS temporary storage follow-up (2026-11-02)

On mbpro, the normal non-race command took 41.42 seconds with its ordinary
SSD temporary directory, versus 19.99 and 19.60 seconds with temporary fixtures
and Go build work on a disposable APFS RAM volume. These are individual warm
runs, not three-run medians. An HFS+ RAM control was similarly fast; a shorter
SSD path remained slow. Tarro stayed near 12 seconds with either RAM or SSD
scratch space. No application source, test assertion, worker default or GC
setting changed. See the [profiling report and resource evidence](http://kitten:3000/fc/nodwyn-evals/src/branch/main/benchmarks/analysis/workstation-tests-2026-11-02/remaining-gap.md).

The manual workspace used in that experiment has now been integrated into the
normal full commands, with private mounting, retained diagnostics and cleanup
as described above. HFS+ was chosen for the disposable image because it has a
simple whole-device lifecycle; both HFS+ and APFS RAM controls passed the full
suite. Higher concurrency and GOGC=25 gave little additional benefit and were
not made defaults.

### X1 storage control

X1 already uses a 16 GiB tmpfs for `/tmp`. Three alternating warm non-race runs
on balanced power, using the same source and Go 1.27.3, passed all 58 packages:

| Scratch filesystem | Samples (seconds) | Median |
| --- | --- | ---: |
| Existing RAM-backed `/tmp` | 18.408, 19.662, 19.912 | 19.662 |
| SSD-backed Btrfs `/var/tmp` | 19.654, 21.230, 21.002 | 21.002 |

RAM was about 6% faster in this control, not the Mac's 2x improvement. X1's
normal command already gets the RAM-backed environment; this macOS integration
does not add another RAM allocation or change Linux's worker settings.

The integrated short private mount passed the full non-race Mac command in
23.615 seconds end to end, including allocation and teardown. Native checks
also verified cleanup after a failed fixture test and SIGTERM, and that two
concurrent images remain independently owned. The runner's 31 fixture tests
cover setup/fallback, signal handling, reused device IDs, bounded busy retries
and cleanup failure. A long-path prototype exposed the existing guard-log
summary limit; shortening the host mount parent fixed the environment without
changing that assertion. The current command retains logs after the image is
removed.
