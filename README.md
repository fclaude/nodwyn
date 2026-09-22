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
