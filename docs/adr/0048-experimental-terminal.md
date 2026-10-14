# ADR 0048: A terminal for the model, experimental and unguarded

- Status: **experimental**; implemented 2026-10-14. Temporary: it is kept
  only until what it is worth has been measured (Decision 7), and is then
  removed or rebuilt under a new ADR. Nothing here is a promise to support
  it as it is.
- Date: 2026-10-14

## Context

`shell-start`, `shell-read` and `shell-send` run a program that keeps
running, but over pipes. A program that checks for a terminal (tmux, neovim,
`less`, `htop`, most REPLs' line editing) does not work over pipes, and what
a full-screen program writes arrives as escape codes, not as the screen a
person sees. A model cannot edit with neovim, work inside tmux, scroll back
through output, or answer a program's menu.

The person asked whether the model could be given a terminal: something that
runs tmux, where it can use neovim, run commands, look at the screen and
scroll. They asked for it without guards, to see how much it helps; if it
does, making it safe is the next piece of work. This ADR records what was
built for that, what was left out, and what would have to be true to keep it.

## Decision

1. **Six primitives, offered as tools** (`internal/prim/term.go`):
   - `term-open cmd args [cols rows]` runs a program on a pseudo-terminal
     (100 by 30 unless told otherwise) in the repository root, with the
     scrubbed environment of `shell-start`, `TERM=xterm-256color`, a UTF-8
     locale when there is none and its own `TMUX_TMPDIR`; it returns the
     screen once the program has been quiet for 300 ms.
   - `term-type id text` types text, a newline pressing Enter; `term-keys id
     keys` presses keys named as tmux names them (`Enter`, `Escape`, `Up`,
     `PageDown`, `F5`, `C-w`, `M-x`, `BTab`), or a character. A name that is
     not one is an error and presses nothing. Both return the screen once
     the program has been quiet for 150 ms, or after 2 seconds.
   - `term-screen id [offset]` returns the screen now, or, with `offset`, the
     view scrolled that many lines back through the history of a program
     that does not use the alternate screen (5000 lines are kept). It needs
     no approval.
   - `term-wait id [seconds] [until]` waits until a line of the screen
     matches a regular expression, or, without one, until the program has
     been quiet for half a second, or until it exits, or for at most 60
     seconds, and says which. It needs no approval.
   - `term-close id` hangs up (SIGHUP to the process group, SIGKILL after 2
     seconds) and returns the last screen. It needs no approval.

   The model reads the screen as text: a header (state, size, cursor row and
   column, whether the program is full-screen), then each row with its number
   and `| `, three or more blank rows in a row as one line. The program's
   output goes through a terminal emulator (`charmbracelet/x/vt`), which
   also answers the questions programs ask of a terminal (neovim and tmux ask
   where the cursor is and wait), and encodes keys for the modes the program
   has set. A pseudo-terminal comes from `creack/pty`. Both are new
   dependencies.

   The emulator is a Charm library, and ADR 10's boundary lint forbids the
   core, and so `internal/prim`, any package of that family. The primitives
   therefore use an interface (`internal/termemu`, which imports nothing),
   the adapter over `x/vt` is `internal/frontend/termvt`, where Charm
   libraries are allowed, and `cmd/nodwyn` links it in with a blank import
   as a SQL driver is. A binary that does not link it opens no terminal: the
   primitives are there and `term-open` says so. The lint was not changed.
2. **It is experimental and temporary, and says so where it is used.** It is
   off unless `[experimental] terminal = true` is in `config.toml`; Settings
   does not offer it, deliberately, since a switch there would suggest a
   feature. A session with it on records a note saying it is experimental,
   temporary and unchecked; every primitive's description, which the model
   reads, begins with the same; the prompt for `term-open` says it is
   experimental; the manual says it; and this ADR's status says it. When it
   is removed the config key goes with it, and a `config.toml` that still
   names it fails to load with the unknown key, which is how the person finds
   out.
3. **Nothing the model types is checked.** `term-open` is irreversible and
   asks, once, like `shell-start`: its `cmd` and `args` are an `argv` role, so
   the argv trigger, the taint rule and the credential check read the command
   line that starts the program. After that `term-type` and `term-keys` run
   without a confirm, and nothing reads what they send. So a terminal is a
   shell with the person's privileges, and what that means is: the path
   denylist and the repository pin do not reach what a program run in it
   opens (the model can open any file with neovim, or `cat` it in a shell);
   writes by a program in it are not versioned or shown with a diff, git is
   the only undo; its network is unrestricted; a standing approval for
   `shell-exec` neither covers nor limits it; and the judge answers only the
   `term-open` prompt. The primitive's description tells the model that a
   refusal from another tool is the answer, not something to get around
   through a terminal; that is a request, not a control. `approval.dont_ask`
   and `--yolo` answer the `term-open` prompt as they answer any other.
4. **What is done so that it is usable and does not leave things behind.**
   A tmux server detaches from the terminal that starts it, so hanging up
   does not end it, and the default tmux socket is the person's own. Every
   terminal of a session therefore gets one private `TMUX_TMPDIR` (a short
   directory under `/tmp`, since socket paths are short), the server survives
   `term-close` so a later `term-open tmux attach` finds it, and closing the
   session runs `tmux kill-server` there and removes the directory. At most 3
   terminals are open at once; `term-type` sends at most 16 KiB and
   `term-keys` presses at most 200 keys; a write the program does not read
   fails after 5 seconds instead of hanging the session; a panic in the
   emulator ends that terminal and not the daemon.
5. **The progress guard counts terminal calls on their own.** It ends a turn
   on the same call four times in a row, which is a loop for most tools and
   is how one moves down a file in a terminal (`Down` four times). Successful
   terminal calls are not repeats; 150 of them with no other tool between
   end the turn (`screenLimit`, a guess: no log has driven a terminal yet).
   A call that fails still counts as it does for any tool.
6. **Not built.** Each of these is a reason this is not the final design:
   - Any check of what is typed: a per-keystroke guard, a command-line
     parser over a shell's input, a pin for paths a program opens.
   - Running the terminal in the container runtime of ADR 23 (read-only
     root, no network but the egress proxy), which would make unchecked
     keystrokes defensible. It needs an image with the tools in it, and the
     host's toolchain is not there.
   - Text attributes. The screen is plain text: a menu entry shown only by
     colour or reverse video, a visual selection, a syntax error underlined
     in red, are not visible to the model.
   - Resizing after the program starts, the mouse, bracketed paste (text is
     typed as typed, so an editor with auto-indent mangles pasted code; use
     `fs-edit` for that), and a live view of the terminal for the person in
     any client (`docs/clients.md`).
   - Seeing it in the loop's accounting: a test run in a terminal is not a
     `shell-exec`, so `ran-ok?`, the turn summary and the check loop do not
     know about it.
   - Windows: pseudo-terminals here are Unix ones, as `shell-start`'s process
     groups already are.
7. **How it is judged, and what happens after.** Use it for real work with
   `[experimental] terminal = true` and look at the sessions: which
   `term-*` calls the model made, whether tasks that failed without it
   finished with it, how many calls and tokens a screen-driven edit takes
   against `fs-edit`, and what the model did that the guard would have
   stopped. If it earns its keep, a new ADR designs the guarded terminal
   (Decision 6) and replaces this; if not, the removal is: `internal/prim/term.go`
   and `term_test.go`, `Options.Terms`, the session's `terms` and its `Close`,
   `config.Experimental`, the progress guard's `terminal` outcome, the six rows
   of `docs/scheme.md` and the book and expert guide tables, this ADR's
   status, and the two dependencies (`go mod tidy`).

## Validation

`internal/prim/term_test.go` (linking the real emulator, as the binary does)
runs programs on the terminal: that they have
one (`test -t 0`, `stty size`), typed text echoing, the bytes each named key
sends (read back by a program in raw mode), keys refused without any being
pressed, a program's cursor-position question being answered, scrollback and
the alternate screen, `term-wait` returning for each reason, exit and close,
the limit, the hang-up of every terminal when the set closes, and tmux
(skipped without it): its socket in the session's directory, its server
outliving `term-close` and gone when the session ends. `internal/core`
checks that a session offers the primitives only with the setting on, asks
once when the model opens a terminal and not for what it types, and hangs up
on a terminal left open. `internal/termemu` checks that without an adapter
there is no terminal. `internal/loop` checks the progress guard. neovim and
tmux were also driven by hand through the primitives on 2026-10-14: neovim
opened a file, took an insertion and wrote it; tmux split a window, ran a
command in each pane, was left, and was attached again.
