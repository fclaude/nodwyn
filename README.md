# nibb

A minimal, self-extending coding-agent harness: a Go host, a small Scheme
embedded in it for orchestration, one guard at the only Scheme→Go exit, and
a binding table as the sole authority over where a secret may go.

- [SPEC.md](SPEC.md) — the architecture and its invariants
- [plan.md](plan.md) — the bootstrap-then-self-host build plan
- [docs/manual.md](docs/manual.md) — how to read the code and how to use nibb
- [docs/adr/](docs/adr/) — decision records
- [docs/scheme.md](docs/scheme.md) — the language reference
- [docs/friction.md](docs/friction.md) — where nibb got in the way of building nibb

## Maintainer map

The packages follow responsibilities that remain useful as nibb gains more
integrations and front-ends:

| Packages | Responsibility |
|---|---|
| `core` | Own a session, assemble dependencies, coordinate its lifetime and prompts. |
| `loop`, `plan` | Run one model/tool turn; decide whether to plan and score the plan. |
| `event`, `log` | Describe front-end events; persist the history from which context and replay are derived. |
| `keychain`, `binding` | Hold encrypted credentials and inject them; authorize their destinations, methods and positions. |
| `scheme`, `guard`, `prim` | Evaluate extensions; authorize host operations; implement those operations. |
| `llm`, `window`, `redact`, `detect/l1` | Talk to the model; derive its context; redact text; detect credential shapes. |
| `frontend/plain`, `config`, `cmd/nibb` | Render and answer events; load settings; wire the application together. |

**A user message:** the front-end calls `Session.Send`. The session records
and redacts the message, runs the plan gate, and starts one `loop.Turn`.
The loop builds its window from recorded events, calls the model, and applies
each requested tool through Scheme. Results are recorded before they enter
the next model request. The front-end consumes events from the session.

**A credential-bearing tool call:** Scheme applies a primitive; the guard
reads its metadata and arguments and checks the binding and other policies.
Only an allowed call reaches the handler. The handler builds the request and
asks the shared credential service to inject the opaque handle's secret at
the authorized position. Secret values stay out of Scheme and the log. Jev
uses this same path, including when called by the plan gate. The harness's
control-plane model call is the deliberate exception described in the spec.

**Adding a built-in integration:** declare its settings in `config`, then
register its handlers in `prim` with explicit egress/irreversible metadata
and credential, destination and payload roles. Supply its settings during
core construction and use host interfaces for credential injection,
transport, redaction and recording, as Jev does. Do not open the keychain or
read secrets in the integration. Test it through Scheme and the guard with
a local fake service, bound/unbound destinations and canary credentials.
Integration-specific settings should not become getters on `Session`.

The roadmap extends these boundaries: ADR 21 uses events for RPC clients;
ADRs 22–23 bring MCP tools through the primitive/guard path; ADR 24 extends
the credential lifecycle to OAuth. These are planned features, not claims
that RPC, MCP or OAuth are already implemented. Keep `loop`, `plan`, and
`event` separate, and keep credential management shared across integrations.

## Build

```
make build      # bin/nibb
make test       # go test -race ./...
make lint       # vet, staticcheck, boundary lint
make accept     # everything a candidate must pass before promotion
make smoke      # manual two-turn session against the real control plane
make selfcheck  # the candidate proves itself (ADR 13)
make promote TAG=v0.N-phase   # accept + selfcheck, then ~/.local/bin/nibb-stable
```

Requires Go 1.27 and `staticcheck` (`go install honnef.co/go/tools/cmd/staticcheck@latest`).

## Run

```
bin/nibb                      # chat in the current repository (plain front-end)
bin/nibb --repo ~/src/x       # pin to another repository
bin/nibb --show-thinking      # show the model's redacted thinking too
bin/nibb --no-confirm         # non-interactive: every confirm is a no
bin/nibb replay <session-id>  # print a session log as a transcript
bin/nibb sessions             # list sessions
bin/nibb repl                 # Scheme REPL
```

The control plane is the Ollama server in `~/.config/nibb/config.toml`
(default: Qwen on workshop over the tailnet; no key). Every write and every
command is confirmed by you, with a diff for writes and edits; standing
approvals for read-only commands live in `.nibb/allow.toml` (ADR 17).
Session logs live in `~/.local/share/nibb/sessions/`.

Bootstrap-era safety envelope (plan §3.3): paths are pinned to the
repository, credential-shaped files are unreadable, network tools on a
command line need confirmation, and child processes get a scrubbed
environment. P7 redacts recognized credential shapes from user messages,
tool results, logs, and frontend events. Responses are buffered until complete
so split stream chunks cannot expose a key. Unknown or encoded secrets are
not guaranteed to be detected; L3 remains P8. The old operator restrictions
remain in force for older stable binaries until P7 promotion.
