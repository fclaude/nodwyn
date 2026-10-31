# Approvals

When a tool call needs permission, the task stops and asks. This page explains what the question means and what each answer allows.

## What asks, and what doesn't

By default a write inside the task's folder goes ahead. The diff is logged and shown in the conversation, and the file's old contents are kept first, so Restore or `nodwyn undo` puts it back. A command without approval goes to the judge first, if you've set one up. If the judge refuses, or there isn't one, nodwyn asks you.

Some things always ask you, whatever the settings say: a write to a protected path (git hooks, CI configuration, the Makefile, `scripts/`, `.nodwyn/`), content that looks like a credential, a request that carries a credential, and an upload. Nothing outside the task's folder gets written at all.

![Settings, General: what a task may do without asking](shot:settings-general)

Settings › General › Approvals sets the first two. **Writing files** is *Without asking* or *Ask me*, and **Running commands** is *The judge, then me*, *The judge alone*, *Ask me* or *Without asking*. Whichever you choose for commands also answers a command that names a path outside the task's folder (a read of the Go module cache, say) or is built from what a tool returned. *The judge alone* refuses what the judge doesn't allow rather than putting it to you, and *Without asking* runs it; writes, sending out and keys still ask you.

Each task can have its own: **Commands**, under New task's **Options**, sets it for the next task, and the same select in a task's model picker, beside the judge, changes it at any time. Changing it while a question about a command is waiting answers that question the new way.

## The approval card

An approval shows up in the conversation, on Home under **Needs you**, and on your phone as a notice if you've allowed notices. It says which tool wants to run, why it's asking, and the exact call. For a write it shows the diff.

![An approval waiting on a phone](shot:phone-task)

The buttons are the answers you can give.

**Allow once** lets this one call through. **Deny** refuses it, and the model is told that you didn't confirm, so it can try something else.

**Always under** `ls` (the label names the command) approves that program and subcommand for the rest of the task. If the model runs one command through `bash -c`, the card offers the command inside it. A line with several commands, or a program that runs whatever it's handed (a shell, python, node, ssh), offers no Always and asks each time.

For writes there's also an answer that approves every further write until the turn ends.

Read the reasons under the heading. For example, "Command line names a path outside the repository" tells you why this call needs a separate answer. Check the call itself before allowing it.

Every answer is logged with who gave it: the terminal, this browser, or a named device.

## Approving ahead of time

Command prefixes you trust can go in `.nodwyn/allow.toml` in the project, so `go test` and `git diff` never ask. A project's own file is only read after you've reviewed it with `nodwyn trust`, because anyone who can commit to a repository can write that file.

```toml
commands = ["go test", "go build", "git status", "git diff"]
writes = ["internal/", "docs/"]
```

A standing approval still checks arguments: flags that pick a program, an output file or another working directory, and absolute or escaping paths, can't inherit it, and the question comes back.

## Don't ask

Settings › General › Approvals › **Don't ask in tasks** can offer a switch under each task's reply box. With it on, that task's ordinary writes and commands go through without a question until you turn it off or the task closes. Protected paths and credential-shaped content still ask, and nothing the guard refuses becomes allowed. It's never saved, so a resumed task asks again. The setting is off until you choose to offer it, and it shows a red warning while it's on. Use it only in a task you're watching.

## The judge

A judge is a second model that gets one question about a call: should it go ahead? If its answer meets the approval threshold, the call runs. Otherwise the question comes to you with the judge's answer. Calls that always need your approval still ask you. The judge sees redacted content, not the keys in your keychain.

![Settings, Judge](shot:settings-judge)

Settings › Judge sets one up. It's optional, and without one every question that isn't pre-approved comes to you.

[Your keys](keys.html) explains the other half of the safety story, the part about credentials.
