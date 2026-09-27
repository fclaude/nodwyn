// Tests for the noul gate + Seam A score loop (plan §Phase 5).
//
// A fake Gate drives jev-noul / jev-score through scripted outcomes and
// captures the Model calls and log events, so we can assert on the revision
// bound, the degradation path, and the gate.plan event without a real Jev.
package plan

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
)

// fakeGate implements Gate with scripted Jev responses and a counting model.
type fakeGate struct {
	t           *testing.T
	handle      *scheme.Handle
	noulOuts    []float64 // scripted jev-noul probabilities
	scoreOuts   []float64 // scripted normalised jev-score values
	noulErr     error
	scoreErr    error
	models      []string // generated plan texts (one per Model call)
	logKinds    []log.Kind
	logPayloads []any
}

func (f *fakeGate) Record(kind log.Kind, payload any) (log.Event, error) {
	f.logKinds = append(f.logKinds, kind)
	f.logPayloads = append(f.logPayloads, payload)
	return log.Event{Kind: kind}, nil
}

func (f *fakeGate) ApplyPrim(ctx context.Context, name string, args []scheme.Value) (scheme.Value, error) {
	switch name {
	case "jev-noul":
		if f.noulErr != nil {
			return scheme.Value{}, f.noulErr
		}
		if len(f.noulOuts) == 0 {
			return scheme.Value{}, fmt.Errorf("no scripted noul")
		}
		out := f.noulOuts[0]
		f.noulOuts = f.noulOuts[1:]
		return scheme.Float(out), nil
	case "jev-score":
		if f.scoreErr != nil {
			return scheme.Value{}, f.scoreErr
		}
		if len(f.scoreOuts) == 0 {
			return scheme.Value{}, fmt.Errorf("no scripted score")
		}
		out := f.scoreOuts[0]
		f.scoreOuts = f.scoreOuts[1:]
		if len(args) != 4 || args[3].Kind != scheme.KPair {
			return scheme.Value{}, fmt.Errorf("jev-score: expected (cred state instructions levels)")
		}
		return scheme.List(scheme.Cons(scheme.Str("score"), scheme.Float(out)), scheme.Cons(scheme.Str("raw"), scheme.Float(out*4))), nil
	}
	return scheme.Value{}, fmt.Errorf("unexpected prim %q", name)
}

func (f *fakeGate) JevHandle() *scheme.Handle { return f.handle }

func (f *fakeGate) PlanPrompts() Prompts {
	return Prompts{
		Draft:       func(request string) (string, error) { return "plan for: " + request, nil },
		Revise:      func(request, plan string, _ float64) (string, error) { return "revise: " + plan, nil },
		Needed:      "needs a plan?",
		ScoreRubric: "score the plan",
		ScoreLevels: []string{"bad", "partial", "vague", "good", "excellent"},
	}
}

func (f *fakeGate) Draft(ctx context.Context, prompt string) (string, error) {
	if len(f.models) == 0 {
		return "fallback-plan", nil
	}
	out := f.models[0]
	f.models = f.models[1:]
	return out, nil
}

func gatePlan(f *fakeGate) *log.GatePlan {
	for i := len(f.logKinds) - 1; i >= 0; i-- {
		if f.logKinds[i] == log.KindGatePlan {
			if gp, ok := f.logPayloads[i].(*log.GatePlan); ok {
				return gp
			}
			if gp, ok := f.logPayloads[i].(log.GatePlan); ok {
				return &gp
			}
		}
	}
	return nil
}

func TestNoJevHandleSkipsGate(t *testing.T) {
	f := &fakeGate{t: t, handle: nil}
	res := Run(context.Background(), f, "do a thing")
	if !res.OK {
		t.Fatalf("gate should be OK without Jev, got %v", res.Error)
	}
	if res.Plan != "" {
		t.Fatalf("no plan expected without Jev, got %q", res.Plan)
	}
	if len(f.logKinds) != 0 {
		t.Fatalf("no log expected without Jev, got %d", len(f.logKinds))
	}
}

func TestNoulProceedSkipsPlan(t *testing.T) {
	f := &fakeGate{t: t, handle: &scheme.Handle{Class: "jev-key", ID: "x"}, noulOuts: []float64{0.2}}
	res := Run(context.Background(), f, "trivial")
	if !res.OK || !res.JevOut {
		t.Fatalf("want OK+JevOut, got %+v", res)
	}
	if res.Plan != "" {
		t.Fatalf("proceed should not generate a plan, got %q", res.Plan)
	}
	gp := gatePlan(f)
	if gp == nil || gp.Noul != "proceed" {
		t.Fatalf("want gate.plan noul=proceed, got %+v", gp)
	}
}

func TestPlanScoredAboveThreshold(t *testing.T) {
	f := &fakeGate{t: t, handle: &scheme.Handle{Class: "jev-key", ID: "x"},
		noulOuts:  []float64{0.9},
		scoreOuts: []float64{0.9},
		models:    []string{"the plan"},
	}
	res := Run(context.Background(), f, "build it")
	if !res.OK {
		t.Fatalf("want OK, got %v", res.Error)
	}
	if res.Plan != "the plan" {
		t.Fatalf("want the plan, got %q", res.Plan)
	}
	if res.Score != 0.9 {
		t.Fatalf("want score 0.9, got %v", res.Score)
	}
	if res.Revises != 0 {
		t.Fatalf("0 revisions expected (first score above threshold), got %d", res.Revises)
	}
}

