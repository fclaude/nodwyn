# Browser checks

`run.sh` drives the console in headless Chromium against synthetic worlds —
a scripted model (`demo/`), a fake MCP server, temporary directories — and
leaves each check's screenshots in `out/<check>/` and its output in
`out/<check>.log`. Nothing touches real sessions, keys or folders. The checks
run at the same time, and `run.sh` prints each one's output in order with how
long it took.

| Check | What it proves |
|---|---|
| `screens` | Home, a task with its approval, thinking shown and hidden, dark mode, phone width, Settings and Connections each show what they must, on the right background, with nothing sideways at phone width; Home's model/effort picker stays a slim paired control with a divider, contained dropdowns and visible keyboard focus in light/dark desktop and phone views |
| `approve` | The edit goes ahead and Y answers each of the two commands' inline approvals, one answer each; the run they fold into says when one waits, then how many ran and for how long; the plan panel counts the steps marked, each folded to its title with its mark's note behind the fold; the reply renders as Markdown; a follow-up sends and is answered; a plan carries over a rate limit; two edits to the Makefile in one call ask once and write both; Home then has nothing waiting |
| `transcript` | Drawn from written events with the console's own module: reads and commands keep their order, commands in a row are one run that counts up while one runs, says when one waits for an answer and opens on a failure, and its clock stops when the transcript leaves the page |
| `firstrun` | `nodwyn app` with no config: model setup with the forms of Settings › Models (the presets, started on Ollama, which asks for no key, Anthropic filling in its address and asking for a key and a keychain; a provider saved, its models offered, one saved as the default) and keychain creation from Settings, another model chosen on the saved server, a documents workspace, an attachment with a hidden secret, a versioned edit made and restored without asking; History stays open while follow-ups redraw the panel, and a kept-versions reply held back past a redraw still fills it; project notes being edited keep what was typed, and the focus, while a follow-up redraws the panel, save what was typed, and a cancelled draft is dropped |
| `firstmodel` | `nodwyn app` with no config, on a fake Anthropic API that wants a key: the provider form has the key field and, beside it, a keychain form, and cannot be saved until the keychain is made, which keeps what was typed; the provider is saved with its key in the keychain and two `x-api-key` binding rows and never in `config.toml` or the page; another provider can be started and cancelled; the model form offers the models the provider lists to its key, tests a model without saving it, and saves it as the default at the effort chosen (`settings.model.write`, then `settings.control.write`); New task then starts on it at that effort |
| `lastfolder` | Home and Workspaces picks survive reload and app restart; Forget cancels without a request, forgets unnamed and named folders persistently without deleting files or task history, clears the last pick, and re-adding clears the forgotten marker |
| `remote` | `nodwyn ui --listen` with another host name: a phone pairs by a valid name, answers, and the log records `via web:phone`; launch tickets are refused remotely, forgetting folders is not offered, and a folder forgotten on the computer is chosen by path from the phone and comes back once its task starts |
| `mcp` | Connections adds, reviews and approves an MCP server with the tools chosen; an undeclared tool asks every call without a standing answer; a read tool is gathered silently |
| `credentials` | A keychain created without a passphrase; Connections › Credentials stores a key allowed for one host, replaces it, connects an OAuth2 account through a fake provider and allows it with its scopes, removes the key; Settings sets and removes the passphrase; a protected restart keeps early password typing and focus through the final startup reply, the New task shortcut and a failed unlock, then unlocks without chat text; restarted without a passphrase, the keychain is open without unlocking; nothing sideways at phone width |
| `tasks` | Tasks sit under their folders, the latest active first, and a message moves its folder up; a folder shows five and Show more, folds and stays folded across a reload; a task renamed from its heading (Escape keeps the name), archived, brought back and deleted after a confirm, each still so after a reload; search reaches every folder; the list at phone width; a refresh of the list that a newer one overtakes waits for it, so a task just started is never "No such task" |
| `worktrees` | Two tasks started from the ask bar with **In a worktree** go through `worktree.open`, each writes in a git worktree of its own and not in the repository, both are listed under the repository, which Home offers once, and the heading, conversation and Workspace panel name the branch; switching between open tasks sends no `session.subscribe` or `log.read`, keeps the conversation as drawn, shows a turn another window started while the task was off screen, and clears a key being typed; a closed task is read from its log again, and its worktree, with work in it, stays |
| `attach` | An image attached in the New task window shows as a chip at once and stays drawn, with the attach input working, through a redraw of Home that keeps the composer and through a full redraw; a second file's chip can be removed before the task starts, and starting then carries the kept image to its confirm on the task's page, which the model counts once allowed, while the removed file stays home |
| `models` | Settings › Models from a config that names one inline Ollama model: a provider added with a wrong key, a test request that says the server did not accept it, the key replaced in place and the test passing, with the key in the keychain and one binding row and never in `config.toml` or the page; two models added, one with efforts, and the default chosen with an effort while the previous default stays in the list; the New task window offering each model's own efforts (none for a model that takes none), a choice kept across Settings and a reload, starting a task on it with the provider seeing that model and effort; a conversation's effort changed between messages, an unchanged running response showing no change notice, and one chosen while a response is held waiting for it, the page saying so and the provider seeing the old effort, then the new; the selection after a reload; a model removed from the list said to be gone, and a closed task whose model was removed asking nothing until one is chosen; an Anthropic provider added from its preset with no dialect, its key bound as `x-api-key` and not as a bearer token, its model added with the prompt cache's ask already chosen and offered no seed, reply cap field or session key, and a test of it sent to the Messages API with its effort as adaptive thinking; the Baseten preset filling in its address; Anthropic's and Baseten's models showing the context, reply cap, efforts and prices the catalogue publishes; the default model, a Claude route that caches only when asked, saved with Prompt cache and then sent the ask on every request, the fake provider reading the prompt back from its cache and the transcript saying so; after an app restart the locked keychain said, and the effort restored with the resumed task; a provider that stops answering said so, its task failing there and nothing asked of the other model; nothing sideways at phone width. `remote` adds that a paired phone is offered no picker and cannot change a task's model |

