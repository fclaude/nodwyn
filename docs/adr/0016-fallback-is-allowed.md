# ADR 0016: Fallback is allowed and honest

- Status: accepted
- Date: 2026-09-20

## Context
Self-hosting is a means, not the goal. A 27B local model will sometimes be
the wrong tool for a task.

## Decision
If a task is faster by hand or with another agent, do it that way and note
it in friction.md. The measure of success is the trend in friction.md, not
100% self-authorship.

## Consequences
- Nobody wastes a day forcing nodwyn through a task it cannot do yet.
- If the evidence says the model is the bottleneck on hard phases, switching
  the control plane (ADR 8) is a config decision made on that evidence.
