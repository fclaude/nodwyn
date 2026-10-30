# Working in a task

A task is a conversation with the model, pinned to a folder. Everything it does is recorded in a log, and the page you're looking at is drawn from that log. That's why you can close the tab, open the same task on your phone, and find it where it was.

## The conversation

Your messages, the model's replies and its tool calls are in the middle column. Routine work is folded into **Activity** rows, one per stretch between replies, which say in a line what happened: "read 2 files, searched once, listed 1 folder". Click one to see each call. When a reply is only thinking and reading, the thinking sits inside that row, and so does what the planner and other helpers reply to nodwyn, such as a plan written in Scheme. Thinking is hidden until you choose to show it in Settings › This browser.

![A task with its activity opened](shot:task-activity)

When the model needs something from you, an approval card appears in the conversation and the task shows **Waiting for you** in its heading. [Approvals](approvals.html) has the details. The reply box under it says to answer first, because a turn that's waiting can't take another message.

While a turn runs, you can write a message and choose **Queue** to send it after the current response and any messages already queued. **Steer** stops the current response and sends your message next, ahead of the queue. **Stop** stops the response and discards messages still waiting in the queue.

The heading shows the action that fits the state: **Stop** while the model works, **Resume** once the task is saved. The **⋯** menu holds the rarer ones: **Search** within the task, **Schedule…**, **Close**, **Archive** and **Delete**.

## The panel on the right

At the top, three numbers stay put across every tab: what the task has spent so far (it says *unpriced* when the provider gives no prices), how long the current turn has run, and how full the context window is. A warning line appears when any of them reaches 90% of its limit.

Under them are four tabs.

**Progress** is the plan. The model writes a plan for work that needs one, and marks each step done or skipped as it goes, with what shows it. A step folds to its title, and clicking it shows the whole of it.

**Changes** lists every file the model wrote, with the diff you approved. Click a file to see it with line numbers. The label says *Approved preview*, and it isn't proof that the write succeeded: approval comes before the write, and the tool result says whether it worked. **History** keeps the previous versions of a file, so you can compare an old version with the file as it is now and **Restore** the one you want. nodwyn keeps a version before it writes, unless git already has the file committed and unchanged.

**Statistics** has this turn's budget (tool calls, time, tokens, actions, and spend if you set a limit) and the task's totals, including how much of the prompt the provider served from its cache.

**Workspace** shows where the task works, the tools it was offered and the project notes it kept.

## Worktrees and review

A task in a git worktree has a **Review** button next to its conversation. It replaces the conversation with every file the task changed since it branched, committed or not. Unified is the default, and Side-by-side lines the old and new files up. Hover a line and press **+** to comment on it, or comment on a whole file. **Send review** sends all the comments at once, ordered by file and line, and the task answers them in one turn. Only your comment and the line's number are sent, never the line itself. **Close review** brings the conversation back, and doesn't close the task.

The work stays on its branch until you land it. Ask the task to "commit and push to main", and once its checks have passed it does that in one step: it commits with the message the model writes, rebases onto the newest base, refuses a change that adds credential-shaped text, and pushes by fast-forward only. It asks you first, like anything that can't be undone. Commits are signed the way your git configuration says, and if signing fails it stops instead of committing unsigned.

Closing a task removes its worktree and branch when nothing would be lost. If there's work on the branch, both stay and resuming picks them up again.

## Limits

A task stops at the limits in Settings › General, so a runaway turn can't spend your evening. There are hard ceilings on tool calls, time, tokens and actions that attempt something irreversible, and a spend limit you can add. You can also set a lower soft limit, and the task asks before going past it. Answer yes to double it up to the hard ceiling, or type a number. Turns also stop on their own when the model repeats the same call four times in a row or keeps hitting errors, and the reason is shown.

The **Limits** box under the reply field turns the limits off for one task. It's there for the long jobs you're watching.
