# Real session fixtures

`p7-selfcheck.jsonl` is the unmodified candidate selfcheck session
`20260922T162647Z-bb4c5d03`, recorded on 2026-09-22 against the live local Ollama
control plane with isolated temporary XDG config/data directories. It loaded
the dev tools, read README length, ran the log package tests via `go-test`,
and received READY from the model. It contains 11 events.

This is a real acceptance session, not a nodwyn-authored development session.
It preserves the recorded repository path, model metadata, and timing so replay
consumers have genuine provenance. No credentials were registered or supplied.
P7's seeded-canary coverage lives in `internal/core/redaction_test.go` and uses
the fake Ollama server; it is intentionally separate from this live evidence.


`p10-window-v1-selfcheck.jsonl` is the unmodified v0.27-window-replay promotion
selfcheck, session `20260923T015153Z-56545323`, against the live local control
plane. Its recipe and request digest were independently verified by the installed
binary. It provides real `window-v1` compatibility evidence as summary-aware
windows are added. Like the earlier fixture, this is an acceptance session,
not evidence of nodwyn-authored development; no credentials were supplied.

The recordings predate the rename to nodwyn (ADR 31) and keep the name
nibb where it appears: they are replayed byte for byte.
