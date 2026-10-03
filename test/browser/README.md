# Browser checks

`run.sh` drives the console in headless Chromium against synthetic worlds —
a scripted model (`demo/`), a fake MCP server, temporary directories — and
leaves screenshots in `out/`. Nothing touches real sessions, keys or folders.

| Check | What it proves |
|---|---|
| `screens` | Home, a task with its approval, dark mode, phone width, Settings and Connections render without console errors |
| `approve` | The edit goes ahead and Y answers the command's inline approval; both steps show their outcomes; the reply renders as Markdown; a follow-up sends |
| `firstrun` | `nodwyn app` with no config: model setup and keychain creation from the page, a documents workspace, an attachment with a hidden secret, a versioned edit made and restored without asking |
| `remote` | `nodwyn ui --listen` with another host name: a phone pairs by name, answers, and the log records `via web:phone`; launch tickets are refused remotely |
| `mcp` | Connections adds, reviews and approves an MCP server; an undeclared tool asks every call without a standing answer; a read tool is gathered silently |
| `credentials` | A keychain created without a passphrase; Connections › Credentials stores a key allowed for one host, replaces it, connects an OAuth2 account through a fake provider and allows it with its scopes, removes the key; Settings sets and removes the passphrase; restarted, the keychain is open without unlocking; nothing sideways at phone width |

`go run ./test/browser/demo -seed` starts the same world by hand and prints a
launch URL; `-oauth-only` runs just a fake OAuth2 provider (client id
`demo-client`) and prints its endpoints, for Connect an account › custom. A contained MCP server added in Connections (the `IMAGE` it
prints, a network list, a folder such as `notes`) runs in a fake container
runtime, the demo run again: Pull, review and approve it, then ask a task in
`console` to "Note the weekly status" to see its confirm.
`-docs` also keeps a made-up Google token in a throwaway keychain, bound to a
fake Drive: ask a task in `console` to "Upload the status to Google Docs" to
see the question that sets how uploads are handled, then its effect in
Settings.
