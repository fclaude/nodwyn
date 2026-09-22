// Package plan implements the noul gate and Seam A score loop (ADR 19,
// Phase 5). The gate is the first thing that happens when a user message
// arrives: it asks Jev whether the message needs a plan; if so, it generates
// a plan (via the model), scores it (via Jev Seam A), revises up to N
// times, and logs the outcome as gate.plan. If Jev is unreachable the gate
// defaults to "plan" (hard trigger still enforced) and logs the outage.
//
// The gate drives jev-noul and jev-score through the Gate interface so every
// Jev call goes through the guard, the binding check, and the redaction
// controller just like a model tool call (plan §Phase 5).
package plan

import (
	"context"
	"fmt"

	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
)

// MaxRevises bounds the Seam A loop (ADR 19: ≤3).
const MaxRevises = 3

// ScoreThreshold is the minimum Jev score for a plan to be accepted.
const ScoreThreshold = 0.7

// Gate is what the plan gate needs from the core session.
type Gate interface {
	Record(kind log.Kind, payload any) (log.Event, error)
	// ApplyPrim evaluates the named primitive with the given Scheme values,
	// through the same apply→guard path a model tool call takes.
	ApplyPrim(ctx context.Context, name string, args []scheme.Value) (scheme.Value, error)
	// JevHandle returns the Jev credential handle (class "jev-key").
	JevHandle() *scheme.Handle
	// Model generates a plan for the given user message.
	Model(ctx context.Context, userMsg string) (string, error)
}

// Result is the gate's outcome for one user message.
type Result struct {
	Plan    string  // the final plan text (empty if no plan was needed)
	Score   float64 // the final Jev score (0 if no plan)
	Revises int     // how many Seam A revisions happened
	OK      bool    // true if the gate completed without error
	Error   string  // if !OK: the error
	JevOut  bool    // true if Jev was reachable
}

// Run executes the noul gate + Seam A loop for one user message.
func Run(ctx context.Context, g Gate, userMsg string) Result {
	h := g.JevHandle()
	if h == nil {
		// Jev is not configured: skip the gate entirely. The loop runs
		// unguarded (no plan, no score) — the hard-trigger checkers in the
		// guard still fire on every tool call.
		return Result{OK: true, JevOut: false}
	}
	hval := scheme.Value{Kind: scheme.KHandle, H: h}

	// Step 1: noul gate — does this message need a plan?
	noulVal, noulErr := g.ApplyPrim(ctx, "jev-noul",
		[]scheme.Value{hval, {Kind: scheme.KString, S: userMsg}})
	if noulErr != nil {
		// Jev unreachable: degrade to "plan", log the outage.
		_, _ = g.Record(log.KindNote, log.Note{Text: "jev-noul failed: " + noulErr.Error() + "; gate defaults to plan"})
		planText, planErr := g.Model(ctx, userMsg)
		if planErr != nil {
			return Result{OK: false, Error: planErr.Error(), JevOut: false}
		}
		_, _ = g.Record(log.KindGatePlan, log.GatePlan{
			Origin: "gate", Noul: "degraded", Plan: planText, Reason: noulErr.Error(),
		})
		return Result{Plan: planText, JevOut: false, OK: true}
	}

	if noulVal.Kind == scheme.KString && noulVal.S == "proceed" {
		_, _ = g.Record(log.KindGatePlan, log.GatePlan{Origin: "gate", Noul: "proceed"})
		return Result{OK: true, JevOut: true}
	}

	// Step 2: generate initial plan.
	planText, planErr := g.Model(ctx, userMsg)
	if planErr != nil {
		_, _ = g.Record(log.KindNote, log.Note{Text: "plan generation failed: " + planErr.Error()})
		return Result{OK: false, Error: planErr.Error(), JevOut: true}
	}

	bestPlan := planText
	bestScore := 0.0
	reviseCount := 0

	for i := 0; i <= MaxRevises; i++ {
		scoreVal, scoreErr := g.ApplyPrim(ctx, "jev-score",
			[]scheme.Value{hval,
				{Kind: scheme.KString, S: bestPlan},
				{Kind: scheme.KString, S: userMsg}})
		if scoreErr != nil {
			_, _ = g.Record(log.KindNote, log.Note{Text: fmt.Sprintf("jev-score failed at revision %d: %s", i, scoreErr.Error())})
			break
		}
		score := scoreVal.F
		if score > bestScore {
			bestScore = score
		}
		reviseCount = i
		if score >= ScoreThreshold {
			break
		}
		if i == MaxRevises {
			break
		}
		// Revise the best plan so far.
		cand, err := g.Model(ctx, userMsg+"\n\nYour previous plan scored "+fmt.Sprintf("%g", score)+" (threshold "+fmt.Sprintf("%g", ScoreThreshold)+"). Revise it to better address the ask.")
		if err != nil {
			_, _ = g.Record(log.KindNote, log.Note{Text: "plan revision failed: " + err.Error()})
			break
		}
		bestPlan = cand
	}

	_, _ = g.Record(log.KindGatePlan, log.GatePlan{
		Origin: "gate", Noul: "plan", Plan: bestPlan,
		Score: bestScore, Revisions: reviseCount,
	})
	return Result{Plan: bestPlan, Score: bestScore, Revises: reviseCount, OK: true, JevOut: true}
}
