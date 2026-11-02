# ADR 0060: Windows through WSL 2

- Status: accepted; route and acceptance requirements documented; WSL testing and integration not built.
- Date: 2026-11-02
- Amends: 30, 44, 58.

## Context

The host uses Unix process groups, advisory file locks, file ownership,
local peer credentials and pseudo-terminals. A Windows cross-build fails
on those operations. An exploratory native port also found that Windows
append handles and Unix shell parsing cannot simply stand in for the
existing locking and path checks. That exploration is not shipped.

There is now a Windows 11 x64 test machine. Its WSL default version is 2,
but no distribution was installed when this decision was made. The
maintainer does not use Windows, so tests must establish the experience;
Linux CI alone is not evidence of a working Windows-browser route.

## Decision

1. The intended Windows route is the existing Linux host inside WSL 2,
   with the console opened in a Windows browser through localhost. Document
   this as an unverified route until the acceptance gate has run. Do not
   publish a native Windows binary or advertise native support.
2. Repositories, private state and runtime sockets stay in the Linux
   filesystem. Commands and interactive terminals use Linux tools. Windows
   executable interoperability, Windows-mounted repositories and native
   desktop integration are not part of this initial route.
3. Use `nodwyn app --no-open` and the complete printed launch URL; no new
   launcher or RPC transport is needed to describe the route. Verify that
   the actual Windows browser can open, pair, reload and reconnect to it
   before calling it supported. Windows login startup and desktop
   integration are not built.
4. Require tests on an actual Windows/WSL 2 machine: an isolated fake-model
   coding session, approval refusals, credentials, exfiltration, persistence,
   terminals, process cleanup, WSL restart and the Windows-browser flow,
   besides the full Linux acceptance suite inside WSL. Record environment
   and source versions. Missing prerequisites must not silently produce a
   support claim. This automated gate is not built or run yet.
5. Treat Docker Desktop's WSL integration as an optional, separately tested
   capability. Require contained fake MCP, mount and egress checks before
   claiming it. That combination has not been verified.

## Consequences

This change documents the decision and the installation route in
[Windows through WSL 2](../windows.md); it supplies no Windows runtime
changes. The original native-port work on filesystem protections, Job
Objects, named pipes, ConPTY, a WebView2 app, a native installer and Windows
release archives is deferred. WSL installation, automated acceptance,
Windows browser launching and optional Docker integration remain work to
build and run. Existing Linux/macOS release checks do not certify them.

The user-facing documentation and release books must keep that distinction
until evidence changes it. A future implementation updates this ADR with
what ran and which capabilities passed, rather than treating this decision
as a test result.
