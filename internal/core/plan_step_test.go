package core

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/window"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func stepCall(id string, step any, state, note string) fake.ToolCall {
	return fake.ToolCall{ID: id, Name: "plan-step", Args: map[string]any{"step": step, "state": state, "note": note}}
}

// The model marks the plan's steps with plan-step: each mark is recorded as
// plan.step and answered with the next unmarked step; a step the plan does
// not have is refused in the answer and recorded nowhere.
func TestModelMarksPlanSteps(t *testing.T) {
	t.Parallel()
	judge := newFakeJudge(t, noul(0.9), score(3.6))
	r := judgedRig(t, judge, "",
		fake.Turn{Content: "1. read main.go\n2. edit it\n3. run the tests"},
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("a", 1, "done", "read it"), stepCall("b", 3, "skipped", "no tests here"), stepCall("c", 9, "done", "")}},
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("d", 2, "done", "edited")}},
		fake.Turn{Content: "done"})
	if e := r.turn("change it"); e != "" {
		t.Fatal(e)
	}
	// The main request shows the plan under a header that asks for marks
	// as the work happens, not at the end: a plan marked all at once left
	// the console's plan panel at 0 of 6 for the whole hour of session
	// 20261004T053047Z-1b4c67dc (ADR 32, amended 2026-10-04).
	if main := string(r.srv.Requests[1].Raw); !strings.Contains(main, "mark each step with plan-step") ||
		!strings.Contains(main, "never several together at the end") {
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
	t.Parallel()
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
// it is recorded (decision "model"), the next request shows it after
// plan-write's answer, and plan-step marks it. Writing it again revises it.
func TestModelWritesItsOwnPlan(t *testing.T) {
	t.Parallel()
	revised := strings.Replace(shapedPlan, "go test ./...", "go vet ./...", 1)
	r := judgedRig(t, nil, "",
		fake.Turn{ToolCalls: []fake.ToolCall{planWriteCall("w", shapedPlan)}},
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("a", 1, "done", "edited")}},
		fake.Turn{ToolCalls: []fake.ToolCall{planWriteCall("v", revised)}},
		fake.Turn{Content: "done"})
	if e := r.turn("change main.go"); e != "" {
		t.Fatal(e)
	}
	if msgs := requestMessages(t, r.srv.Requests[1]); msgs[len(msgs)-2].Role != "tool" ||
		!strings.HasSuffix(msgs[len(msgs)-1].Content, "follow the run and write the plan again):\n"+shapedPlan) {
		t.Fatalf("the plan does not follow plan-write's answer: %+v", msgs)
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
	if answers["w"] != "Plan recorded: 4 steps. It stays in front of you; mark each step with plan-step as its work is done, before you start the next — never several together at the end." ||
		answers["a"] != "Step 1 is done. Next unmarked: 2. Test: main_test.go TestGreeting — main prints it; fails today: main prints nothing." {
		t.Fatalf("answers %q", answers)
	}
}

