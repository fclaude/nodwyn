# The first ten minutes

Start the console:

```sh
nodwyn app
```

With a window build you get a window. Otherwise your browser opens at an address that pairs itself with a one-time ticket, so there's nothing to copy.

## Connecting a model

There's no built-in model, so a new install asks for one before it lets you start a task. Home shows two forms, one after the other.

The first form is the provider. It offers Ollama, OpenAI, Anthropic, OpenRouter, Baseten, Cerebras and "another server", starting on Ollama, which needs no key and can run on this computer or another one on your network. A provider that wants a key can't be saved until there's a keychain to hold it, so the form shows the keychain form beside it. Pick a passphrase of at least eight characters and remember it, because nobody can recover it. You can also tick **Don't protect it with a passphrase**, which makes the keychain a file only your user can read. It's the weaker choice and Settings says so for as long as you keep it.

The second form is the model. nodwyn asks the provider what it has and offers those as buttons. **Save and use it** makes it the default for new tasks. The efforts it takes, what it's for and the rest wait behind a closed **More** fold, since none of it has to be answered before a task works.

![Settings, Models: the providers nodwyn talks to and the model new tasks start on](shot:settings-models)

Your API key goes into the keychain and never into `config.toml`. A binding row allows it only for that provider's host. A model with a key has to be reached over HTTPS unless it runs on this computer.

## A first task

Home asks "What should we get done?". Under the box, **Work in** lists your folders, and **Add a folder** points nodwyn at a new one. A task is pinned to its folder. It reads and writes inside it and nowhere else.

![Home: the ask box, the folders, and what needs you](shot:home)

If the folder is a git repository, **In a worktree** is ticked by default under **Options**. The task then works on a branch of its own, `nodwyn/<id>`, in a copy of the repository under `.nodwyn/worktrees`, so two tasks on one repository don't step on each other and your checkout stays as it was.

Type what you want and press Enter. Shift+Enter starts a new line. The task opens, the model starts working, and the sidebar groups what's happening: **Needs you** on top, then your folders with their tasks.

When the model wants to run something nothing has approved, the task stops and asks you. [Approvals](approvals.html) covers what you'll see then, and [the tour of the console](console.html) shows the rest of the screen.
