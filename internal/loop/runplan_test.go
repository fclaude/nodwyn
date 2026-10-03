package loop

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
)

// runLog writes events the way a run's driver and turns record them.
type runLog struct {
	buf bytes.Buffer
	w   *log.Writer
}

func newRunLog() *runLog {
	r := &runLog{}
	r.w = log.New(&r.buf, "s")
	return r
}

func (r *runLog) start(turns int) {
	r.w.Append(log.KindRunState, log.RunState{Status: "running", Turns: turns})
}
func (r *runLog) runTurn(n int) {
	r.w.Append(log.KindRunTurn, log.RunTurn{Number: n})
	r.chat("run turn")
}
func (r *runLog) chat(text string)    { r.w.Append(log.KindUserMsg, log.UserMsg{Text: text}) }
func (r *runLog) plan(p log.GatePlan) { r.w.Append(log.KindGatePlan, p) }
func (r *runLog) mark(n int, state, note string) {
	r.w.Append(log.KindPlanStep, log.PlanStep{Step: n, Of: 3, State: state, Note: note})
}

func (r *runLog) runPlan(t *testing.T) (string, []log.PlanStep, bool) {
	t.Helper()
	evs, err := log.Read(bytes.NewReader(r.buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return runPlan(evs)
}

const threeSteps = "1. read\n2. edit\n3. test"

// A run's plan outlives its turns: a later turn finds the plan an earlier
// one recorded, with every mark made since, the carried ones included, the
// latest per step and in step order.
func TestRunPlanCarriesAcrossTurns(t *testing.T) {
	r := newRunLog()
	r.start(0)
	r.runTurn(1)
	r.plan(log.GatePlan{Noul: "required", Plan: threeSteps})
	r.mark(2, "skipped", "not yet")
	r.mark(1, "done", "read it")
	r.runTurn(2)
	plan, marks, ok := r.runPlan(t)
	want := []log.PlanStep{{Step: 1, Of: 3, State: "done", Note: "read it"}, {Step: 2, Of: 3, State: "skipped", Note: "not yet"}}
	if !ok || plan != threeSteps || !reflect.DeepEqual(marks, want) {
		t.Fatalf("turn 2: %q %+v %v", plan, marks, ok)
	}
	// Turn 2 carries it, and marks step 2 again, done this time.
	r.plan(log.GatePlan{Noul: "carried", Plan: threeSteps, Marks: want})
	r.mark(2, "done", "edited")
	r.runTurn(3)
	_, marks, _ = r.runPlan(t)
	want = []log.PlanStep{{Step: 1, Of: 3, State: "done", Note: "read it"}, {Step: 2, Of: 3, State: "done", Note: "edited"}}
	if !reflect.DeepEqual(marks, want) {
		t.Fatalf("turn 3: %+v", marks)
	}
	// A new plan starts with no marks.
	r.plan(log.GatePlan{Noul: "plan", Plan: "1. finish"})
	r.runTurn(4)
	if plan, marks, ok := r.runPlan(t); !ok || plan != "1. finish" || len(marks) != 0 {
		t.Fatalf("new plan: %q %+v %v", plan, marks, ok)
	}
}

// Only a run's turns have a run plan, and only its turns' plans and marks
// count: a chat turn between leaves the run's plan alone, a resume keeps
// it, and a run started afresh has none.
func TestRunPlanBelongsToTheRun(t *testing.T) {
	r := newRunLog()
	r.chat("hello")
	r.plan(log.GatePlan{Noul: "plan", Plan: "1. chat plan"})
	r.start(0)
	r.runTurn(1)
	if _, _, ok := r.runPlan(t); ok {
		t.Fatal("a chat turn's plan became the run's")
	}
	r.plan(log.GatePlan{Noul: "required", Plan: threeSteps})
	r.w.Append(log.KindRunState, log.RunState{Status: "stopped", Turns: 1})
	r.chat("what happened?")
	if _, _, ok := r.runPlan(t); ok {
		t.Fatal("a chat turn has a run plan")
	}
	r.plan(log.GatePlan{Noul: "plan", Plan: "1. answer"})
	r.mark(1, "done", "answered")
	r.start(1) // resumed
	r.runTurn(2)
	if plan, marks, ok := r.runPlan(t); !ok || plan != threeSteps || len(marks) != 0 {
		t.Fatalf("resumed: %q %+v %v", plan, marks, ok)
	}
	r.start(0) // a fresh run
	r.runTurn(1)
	if _, _, ok := r.runPlan(t); ok {
		t.Fatal("a fresh run inherited a plan")
	}
}

func (r *runLog) end(outcome string) {
	r.w.Append(log.KindTurnEnd, log.TurnEnd{Text: "turn ended", Outcome: outcome})
}

func (r *runLog) unfinishedPlan(t *testing.T) (string, []log.PlanStep, bool) {
	t.Helper()
	evs, err := log.Read(bytes.NewReader(r.buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return unfinishedPlan(evs)
}

// A turn that an error, a limit or the person's Stop ended, or that nothing
// ended, leaves its plan to the next, with every mark made since, through
// as many such turns as follow. A turn that finished leaves none, and
// neither does one from a log that recorded no outcome.
func TestUnfinishedPlanOutlivesTurnsThatDidNotFinish(t *testing.T) {
	r := newRunLog()
	r.chat("change it")
	r.plan(log.GatePlan{Noul: "model", Plan: threeSteps})
	r.mark(1, "done", "read it")
	r.end("error")
	r.chat("continue")
	plan, marks, ok := r.unfinishedPlan(t)
	want := []log.PlanStep{{Step: 1, Of: 3, State: "done", Note: "read it"}}
	if !ok || plan != threeSteps || !reflect.DeepEqual(marks, want) {
		t.Fatalf("after an error: %q %+v %v", plan, marks, ok)
	}
	r.plan(log.GatePlan{Noul: "carried", Plan: threeSteps, Marks: want})
	r.mark(2, "done", "edited")
	r.end("canceled")
	r.chat("no, use the other file") // nothing ends this turn: the process died
	r.chat("continue")
	want = append(want, log.PlanStep{Step: 2, Of: 3, State: "done", Note: "edited"})
	if _, marks, ok := r.unfinishedPlan(t); !ok || !reflect.DeepEqual(marks, want) {
		t.Fatalf("after a Stop: %+v %v", marks, ok)
	}
	for _, outcome := range []string{"completed", ""} {
		r.chat("change it")
		r.plan(log.GatePlan{Noul: "model", Plan: threeSteps})
		r.end(outcome)
		r.chat("what is this?")
		if _, _, ok := r.unfinishedPlan(t); ok {
			t.Fatalf("a turn that ended %q left its plan", outcome)
		}
	}
	for _, outcome := range []string{"stopped", "hard_budget", "spend_limit"} {
		r.chat("change it")
		r.plan(log.GatePlan{Noul: "model", Plan: "1. finish"})
		r.end(outcome)
		r.chat("continue")
		if plan, marks, ok := r.unfinishedPlan(t); !ok || plan != "1. finish" || len(marks) != 0 {
			t.Fatalf("a turn that ended %q: %q %+v %v", outcome, plan, marks, ok)
		}
		r.end("completed")
	}
}
