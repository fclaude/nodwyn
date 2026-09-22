# Real session fixtures

`p7-selfcheck.jsonl` is the unmodified candidate selfcheck session
`20260922T162647Z-bb4c5d03`, recorded on 2026-09-22 against the live local Ollama
control plane with isolated temporary XDG config/data directories. It loaded
the dev tools, read README length, ran the log package tests via `go-test`,
and received READY from the model. It contains 11 events.

This is a real acceptance session, not a nibb-authored development session.
It preserves the recorded repository path, model metadata, and timing so replay
consumers have genuine provenance. No credentials were registered or supplied.
P7's seeded-canary coverage lives in `internal/core/redaction_test.go` and uses
the fake Ollama server; it is intentionally separate from this live evidence.
