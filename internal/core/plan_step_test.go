package core

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func stepCall(id string, step any, state, note string) fake.ToolCall {
	return fake.ToolCall{ID: id, Name: "plan-step", Args: map[string]any{"step": step, "state": state, "note": note}}
}

// The model marks the plan's steps with plan-step: each mark is recorded as
// plan.step and answered with the next unmarked step; a step the plan does
// not have is refused in the answer and recorded nowhere.
func TestModelMarksPlanSteps(t *testing.T) {
	judge := newFakeJudge(t, noul(0.9), score(3.6))
	r := judgedRig(t, judge, "",
		fake.Turn{Content: "1. read main.go\n2. edit it\n3. run the tests"},
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("a", 1, "done", "read it"), stepCall("b", 3, "skipped", "no tests here"), stepCall("c", 9, "done", "")}},
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("d", 2, "done", "edited")}},
		fake.Turn{Content: "done"})
	if e := r.turn("change it"); e != "" {
		t.Fatal(e)
	}
	// The main request shows the plan under a header that asks for marks.
	if main := string(r.srv.Requests[1].Raw); !strings.Contains(main, "mark each step with plan-step") {
		t.Fatalf("plan header:\n%s", main)
	}
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var marks []log.PlanStep
	answers := map[string]string{}
	for _, ev := range evs {
		switch ev.Kind {
		case log.KindPlanStep:
			var m log.PlanStep
			ev.Decode(&m)
			marks = append(marks, m)
		case log.KindToolResult:
			var tr log.ToolResult
			ev.Decode(&tr)
			answers[tr.ID] = tr.Value + tr.Error
		}
	}
	want := []log.PlanStep{{Step: 1, Of: 3, State: "done", Note: "read it"}, {Step: 3, Of: 3, State: "skipped", Note: "no tests here"}, {Step: 2, Of: 3, State: "done", Note: "edited"}}
	if len(marks) != len(want) {
		t.Fatalf("marks %+v", marks)
	}
	for i := range want {
		if marks[i] != want[i] {
			t.Fatalf("mark %d: %+v, want %+v", i, marks[i], want[i])
		}
	}
	if answers["a"] != "Step 1 is done. Next unmarked: 2. edit it" ||
		answers["b"] != "Step 3 is skipped. Next unmarked: 2. edit it" ||
		!strings.Contains(answers["c"], "There is no step 9: the plan has 3 steps") ||
		answers["d"] != "Step 2 is done. Every step of the plan is marked." {
		t.Fatalf("answers %q", answers)
	}
}

// Without a plan there is nothing to mark, and nothing is recorded.
func TestPlanStepWithoutAPlan(t *testing.T) {
	r := judgedRig(t, nil, "",
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("a", 1, "done", "")}},
		fake.Turn{Content: "done"})
	if e := r.turn("what is this?"); e != "" {
		t.Fatal(e)
	}
	if strings.Contains(r.logb.String(), `"kind":"plan.step"`) {
		t.Fatal("a mark was recorded without a plan")
	}
	if !strings.Contains(r.logb.String(), "No plan was recorded in this turn") {
		t.Fatalf("answer:\n%s", r.logb.String())
	}
}
