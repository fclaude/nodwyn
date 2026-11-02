# Windows through WSL 2

The chosen Windows route is to run nodwyn's Linux build inside WSL 2 and
open its console in a Windows browser. There is no native Windows
executable, installer or WebView2 app. The Linux host's process, file and
terminal operations stay Linux operations (ADR [60](adr/0060-windows-through-wsl2.md)).

**Status:** documented, not yet verified end to end on Windows. The current
release checks run on Linux and macOS; they do not establish WSL support.
No WSL distribution was installed on the Windows test machine when this
route was chosen on 2026-11-02. The acceptance gate below remains to be
implemented and run before this route is described as tested support.

## Set up the Linux side

1. Install WSL 2 and a Linux distribution following
   [Microsoft's installation guide](https://learn.microsoft.com/en-us/windows/wsl/install).
   In PowerShell, `wsl --list --verbose` should show version **2** for the
   distribution you will use.
2. Open that distribution's terminal. Follow [the release installation
   instructions](releases.md) **inside Linux**, choosing the Linux archive
   for that distribution's architecture: amd64 for x86-64, arm64 for ARM64.
   Keep `nodwyn` and `nodwyn-egress` beside each other on the Linux `PATH`.
3. Keep repositories in the distribution, for example `~/projects`, and
   keep nodwyn's configuration, keychain, logs and runtime socket in their
   default Linux locations. `nodwyn paths` prints the configuration and
   data paths. Do not point them at `/mnt/c` or another Windows mount:
   Windows-mounted files have different permission and filesystem behaviour.
   Microsoft also recommends the Linux filesystem for
   [Linux-tool performance](https://learn.microsoft.com/en-us/windows/wsl/filesystems).
4. From the Linux terminal, run:

   ```sh
   nodwyn version
   nodwyn app --no-open
   ```

   Copy the **complete** address printed after `app: open` into your
   Windows browser, including the launch ticket after `#`. Keep the
   process running while you use it. WSL provides
   [Windows-browser access to Linux localhost servers](https://learn.microsoft.com/en-us/windows/wsl/networking).
   You do not need to expose the console to the network for this route.
5. Set up the model in the console and choose a Linux repository path,
   such as `/home/yourname/projects/example`. The browser is on Windows;
   the task, its files and its commands are in Linux. Do not enter a
   `C:\...` path or expect a native Windows folder chooser.

These are setup instructions for the intended route, not a claim that
this installation and browser flow have passed the gate below.

## What remains different

- Use Linux Git, language runtimes and build tools for the task. Windows
  command execution and process cleanup through WSL interoperability have
  not been validated. Do not assume a Windows executable behaves like a
  Linux child process.
- This route uses the browser console. A native Windows shortcut, folder
  chooser, notification integration and login launcher are not supplied.
- Shutting down the distribution or WSL stops its daemon and tasks. The
  Linux login-startup option does not install a Windows service or arrange
  for the distribution to start when Windows starts.
- A model server running on Windows has a different network path from one
  running in WSL. Configure an address reachable from the distribution;
  do not assume Linux `localhost` is the Windows host. See Microsoft's
  [WSL networking guide](https://learn.microsoft.com/en-us/windows/wsl/networking).
- Containers are optional. Docker Desktop has a
  [WSL 2 integration setting](https://docs.docker.com/desktop/features/wsl/use-wsl/),
  but nodwyn's container mounts, network policy and egress helper must be
  tested through it before that combination is claimed as supported.
- WSL is a development environment, not a disposable sandbox. Do not turn
  on `--sandbox container` merely because nodwyn is running in WSL.

## Acceptance gate before claiming support

The Windows machine must exercise the actual route, rather than merely
cross-compile a binary or rerun Linux CI elsewhere. Use an isolated test
home, temporary repositories and the existing fake model, judge, OAuth and
MCP services. No paid model, real credential or personal repository is
needed.

| Check | Required evidence |
| --- | --- |
| Installation | Install an actual Linux release archive in WSL 2; check its digest and source version |
| Windows browser | Open the complete launch URL from Windows, pair, reload and reconnect; reject an invalid ticket and an unpaired request |
| Coding session | A fake-model task reads, edits and tests a repository in the Linux filesystem; a fresh and a resumed session preserve their log and plan |
| Approvals and credentials | Refusals remain refusals; keychain, policy, path and exfiltration tests pass without exposing test credentials |
| Send and Queue | Send and Enter steer a running response, Queue waits, each message runs once, and an approval still blocks the composer |
| Lifecycle | Timeout, Stop, cancel and daemon shutdown leave no task descendants; restart can resume the log; WSL shutdown and restart are exercised in a dedicated test distribution |
| Files and terminals | Spaces, Unicode, symlinks, worktrees, locks, concurrent updates, terminal input and terminal closure run on the Linux filesystem |
| Optional containers | A contained fake MCP server uses Docker's WSL integration, the correct Linux egress helper, allowed mounts and fail-closed network rules |

Run the full Linux acceptance and browser suites **inside WSL** too. Run
the browser-flow checks from Windows so localhost forwarding and the
Windows browser are covered. Keep the Windows test gate required for
future Windows/WSL release claims, report skipped prerequisites as
incomplete coverage, and preserve logs identifying the Windows build,
WSL version, distribution, source commit and archive tested. Until that
automation is built and passes, release notes must keep the status above.
