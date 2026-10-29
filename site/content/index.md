<p class="lede">nodwyn is a coding agent that keeps your keys out of the model's hands. It runs on your computer or on a server, and you work with it from a window, a browser, a phone or a terminal.</p>

![The console: a task waiting for an answer, with its plan beside it](shot:task)

The model writes the plan and asks to run commands. Your API keys sit in an encrypted keychain, and the model only holds a handle to one. A key can be used at the places you bound it to, and the host puts it into the request after the model has finished writing it. Everything that leaves Scheme for the host goes through one guard, and what the guard refuses stays refused.

This guide walks through the console and shows how to run nodwyn on your own computer or on a server.

<div class="ways" markdown="1">
<div markdown="1">

### On your computer

Start with `nodwyn app`. It opens the console in a window or your browser and starts a daemon if you don't already have one running.

[Run it standalone](guide/standalone.html)

</div>
<div markdown="1">

### On a server

Keep the daemon running on a server and open its console from your laptop or phone. Terminals on the server attach to the same tasks.

[Run it on a server](guide/server.html)

</div>
<div markdown="1">

### From a terminal

A full-screen client and a plain chat, both attached to the daemon, so a task outlives your connection.

[Use the terminal clients](guide/terminals.html)

</div>
</div>

## Where to start

If you haven't installed it yet, [the install page](guide/install.html) has the release archives and the source build. [The first ten minutes](guide/first-run.html) gets a model connected and a first task running. After that, [the tour of the console](guide/console.html) shows what each part of the screen does.

The reference is built from the same repository: the [manual](reference/manual.html), the [protocol](reference/protocol.html), the [Scheme language reference](reference/scheme.html) and the [decision records](decisions/index.html). Read the decisions when you want to know why something works the way it does.

nodwyn is pre-1.0 and I use it every day, so expect rough edges. It isn't a sandbox either: commands you approve run with your user's privileges. [Your keys](guide/keys.html) says what is and isn't promised.
