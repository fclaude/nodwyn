# ADR 0011: Terminal stack

- Status: accepted
- Date: 2026-09-20

## Context
SPEC names "bubbletea + crossterm". crossterm is a Rust crate.

## Decision
bubbletea + bubbles + lipgloss, confined to `internal/frontend/tui`. bubbletea
owns the terminal in Go; nothing is missing.

## Consequences
- The boundary lint (ADR 10) keeps charmbracelet packages out of the core.