What a check asserts is what each action did: the JSON-RPC requests the page
sent over its socket (`requests` in `lib.js`), the files the daemon wrote, and
what the page shows after. Finding a control is never enough, since a control
that does nothing is still found. The screenshots are for people to look at:
nothing compares them with anything.

A check fails on any error a page throws or logs to its console. An error it
provokes on purpose, such as a request the console must refuse, it names with
`expectError` first, once for each time it provokes it. Warnings are printed
and do not fail it.

A check waits for conditions, never for a time. The console marks what the
checks wait for: `<html data-ready>` once the desk's first refresh is drawn
and the models have answered, and `.task[data-loaded]` once a task's
conversation, details and notes are. `demo -seed` prints its URL only once
its tasks have settled (one finished, one waiting for an answer), and the
checks pass `-pace=0`, so the scripted model answers at once.

`lib.js` is what the checks share. Whatever ends a check, it stops the
programs and browsers it started and removes its temporary folders, which are
under `TMPDIR` (a daemon's socket folder falls back to `/tmp` when `TMPDIR` is
too long for a socket's path). A check can be run alone, as
`DEMO=out/demo NODWYN=out/nodwyn node checks/<check>.js out/<check>` from
this folder, after `run.sh` has built the two programs.

`go run ./test/browser/demo -seed` starts the same world by hand and prints a
launch URL; its model waits 400 ms before each reply, so the work shows step
by step (`-pace` changes that). `-oauth-only` runs just a fake OAuth2 provider (client id
`demo-client`) and prints its endpoints, for Connect an account › custom.
`-chat-only` runs just a fake chat-completions provider (`test/fakes/chat`)
and prints its address and key: it lists a model that takes an effort and one
that does not, refuses a wrong key, a model it lacks and an effort a model
does not take, and shows what it was sent at `/_fake/requests`, for Settings ›
Models and the model picker. A contained MCP server added in Connections (the `IMAGE` it
prints, a network list, a folder such as `notes`) runs in a fake container
runtime, the demo run again: Pull, review and approve it, then ask a task in
`console` to "Note the weekly status" to see its confirm.
`-docs` also keeps a made-up Google token in a throwaway keychain, bound to a
fake Drive: ask a task in `console` to "Upload the status to Google Docs" to
see the question that sets how uploads are handled, then its effect in
Settings.