func TestPlanRevisesToThreshold(t *testing.T) {
	// First two scores below threshold, third above: 2 revisions used.
	f := &fakeGate{t: t, handle: &scheme.Handle{Class: "jev-key", ID: "x"},
		noulOuts:  []float64{0.9},
		scoreOuts: []float64{0.2, 0.5, 0.85},
		models:    []string{"v1", "v2", "v3"},
	}
	res := Run(context.Background(), f, "hard task")
	if !res.OK {
		t.Fatalf("want OK, got %v", res.Error)
	}
	if res.Score != 0.85 {
		t.Fatalf("want final score 0.85, got %v", res.Score)
	}
	if res.Revises != 2 {
		t.Fatalf("want 2 revisions, got %d", res.Revises)
	}
	if res.Plan != "v3" {
		t.Fatalf("want best plan v3, got %q", res.Plan)
	}
}

func TestRevisionBound(t *testing.T) {
	// All scores below threshold: the loop must stop after MaxRevises
	// revisions and log the outcome (not run forever).
	scores := make([]float64, MaxRevises+1)
	for i := range scores {
		scores[i] = 0.1
	}
	models := make([]string, MaxRevises+1)
	for i := range models {
		models[i] = fmt.Sprintf("v%d", i)
	}
	f := &fakeGate{t: t, handle: &scheme.Handle{Class: "jev-key", ID: "x"},
		noulOuts:  []float64{0.9},
		scoreOuts: scores,
		models:    models,
	}
	res := Run(context.Background(), f, "impossible")
	if !res.OK {
		t.Fatalf("want OK, got %v", res.Error)
	}
	if res.Revises > MaxRevises {
		t.Fatalf("revisions %d exceed bound %d", res.Revises, MaxRevises)
	}
	if gatePlan(f) == nil {
		t.Fatal("want a gate.plan event")
	}
}

func TestJevOutageDegradesToPlan(t *testing.T) {
	// jev-noul unreachable: the gate logs an outage, defaults to a plan,
	// and the hard trigger is still enforced (the loop proceeds via the
	// guard, not here — we assert the degradation result + log).
	f := &fakeGate{t: t, handle: &scheme.Handle{Class: "jev-key", ID: "x"},
		noulErr: fmt.Errorf("connection refused"),
		models:  []string{"degraded plan"},
	}
	res := Run(context.Background(), f, "do it")
	if !res.OK {
		t.Fatalf("want OK (degraded), got %v", res.Error)
	}
	if res.JevOut {
		t.Fatal("Jev should be marked unreachable")
	}
	if res.Plan != "degraded plan" {
		t.Fatalf("want degraded plan, got %q", res.Plan)
	}
	gp := gatePlan(f)
	if gp == nil || gp.Noul != "degraded" {
		t.Fatalf("want gate.plan noul=degraded, got %+v", gp)
	}
}

// A driver must not be able to bypass planning through noul or a missing judge.
func TestRequiredPlan(t *testing.T) {
	for _, judge := range []bool{false, true} {
		t.Run(fmt.Sprint(judge), func(t *testing.T) {
			f := &fakeGate{t: t, models: []string{"required plan"}, noulOuts: []float64{0}, scoreOuts: []float64{0.9}}
			if judge {
				f.handle = &scheme.Handle{Class: "jev-key", ID: "x"}
			}
			result := Required(context.Background(), f, "work")
			if !result.OK || result.Plan != "required plan" || gatePlan(f) == nil {
				t.Fatalf("%+v", result)
			}
			if len(f.noulOuts) != 1 {
				t.Fatal("required planning consulted noul")
			}
		})
	}
	f := &fakeGate{t: t, models: []string{" \n ", "", "\n"}}
	if result := Required(context.Background(), f, "work"); result.OK {
		t.Fatal("accepted empty plan")
	}
}

// An empty draft is asked for again: a model sometimes answers with thinking
// only, and one empty sample must not end a run that requires a plan.
func TestEmptyDraftIsAskedAgain(t *testing.T) {
	f := &fakeGate{t: t, models: []string{"", "1. read\n2. fix"}}
	result := Required(context.Background(), f, "work")
	if !result.OK || result.Plan != "1. read\n2. fix" {
		t.Fatalf("%+v", result)
	}
	notes := 0
	for i, k := range f.logKinds {
		if k == log.KindNote && strings.Contains(fmt.Sprint(f.logPayloads[i]), "empty plan, asking again (1/2)") {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("expected one retry note, got %d: %v", notes, f.logPayloads)
	}
	// A revision that comes back empty keeps the best plan.
	f = &fakeGate{t: t, handle: &scheme.Handle{Class: "jev-key", ID: "x"}, models: []string{"first plan", "", "", ""}, noulOuts: []float64{0.9}, scoreOuts: []float64{0.2, 0.2}}
	if result := Run(context.Background(), f, "work"); !result.OK || result.Plan != "first plan" {
		t.Fatalf("empty revision replaced the plan: %+v", result)
	}
}

type failingRecordGate struct{ *fakeGate }

func (f failingRecordGate) Record(log.Kind, any) (log.Event, error) {
	return log.Event{}, fmt.Errorf("disk failed")
}
func TestRequiredPlanMustBeRecorded(t *testing.T) {
	for _, judge := range []bool{false, true} {
		f := &fakeGate{t: t, models: []string{"plan"}, scoreOuts: []float64{0.9}}
		if judge {
			f.handle = &scheme.Handle{Class: "jev-key", ID: "x"}
		}
		if result := Required(context.Background(), failingRecordGate{f}, "work"); result.OK || result.Error != "disk failed" {
			t.Fatalf("%+v", result)
		}
	}
}