// A chat's turn that a failed model request ends leaves its plan to the
// next message: "continue" carries it (decision "carried") with the steps
// marked so far, shown with them, and plan-step goes on from them. A turn
// that finishes leaves none: the message after it is a new request.
func TestAnUnfinishedTurnLeavesItsPlan(t *testing.T) {
	// llm.RetryDelay is the whole process's, so this test is not parallel:
	// no other session runs while it is shortened.
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
	msgs := requestMessages(t, r.srv.Requests[5])
	if !strings.HasSuffix(msgs[len(msgs)-1].Content, "\n"+shapedPlan+"\n\nMarked in earlier turns of this task:\nStep 1 done: edited") ||
		msgs[len(msgs)-2].Content != window.DefaultTaskHeader+"\ncontinue" {
		t.Fatalf("the carried plan does not follow the request with its marks: %+v", msgs)
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

// The plan gate's explorer and planner read the agent's last answer and the
// names of the files attached to the request, which a context of their own
// does not show: in session 20261005T060224Z-2f9eb922 the explorer said the
// request "does not identify what 'there' refers to", when a screenshot and
// the agent's own question did (ADR 32, amended 2026-10-05). Its judge
// still reads only the person's messages (ADR 19).
func TestThePlannerReadsTheLastAnswerAndTheAttachments(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, `
(define gate-saw #f)
(define base-agent-turn agent-turn)
(define (agent-turn request)
  (if (equal? request "the second")
      (set! gate-saw (list (plan-chat-request request) (plan-model-request (plan-chat-request request))))
      (base-agent-turn request)))`, 10,
		fake.Turn{Content: "Which one: the first or the second?"},
	)
	if e := r.turn("make it less verbose"); e != "" {
		t.Fatal(e)
	}
	notes := filepath.Join(t.TempDir(), "what-i-see.txt")
	if err := os.WriteFile(notes, []byte("the run rows repeat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Attach(notes, "what-i-see.txt", "unix:uid=1"); err != nil {
		t.Fatal(err)
	}
	if e := r.turn("the second"); e != "" {
		t.Fatal(e)
	}
	judged, seen := r.eval("(car gate-saw)"), r.eval("(cadr gate-saw)")
	for _, want := range []string{"The agent last answered:\\nWhich one: the first or the second?", "make it less verbose", "the second", "The person attached to this message: what-i-see.txt"} {
		if !strings.Contains(seen, want) {
			t.Errorf("what the explorer and the planner read lacks %q:\n%s", want, seen)
		}
	}
	// The judge reads the person's messages, never the agent's replies
	// (ADR 19).
	if strings.Contains(judged, "Which one") || strings.Contains(judged, "what-i-see.txt") || !strings.Contains(judged, "make it less verbose") {
		t.Errorf("what the judge reads:\n%s", judged)
	}
}

// A plan written again keeps the marks of the steps it keeps word for word,
// at their new numbers, and the model reads them under it; a new step
// starts unmarked. Written again word for word, a plan started its marks
// over and the model marked six steps again in 30 seconds
// (20261003T143916Z-48f31857 seq 6528).
func TestAPlanWrittenAgainKeepsItsMarks(t *testing.T) {
	t.Parallel()
	plan := "1. Change: a.go Run — do it.\n2. Test: a_test.go TestRun — runs; fails today: no Run.\n3. Docs: none — nothing a person reads changes.\n4. Check: go test ./..."
	again := "1. Change: a.go Run — do it.\n2. Change: b.go Helper — share it.\n3. Test: a_test.go TestRun — runs; fails today: no Run.\n4. Docs: none — nothing a person reads changes.\n5. Check: go test ./..."
	r := strategyRig(t, "", 20,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "p1", Name: "plan-write", Args: map[string]any{"plan": plan}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "s1", Name: "plan-step", Args: map[string]any{"step": 1, "state": "done", "note": "a.go has Run"}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "s2", Name: "plan-step", Args: map[string]any{"step": 2, "state": "done", "note": "TestRun failed, then passed"}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "p2", Name: "plan-write", Args: map[string]any{"plan": again}}}},
		fake.Turn{Content: "done"},
	)
	if e := r.turn("add Run"); e != "" {
		t.Fatal(e)
	}
	var last log.GatePlan
	for _, ev := range r.logged() {
		if ev.Kind == log.KindGatePlan {
			if err := ev.Decode(&last); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(last.Marks) != 2 || last.Marks[0].Step != 1 || last.Marks[0].Note != "a.go has Run" || last.Marks[1].Step != 3 || last.Marks[1].State != "done" {
		t.Fatalf("marks carried: %+v", last.Marks)
	}
	if got := r.results()["p2"]; got != "" {
		t.Fatalf("the second plan-write failed: %s", got)
	}
	// The model reads the kept marks under the plan, as made in this turn.
	final := r.srv.Requests[len(r.srv.Requests)-1]
	found := false
	for _, m := range requestMessages(t, final) {
		found = found || strings.HasSuffix(m.Content, again+"\n\nMarked before the plan was written again:\nStep 1 done: a.go has Run\nStep 3 done: TestRun failed, then passed")
	}
	if !found {
		t.Fatalf("the window does not show the kept marks: %s", final.Raw)
	}
}
