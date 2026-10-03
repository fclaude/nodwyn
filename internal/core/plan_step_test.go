package core

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/llm"
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

func planWriteCall(id, plan string) fake.ToolCall {
	return fake.ToolCall{ID: id, Name: "plan-write", Args: map[string]any{"plan": plan}}
}

// shapedPlan has the shape plan-write asks for (plan.scm): a step of each
// kind, the test saying why it fails today, and a check last.
const shapedPlan = "1. Change: main.go main — print the greeting.\n" +
	"2. Test: main_test.go TestGreeting — main prints it; fails today: main prints nothing.\n" +
	"3. Docs: none — no doc describes main.go.\n" +
	"4. Check: go test ./..."

// Without a plan from the gate, the model writes its own with plan-write:
// it is recorded (decision "model"), pinned in the next request's system
// prompt, and plan-step marks it. Writing it again revises it.
func TestModelWritesItsOwnPlan(t *testing.T) {
	revised := strings.Replace(shapedPlan, "go test ./...", "go vet ./...", 1)
	r := judgedRig(t, nil, "",
		fake.Turn{ToolCalls: []fake.ToolCall{planWriteCall("w", shapedPlan)}},
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("a", 1, "done", "edited")}},
		fake.Turn{ToolCalls: []fake.ToolCall{planWriteCall("v", revised)}},
		fake.Turn{Content: "done"})
	if e := r.turn("change main.go"); e != "" {
		t.Fatal(e)
	}
	if sys := requestMessages(t, r.srv.Requests[1])[0].Content; !strings.HasSuffix(sys, "say so if you must deviate):\n"+shapedPlan) {
		t.Fatalf("the plan is not pinned:\n%s", sys)
	}
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var plans []log.GatePlan
	answers := map[string]string{}
	for _, ev := range evs {
		switch ev.Kind {
		case log.KindGatePlan:
			var p log.GatePlan
			ev.Decode(&p)
			plans = append(plans, p)
		case log.KindToolResult:
			var tr log.ToolResult
			ev.Decode(&tr)
			answers[tr.ID] = tr.Value + tr.Error
		}
	}
	if len(plans) != 2 || plans[0].Noul != "model" || plans[1].Plan != revised {
		t.Fatalf("plans %+v", plans)
	}
	if answers["w"] != "Plan recorded: 4 steps. It stays in front of you; mark each step with plan-step as you finish it." ||
		answers["a"] != "Step 1 is done. Next unmarked: 2. Test: main_test.go TestGreeting — main prints it; fails today: main prints nothing." {
		t.Fatalf("answers %q", answers)
	}
}

// A chat's turn that a failed model request ends leaves its plan to the
// next message: "continue" carries it (decision "carried") with the steps
// marked so far, pinned with them, and plan-step goes on from them. A turn
// that finishes leaves none: the message after it is a new request.
func TestAnUnfinishedTurnLeavesItsPlan(t *testing.T) {
	defer func(d time.Duration) { llm.RetryDelay = d }(llm.RetryDelay)
	llm.RetryDelay = time.Millisecond
	rateLimited := fake.Turn{Status: 429}
	r := judgedRig(t, nil, "",
		fake.Turn{ToolCalls: []fake.ToolCall{planWriteCall("w", shapedPlan)}},
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("a", 1, "done", "edited")}},
		rateLimited, rateLimited, rateLimited,
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("b", 2, "done", "failed before, passes now")}},
		fake.Turn{Content: "tested; the docs are next"},
		fake.Turn{Content: "main.go is the entry point"})
	if e := r.turn("change main.go"); !strings.Contains(e, "429") {
		t.Fatalf("the first turn ended with %q, want the model's 429", e)
	}
	if e := r.turn("continue"); e != "" {
		t.Fatal(e)
	}
	sys := requestMessages(t, r.srv.Requests[5])[0].Content
	if !strings.HasSuffix(sys, "\n"+shapedPlan+"\n\nMarked in earlier turns of this task:\nStep 1 done: edited") {
		t.Fatalf("the carried plan is not pinned with its marks:\n%s", sys)
	}
	if e := r.turn("what is main.go?"); e != "" {
		t.Fatal(e)
	}
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var plans []log.GatePlan
	var turnOf []int
	turns := 0
	answers := map[string]string{}
	for _, ev := range evs {
		switch ev.Kind {
		case log.KindUserMsg:
			turns++
		case log.KindGatePlan:
			var p log.GatePlan
			ev.Decode(&p)
			plans, turnOf = append(plans, p), append(turnOf, turns)
		case log.KindToolResult:
			var tr log.ToolResult
			ev.Decode(&tr)
			answers[tr.ID] = tr.Value + tr.Error
		}
	}
	if len(plans) != 2 || plans[1].Noul != "carried" || turnOf[1] != 2 || plans[1].Plan != plans[0].Plan ||
		len(plans[1].Marks) != 1 || plans[1].Marks[0] != (log.PlanStep{Step: 1, Of: 4, State: "done", Note: "edited"}) {
		t.Fatalf("plans %+v in turns %v", plans, turnOf)
	}
	if answers["b"] != "Step 2 is done. Next unmarked: 3. Docs: none — no doc describes main.go." {
		t.Fatalf("answers %q", answers)
	}
}
