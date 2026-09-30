# Browser checks

`run.sh` drives the console in headless Chromium against synthetic worlds —
a scripted model (`demo/`), a fake MCP server, temporary directories — and
leaves screenshots in `out/`. Nothing touches real sessions, keys or folders.

| Check | What it proves |
|---|---|
| `screens` | Home, a task with its approval, dark mode, phone width, Settings and Connections render without console errors |
| `approve` | Y answers the inline approval; the command and edit steps show their outcomes; the reply renders as Markdown; a follow-up sends |
| `firstrun` | `nibb app` with no config: model setup and keychain creation from the page, a documents workspace, an attachment with a hidden secret, a versioned edit restored through its approval |
| `remote` | `nibb ui --listen` with another host name: a phone pairs by name, answers, and the log records `via web:phone`; launch tickets are refused remotely |
| `mcp` | Connections adds, reviews and approves an MCP server; an undeclared tool asks every call without a standing answer; a read tool is gathered silently |

`go run ./test/browser/demo -seed` starts the same world by hand and prints a
launch URL.
