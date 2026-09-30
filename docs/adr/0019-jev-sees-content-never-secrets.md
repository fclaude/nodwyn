# ADR 0019: Jev sees content, never secrets

- Status: accepted (amends SPEC "Jev — where it is allowed" and plan P5)
- Date: 2026-09-21

## Context
The spec said Jev "judges why, never what" and "never sees content". That
was over-rotated from the real constraint. Seam A asks "is this plan good
enough for the user's ask?" — a question that has no answer without the
ask and the plan, both of which are content. The concern behind the rule
was retention: Jev is an external API with no non-retention guarantee, so a
secret value that reaches it is a secret leaked. That concern is about
*secret values*, not about content in general.

The spec already resolved one category error of this shape (a taint on a
value cannot protect a text window). This is the same correction applied
to the judge.

## Decision

1. **Jev is an egress edge.** A call to Jev is model-context text leaving
   the machine, and it is guarded the way every other egress is: the Jev
   credential is a bound data-plane credential (E3), and the payload passes
   through the same redaction controller as the model context (E2/E4:
   L1, later L3, `[REDACTED class]`). No special sanitiser; the composition
   of two existing mechanisms.

2. **Secret values never reach Jev.** Unchanged: invariant 2 holds, and
   redaction is what enforces it. If redaction finds a known-shape secret
   the placeholder is sent; if L3 later flags a class-unknown residual above
   threshold, the call is confirmed or dropped, never sent raw.

3. **What Jev may see is a user policy, not an invariant.** `jev.content`
   in `config.toml`:
   - `none` — Jev disabled;
   - `plan` (default) — the user's ask and the draft plan text;
   - `plan+summaries` — plus the model's own summaries of tool results it
     cites in the plan (never raw results);
   - `full` — plus raw tool results and file contents referenced by the
     plan; opt-in per repository in `.nodwyn/allow.toml` (`jev = "full"`),
     because it is the level at which proprietary code is retained
     elsewhere.

4. **Every call is logged** as `jev.req` (new kind): content level, byte
   count, a hash of the payload, the redaction events it triggered, and the
   score that came back. The user can see exactly what left.

5. **Unchanged:** Seam C remains value-free by construction (class,
   destination, scope); Jev never sees the harness's own control-plane
   credential and is never audited for it; Jev never controls flow.

## Consequences
- Seam A becomes useful: it judges plans against asks, with real text.
- The P5 "payload sanitiser" is deleted from the plan; P5 depends on P7's
  redaction controller for the send path (or ships L1-only redaction for
  Jev first, which is the same function).
- The only content leaving the machine in v0 is what goes to Jev (the
  control plane is local). The knob and the log make that visible. *(No longer so once a remote model is configured: ADR 27.)*
- The spec's "why-not-what" line is restated as: Jev judges intent and
  plans over redacted content; the local stack judges provenance and
  secret shape, and is the only thing that ever sees a value.
