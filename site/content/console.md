# A tour of the console

The console is a web app that a daemon serves. `nodwyn app` shows it in a window or your browser, `nodwyn ui` serves it to a browser, and the same page works on a phone. Everything on this page is the same in all of them.

## The sidebar

The sidebar is the same on every page. **New task** is at the top (⌘N on a Mac), and the magnifier next to it searches what your earlier tasks did. Under that, **Needs you** lists the tasks that have stopped to ask something, and below it your folders, each with its tasks. At the bottom are the pages: Connections, Workspaces, Workflows, Hosts and Settings. The last line says which daemon you're talking to and whether it's ready.

On a phone the sidebar is a drawer, and a pill in the top bar says how many tasks need you. Tap it and you land on the one that's waiting.

## Home

Home is where you start things and see what's going on.

![Home: the ask box, what needs you, what is running and what finished](shot:home)

The box at the top starts a task. Under it are **Attach**, for files and images, **Options**, and the model picker. Options holds the choices most tasks don't need: whether to work in a worktree, whether the turn limits apply, which helper models the task may hand work to, and **Schedule…**, which turns the message into a recurring task. The count on Options tells you when something is off its default.

Below the box are four lists. **Needs you** holds the approvals and questions waiting for an answer, with **Review** and **Allow once** right there. **Running** shows what the model is doing now, **Scheduled** what will start by itself, and **Recently finished** what's done, with a **Review** link beside each worktree task.

![Home with Options open](shot:home-options)

## A task

A task is a conversation in a folder. The page has the conversation in the middle, with its approval card inline when the model is waiting for you, and a panel on the right with the plan and the numbers. [Working in a task](tasks.html) goes through it.

![A task waiting for an answer](shot:task)

## The other pages

Settings has five tabs: General, Models, Judge, Keys & devices and This browser. Connections holds the keys and the MCP servers. Workspaces lists the folders. Workflows holds the timetables and the watchers. Hosts is where you link other nodwyns, which matters when you run [a server](server.html). [Models, keys and folders](setup.html) goes through the first three, and [schedules and workflows](automation.html) the last.

## Light and dark

The console starts dark. Settings › This browser chooses light, dark or whatever your system uses, and whether the model's thinking is shown. Both choices live in the browser, so your phone and your laptop can differ.
