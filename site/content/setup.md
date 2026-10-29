# Models, keys and folders

Four pages hold what nodwyn knows about the world: Settings for models and choices, Connections for keys and tools, Workspaces for folders, and Hosts for other nodwyns. This page goes through the first three.

## Settings

Settings has five tabs. A choice you make waits in a bar at the foot of the page and is saved with the others, except keys, the passphrase and starting at login, which act at once, and the options under This browser, which apply at once to this browser alone.

**General** has Approvals (see [Approvals](approvals.html)), the limits a task stops at, the context window, and **While you're away**: whether the daemon starts at login so scheduled tasks run with no window open, and whether it shows a desktop notice when a task needs you.

**Models** is where providers and their models live.

![Settings, Models](shot:settings-models)

A provider is an address and, if it needs one, a key. Each model has its own settings for reasoning effort, context length and price. **Test** tries a model before you save it, and **Add a model** names one the provider lists. The default for new tasks is chosen at the top. Everything is written to `config.toml` with the rest of the file kept as it was, and a key never goes there. A task picks its model, effort and judge as it opens, and can change them between turns.

**Judge** sets up the second model that answers "should this go ahead?", described under [Approvals](approvals.html#the-judge).

**Keys & devices** makes the keychain, sets or changes its passphrase, and lists the devices that have paired with this console. It's also where you invite a new one. [Running it on a server](server.html) uses it a lot.

![Settings, Keys & devices](shot:settings-devices)

**This browser** has the choices that belong to the browser: theme, whether the model's thinking is shown, and notifications for tasks that need you.

## Connections

Connections is for the keys nodwyn holds and the tools it's been given.

![Connections: the keys the keychain holds and where each may go](shot:connections)

**Credentials** lists what the keychain holds. For each key it shows the kind (an API key, or an OAuth2 account with its provider and scopes), what is set up to send it, and the **binding rows** that say where it may go. A key with no binding is marked, because nodwyn refuses any request that would carry it. You can add a key and the host it may go to, replace a key you've rotated (its bindings carry on), connect an OAuth2 account for Google, Microsoft, GitHub or Notion, and remove one. A value is never shown again once it's saved.

**MCP servers** are tools other programs provide. A remote server is reached over HTTPS with a key you've bound to it. A local one can run inside a container (podman or docker), with the network reachable only through the `nodwyn-egress` helper, and nodwyn pins what the server says it offers, so a server that changes its tools behind your back is refused until you look.

## Workspaces

A workspace is a folder nodwyn works in.

![Workspaces](shot:workspaces)

A task is pinned to its folder and to the folders mounted there, and reads and writes inside them and nowhere else. Home's **Work in** row and this page both list the folders you've used. Each has **New task here**, **Name it** for a friendlier name, and **Forget**, which drops its name and mounts from the list and leaves the folder and its history alone. If a project has its own startup files (`.nodwyn/init.scm` and `allow.toml`), the page says whether they're trusted. They aren't read until someone reviews them with `nodwyn trust` on the computer nodwyn runs on, or from here on a device signed in with a passkey.
