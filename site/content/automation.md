# Schedules and workflows

A task can start by itself. There are two ways to set that up, and they live in different places.

## A schedule

A schedule runs one message again and again, in a task of its own. Write the message on Home, open **Options** and choose **Schedule…**, or choose **Schedule…** from a task's **⋯** menu to schedule that task's last message.

**Run type** also offers **Run Scheme**. Write a program in the highlighted
editor to run it directly in the task's harness, with its tools, approvals,
limits and Stop button. Saving checks syntax; **Run now** executes it and
shows its output and returned value in the task. For a first check:

```scheme
(display "Workflow ran")
(+ 20 22)
```

This returns `42` and asks no model. To ask one explicitly, use `add-message`
followed by `model-turn`. A program's definitions last while its task is
open and reset when it resumes; keep lasting state in files. Manual runs
perform real actions, so try file-writing programs in a scratch workspace.

![A Scheme program in the schedule dialog](shot:schedule-scheme)

![The schedule dialog on Home](shot:schedule)

The dialog offers set times on chosen weekdays, an interval within the hours of a day, and monthly dates or a weekday of the month. **Recent runs** lists when each one started, how it ended or why it was skipped, and what it cost.

Scheduled runs happen in the daemon, so they need it to be running. Settings › General › **While you're away** › **Start at login** keeps it going after you close the window, and on a computer nobody logs in to, [the server page](server.html) shows how to have it start at boot. If the keychain has a passphrase, a daemon that started by itself is locked, and a run that needs a key waits until you unlock it from Home, once. It then runs once, late.

## Workflows

Workflows is a page for everything that starts without you.

![Workflows](shot:workflows)

It lists every timetable, along with workflows that watch something and start when it changes. A watcher can follow the pull requests of a Gitea or GitHub repository, a Gmail search, or the lines a command prints. Each event is handed to the task as data, labelled as coming from outside, so a pull request that says "ignore your instructions" is read as text.

The builder's **Do** card has the same **Run type** choice. **Check now**
processes new events using the saved message or program; **Test the trigger**
only previews the source and filters. A program's model reply can wait in
the outbox, while printed output and returned values stay in the task.

The page shows today's runs and spend, and a daily spend limit you can set. Each workflow can be paused, resumed, checked or run right now, and **Test the trigger** shows what it would have started before you save it.

A workflow's answer doesn't have to go out on its own. A review it wrote for a pull request, or a reply to an email, can wait in an outbox that shows up in **Needs you** with **Post review** or **Save draft**, so nothing leaves until you've read it.

From a terminal, `nodwyn workflows` lists them and `nodwyn outbox` handles the held answers.
