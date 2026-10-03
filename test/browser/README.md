# Browser checks

`run.sh` drives the console in headless Chromium against synthetic worlds —
a scripted model (`demo/`), a fake MCP server, temporary directories — and
leaves each check's screenshots in `out/<check>/` and its output in
`out/<check>.log`. Nothing touches real sessions, keys or folders. The checks
run at the same time, and `run.sh` prints each one's output in order with how
long it took.

| Check | What it proves |
|---|---|
| `screens` | Home, a task with its approval, thinking shown and hidden, dark mode, phone width, Settings and Connections each show what they must, on the right background, with nothing sideways at phone width |
| `approve` | The edit goes ahead and Y answers each of the two commands' inline approvals, one answer each; the run they fold into says when one waits, then how many ran and for how long; the plan panel counts the steps marked; the reply renders as Markdown; a follow-up sends and is answered; a plan carries over a rate limit; two edits to the Makefile in one call ask once and write both; Home then has nothing waiting |
| `transcript` | Drawn from written events with the console's own module: reads and commands keep their order, commands in a row are one run that counts up while one runs, says when one waits for an answer and opens on a failure, and its clock stops when the transcript leaves the page |
| `firstrun` | `nodwyn app` with no config: model setup and keychain creation from the page, another model chosen on the saved server, a documents workspace, an attachment with a hidden secret, a versioned edit made and restored without asking; History stays open while follow-ups redraw the panel, and a kept-versions reply held back past a redraw still fills it; project notes being edited keep what was typed, and the focus, while a follow-up redraws the panel, save what was typed, and a cancelled draft is dropped |
| `remote` | `nodwyn ui --listen` with another host name: a phone pairs by a valid name, answers, and the log records `via web:phone`; launch tickets are refused remotely |
| `mcp` | Connections adds, reviews and approves an MCP server with the tools chosen; an undeclared tool asks every call without a standing answer; a read tool is gathered silently |
| `credentials` | A keychain created without a passphrase; Connections › Credentials stores a key allowed for one host, replaces it, connects an OAuth2 account through a fake provider and allows it with its scopes, removes the key; Settings sets and removes the passphrase; restarted, the keychain is open without unlocking; nothing sideways at phone width |
| `tasks` | Tasks sit under their folders, the latest active first, and a message moves its folder up; a folder shows five and Show more, folds and stays folded across a reload; a task renamed from its heading (Escape keeps the name), archived, brought back and deleted after a confirm, each still so after a reload; search reaches every folder; the list at phone width; a refresh of the list that a newer one overtakes waits for it, so a task just started is never "No such task" |

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
`demo-client`) and prints its endpoints, for Connect an account › custom. A contained MCP server added in Connections (the `IMAGE` it
prints, a network list, a folder such as `notes`) runs in a fake container
runtime, the demo run again: Pull, review and approve it, then ask a task in
`console` to "Note the weekly status" to see its confirm.
`-docs` also keeps a made-up Google token in a throwaway keychain, bound to a
fake Drive: ask a task in `console` to "Upload the status to Google Docs" to
see the question that sets how uploads are handled, then its effect in
Settings.
