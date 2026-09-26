// Package plan implements the noul gate and the Seam A score loop (SPEC
// "plan trigger", ADR 19, Phase 5). When a user message arrives the gate
// asks Jev whether it needs a plan; if so the model drafts one, Jev scores
// it against the ask on a fixed rubric, and the model revises up to
// MaxRevises times. The outcome is logged as gate.plan, and the plan is
// picked up from the log by the window's task block (ADR 9), so the turn
// that follows runs with the plan in front of the model.
//
// Every Jev call goes through the guard like a tool call (jev-noul,
// jev-score are primitives): binding check, L1, redaction, jev.req. If Jev
// is unreachable the gate degrades to "plan" and logs the outage.
package plan

import (
	"context"
	"fmt"
	"strings"

	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
)

// MaxRevises bounds the Seam A loop (SPEC: at most 3 rounds).
const MaxRevises = 3

// NoulThreshold is the noul probability above which a message needs a plan.
const NoulThreshold = 0.5

// ScoreThreshold is the normalised Seam A score at which a plan is accepted.
const ScoreThreshold = 0.7

// NoulInstructions is the gate's yes/no question about the user's message.
const NoulInstructions = "Does carrying out this request take several distinct steps, touch more than one file or system, or have consequences that are hard to undo? Answer yes if a short plan would make the work safer or better; no if it is a single, obvious action or a question."

// ScoreInstructions and ScoreLevels are the Seam A rubric.
const ScoreInstructions = "The text is a plan written for the request quoted at its top. Rate how well the plan fulfils the request: complete, concrete, in a sensible order, with nothing unnecessary or unsafe."

// ScoreLevels are the rubric's levels, worst first.
var ScoreLevels = []string{
	"misses or contradicts the request",
	"addresses part of the request; major steps missing or wrong",
	"covers the request but vaguely, out of order, or with unnecessary steps",
	"complete and concrete with minor gaps",
	"complete, concrete, minimal and safe",
}

// Gate is what the gate needs from the core session.
type Gate interface {
	Record(kind log.Kind, payload any) (log.Event, error)
	// ApplyPrim evaluates the named primitive through apply->guard, with
	// harness origin.
	ApplyPrim(ctx context.Context, name string, args []scheme.Value) (scheme.Value, error)
	// JevHandle returns the jev-key credential handle, or nil.
	JevHandle() *scheme.Handle
	// Draft asks the control-plane model to write or revise a plan.
	Draft(ctx context.Context, prompt string) (string, error)
}

// Result is the gate's outcome for one user message.
type Result struct {
	Plan    string  // final plan text; empty when no plan was needed
	Score   float64 // final normalised score (0 without a plan)
	Revises int     // revision rounds performed
	OK      bool
	Error   string
	JevOut  bool // Jev answered
}

// Run executes the gate for one user message.
func Run(ctx context.Context, g Gate, userMsg string) Result {
	return run(ctx, g, userMsg, false)
}

// Required uses the same score/revise path, but always drafts a plan. The host
// must refuse execution when the result is not OK; an empty plan is a failure.
func Required(ctx context.Context, g Gate, userMsg string) Result {
	result := run(ctx, g, userMsg, true)
	if result.OK && strings.TrimSpace(result.Plan) == "" {
		result.OK, result.Error = false, "planner returned an empty plan"
	}
	return result
}

func run(ctx context.Context, g Gate, userMsg string, required bool) Result {
	h := g.JevHandle()
	if h == nil {
		if !required {
			return Result{OK: true}
		}
		text, err := g.Draft(ctx, planPrompt(userMsg))
		if err != nil {
			return Result{Error: err.Error()}
		}
		if _, err := g.Record(log.KindGatePlan, log.GatePlan{Origin: "gate", Noul: "required", Plan: text}); err != nil {
			return Result{Error: err.Error()}
		}
		return Result{OK: true, Plan: text}
	}
	hval := scheme.HandleValue(h)

	jevOut := false
	if !required {
		noul, err := g.ApplyPrim(ctx, "jev-noul", []scheme.Value{hval, scheme.Str(userMsg), scheme.Str(NoulInstructions)})
		if err != nil {
			_, _ = g.Record(log.KindNote, log.Note{Text: "jev-noul failed: " + err.Error() + "; gate defaults to plan"})
			plan, perr := g.Draft(ctx, planPrompt(userMsg))
			if perr != nil {
				return Result{OK: false, Error: perr.Error()}
			}
			_, _ = g.Record(log.KindGatePlan, log.GatePlan{Origin: "gate", Noul: "degraded", Plan: plan, Reason: err.Error()})
			return Result{Plan: plan, OK: true}
		}
		p := noul.AsFloat()
		if p < NoulThreshold {
			_, _ = g.Record(log.KindGatePlan, log.GatePlan{Origin: "gate", Noul: "proceed", Score: p})
			return Result{OK: true, JevOut: true}
		}

		jevOut = true
	}

	plan, err := g.Draft(ctx, planPrompt(userMsg))
	if err != nil {
		_, _ = g.Record(log.KindNote, log.Note{Text: "plan generation failed: " + err.Error()})
		return Result{OK: false, Error: err.Error(), JevOut: jevOut}
	}
	levels := make([]scheme.Value, len(ScoreLevels))
	for i, l := range ScoreLevels {
		levels[i] = scheme.Str(l)
	}
	best, bestScore, revises := plan, 0.0, 0
	for round := 0; ; round++ {
		state := "Request:\n" + userMsg + "\n\nPlan:\n" + plan
		ans, err := g.ApplyPrim(ctx, "jev-score", []scheme.Value{hval, scheme.Str(state), scheme.Str(ScoreInstructions), scheme.List(levels...)})
		if err != nil {
			_, _ = g.Record(log.KindNote, log.Note{Text: fmt.Sprintf("jev-score failed at round %d: %v", round, err)})
			break
		}
		jevOut = true
		score := scoreOf(ans)
		if score > bestScore {
			best, bestScore = plan, score
		}
		if score >= ScoreThreshold || round == MaxRevises {
			break
		}
		revised, err := g.Draft(ctx, revisePrompt(userMsg, plan, score))
		if err != nil {
			_, _ = g.Record(log.KindNote, log.Note{Text: "plan revision failed: " + err.Error()})
			break
		}
		plan = revised
		revises++
	}
	_, recordErr := g.Record(log.KindGatePlan, log.GatePlan{Origin: "gate", Noul: "plan", Plan: best, Score: bestScore, Revisions: revises})
	if required && recordErr != nil {
		return Result{Error: recordErr.Error(), JevOut: jevOut}
	}
	return Result{Plan: best, Score: bestScore, Revises: revises, OK: true, JevOut: jevOut}
}

// scoreOf reads the normalised score from a jev-score result alist.
func scoreOf(v scheme.Value) float64 {
	items, _ := v.Slice()
	for _, it := range items {
		if it.Kind == scheme.KPair && it.P.Car.Kind == scheme.KString && it.P.Car.S == "score" {
			return it.P.Cdr.AsFloat()
		}
	}
	return 0
}

func planPrompt(ask string) string {
	return "Write a short, concrete plan for the following request: numbered steps, each naming the file or command involved, nothing else.\n\nRequest:\n" + ask
}

func revisePrompt(ask, plan string, score float64) string {
	return fmt.Sprintf("Your plan for the request below scored %.2f out of 1 for completeness, concreteness and order. Revise it: fix what is missing or vague, remove what is unnecessary. Return only the revised numbered plan.\n\nRequest:\n%s\n\nPrevious plan:\n%s", score, ask, plan)
}
