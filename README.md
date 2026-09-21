# nibb

A minimal, self-extending coding-agent harness: a Go host, a small Scheme
embedded in it for orchestration, one guard at the only Scheme→Go exit, and
a binding table as the sole authority over where a secret may go.

- [SPEC.md](SPEC.md) — the architecture and its invariants
- [plan.md](plan.md) — the bootstrap-then-self-host build plan
- [docs/adr/](docs/adr/) — decision records
- [docs/scheme.md](docs/scheme.md) — the language reference (B1)

## Build

```
make build      # bin/nibb
make test       # go test -race ./...
make lint       # vet, staticcheck, boundary lint
make accept     # everything a candidate must pass before promotion
make smoke      # manual two-turn session against the real control plane
```

Requires Go 1.27 and `staticcheck` (`go install honnef.co/go/tools/cmd/staticcheck@latest`).

## Run

```
bin/nibb                      # chat in the current repository (plain front-end)
bin/nibb --repo ~/src/x       # pin to another repository
bin/nibb --show-thinking      # stream the model's thinking too
bin/nibb --no-confirm         # non-interactive: every confirm is a no
bin/nibb replay <session-id>  # print a session log as a transcript
bin/nibb sessions             # list sessions
bin/nibb repl                 # Scheme REPL
```

The control plane is the Ollama server in `~/.config/nibb/config.toml`
(default: Qwen on workshop over the tailnet; no key). Every write and every
command is confirmed by you, with a diff for writes. Session logs live in
`~/.local/share/nibb/sessions/`.

Bootstrap-era safety envelope (plan §3.3): paths are pinned to the
repository, credential-shaped files are unreadable, network tools on a
command line need confirmation, and child processes get a scrubbed
environment. Until Phase 7 lands, do not paste secrets into nibb and keep
it to this repository.
