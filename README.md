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
```

Requires Go 1.27 and `staticcheck` (`go install honnef.co/go/tools/cmd/staticcheck@latest`).
