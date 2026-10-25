<p class="lede">nodwyn is a coding agent that keeps your keys out of the model's hands. It runs on your computer or on a server, and you work with it from a window, a browser, a phone or a terminal.</p>

![The console: a task waiting for an answer, with its plan beside it](shot:task)

The model writes the plan and asks to run commands. Your API keys sit in an encrypted keychain, and the model only holds a handle to one. A key can be used at the places you bound it to, and the host puts it into the request after the model has finished writing it. Everything that leaves Scheme for the host goes through one guard, and what the guard refuses stays refused.

This site is mostly about the console, the part you'll look at all day, and about the two ways to run it: on your own computer, and on a server that other devices and terminals connect to.

<div class="ways" markdown="1">
<div markdown="1">

### On your computer

`nodwyn app` opens a window with everything in it. Nothing to start first, nothing to pair.

[Run it standalone](guide/standalone.html)

</div>
<div markdown="1">

### On a server

One daemon that never sleeps, a console you open from a laptop or a phone, and terminals that attach to the same tasks.

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

The reference is the repository's own documentation, so it is as current as the code: the [manual](reference/manual.html), the [protocol](reference/protocol.html), the [Scheme language reference](reference/scheme.html) and the [decision records](decisions/index.html) that say why things are the way they are.

nodwyn is pre-1.0 and I use it every day, so expect rough edges. It isn't a sandbox either: commands you approve run with your user's privileges. [Your keys](guide/keys.html) says what is and isn't promised.
