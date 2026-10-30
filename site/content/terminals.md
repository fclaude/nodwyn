# Terminals and other computers

The console isn't the only way in. Three terminal clients speak to the same daemon, and a task doesn't care which of them, or how many, are looking at it.

| Client | Command | What it's for |
|---|---|---|
| TUI | `nodwyn tui` | A full-screen client: chooser, conversation, approvals, plan and budget. |
| Line mode | `nodwyn` or `nodwyn chat` | Plain text in and out, for scripts and small terminals. |
| Runs | `nodwyn run` | A task carried to its end with no conversation. |

All three attach to the daemon on the default socket. If none answers, the TUI and line mode start one inside their own process and say so, and quitting then ends its tasks, as closing `nodwyn app` does. On a server with the daemon running as a service, they just attach.

## From your laptop to the server

The clients talk to the daemon over a Unix socket, so a terminal that's on the server's own account is a terminal on the daemon. From your laptop, that means an ssh session:

```sh
ssh -t server 'nodwyn tui --repo ~/src/project'
```

The quotes let the server expand `~` to your home directory there. This assumes its daemon is already running, as in [the server setup](server.html#start-both-at-boot).

You can disconnect without stopping the task. The TUI's **Ctrl+Q** detaches: the task and its turn go on in the daemon. Ctrl+C aborts the turn first, if that's what you want. Closing the ssh connection also leaves the task running. Open the TUI again, choose the task in the list (typing narrows it), and the conversation comes back from its log. **Ctrl+R** reconnects a TUI whose connection dropped without resending your message.

Run it inside `tmux` or `screen` on the server if you like your terminal to be there when you come back. The task is safe either way.

A task can be open in several clients at once, because the daemon holds it: a TUI in one terminal and the console on your phone, say. A message or an answer from either shows up in both, and a turn that one client starts is live in the other.

## Working in the TUI

The first screen lists a new task in `--repo` followed by your stored tasks. In a git repository's top folder, the first row starts a new task in a worktree of its own, as the console does, and `--worktree` does the same without the chooser. `--resume SESSION_ID` skips the list.

Approvals are keys: **y** allows once, **n** denies, **a** approves that command for the task, **t** approves further writes until the turn ends, and **b** saves a binding when the prompt offers one. Commands start with a colon:

| Command | Does |
|---|---|
| `:model NAME [EFFORT]` | Shows the model, or moves the task to another from its next turn |
| `:judge NAME` or `:judge none` | Chooses the judge the task asks |
| `:attach PATH` | Adds a file to the conversation |
| `:plan` and `:budget` | Show the plan, and this turn's limits and spend |
| `:log`, `:bindings`, `:replay` | Read-only views of the log, the bindings and the transcript |
| `:thinking` and `:details` | Show the model's thinking, and expand the tool calls and what the planner and sub-agents were told and replied |
| `:close` | Close the task in the daemon, and quit |

[The TUI page of the reference](../reference/tui.html) lists the rest. The TUI doesn't have a review view for a worktree's changes: `git diff` in the worktree does that job, and a comment on the changes is a message.

## Line mode and scripts

`nodwyn` with no command is a plain chat in the current repository, and `nodwyn --repo ~/src/x` pins it somewhere else. Everything comes out as text, so it works in a pipe. `--no-confirm` makes every approval a no, which is what you want in a script that nobody watches.

```sh
nodwyn sessions                  # id, last activity, state, repo, first ask
nodwyn replay SESSION_ID         # a task's log as a transcript
nodwyn --resume SESSION_ID       # carry on in its folder
```

`nodwyn run` carries a task through without a conversation, for scripts and benchmarks:

```sh
nodwyn run --task task.txt --repo . --budget-wall 2h --max-turns 20
nodwyn run status SESSION
nodwyn run stop SESSION
nodwyn run resume SESSION
```

The example sets a two-hour deadline and a maximum of 20 turns. Those limits are optional; without them, a run has neither. You can attach the console or the TUI to answer its prompts. For a run nobody watches, `--unattended` declines confirms unless `--yolo` approves ordinary ones or `--judge` asks the judge. [Runs](../reference/runs.html) covers completion and recovery.

## Terminals that aren't on the server

The terminal clients talk to a Unix socket, so they reach the daemon on the computer they run on. For a task on the server from a window on your laptop, link the two with [Hosts](server.html#one-window-for-several-computers). The laptop's console then shows and answers the server's tasks. The TUI and line mode on the laptop show the laptop's tasks only.
