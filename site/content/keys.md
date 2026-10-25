# Your keys

nodwyn exists because of one worry: an agent that can run commands and read files can also leak your API keys. This page says what the design does about it, and where it stops.

## The model never holds a key

Your keys live in an encrypted keychain, a file under `~/.config/nodwyn` that only your user can read. The model only ever gets an opaque handle to one. When a tool call needs a key, the host puts it into the request at the position you allowed, after the model has finished writing the call. The value never appears in what the model sees, in the task's log or in the events a client receives.

A key is allowed somewhere through a binding: this class of credential, to this host, with these methods, in this position of the request. `sk-ant-...` going to the provider it belongs to is right. The same bytes going anywhere else get refused, and the same goes for a request that carries a key but matches no binding. A key you haven't bound can't be used at all.

The guard sits at the one place Scheme can reach the host. Every file read or write, every command and every request goes through it, and what it refuses stays refused. A command with a flag that selects a program or an output file, or a path that leaves the folder, is asked about even if its program was approved. Child processes get a scrubbed environment, and files that look like credentials can't be read by the tools.

## What gets redacted

Text that looks like a credential is removed from your messages, tool results, logs and the events sent to clients. Replies from the model are buffered until they're complete, so a key split across stream chunks can't slip out. This recognises known shapes. A secret with no recognisable shape, or one that has been encoded, may not be caught, and there's no classifier for those yet.

## What you should still decide

nodwyn isn't a sandbox. A command you approve runs with your user's privileges, and builds and tests execute the project's code, write files and may use the network. The checks around an approval, the argument and path rules above, stop mistakes and leaks. A determined program would get past them.

So the choices that matter are yours:

- Approve a command for one call unless you know the program well, and be careful with the ones that run whatever they're handed (shells, interpreters, `ssh`).
- Keep **Don't ask** off, and turn it on only in a task you're watching.
- Review a project's `.nodwyn/init.scm` and `allow.toml` with `nodwyn trust` before they're read. Anyone who can commit to a repository can write them.
- Give the keychain a passphrase. A keychain without one is still encrypted, but its key comes from the empty passphrase, so it protects nothing the file's permissions don't.
- Run unattended tasks on a machine or in a container you can throw away.

[The security page of the reference](../reference/security.html) is the long version, and [SECURITY.md](https://github.com/fclaude/nodwyn/blob/main/SECURITY.md) says how to report a hole in any of this.
