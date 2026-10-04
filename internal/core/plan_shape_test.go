package core

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// plan-write checks a plan's shape before it records it (plan.scm): each
// step starts with its kind, there is a Change:, a Test: and a Docs: step,
// each Test: says why it fails on the unchanged code, and a Check: comes
// last (ADR 32, amended 2026-10-03).

// planWriteTurns runs one turn per element of turns, each the plans the
// model writes in that turn, one plan-write call per round, then a reply
// that ends it. It returns the answers in call order and the plans the
// log recorded.
func planWriteTurns(t *testing.T, turns ...[]string) ([]string, []log.GatePlan) {
	t.Helper()
	var script []fake.Turn
	var ids []string
	for i, plans := range turns {
		for j, plan := range plans {
			id := string(rune('a'+i)) + string(rune('0'+j))
			ids = append(ids, id)
			script = append(script, fake.Turn{ToolCalls: []fake.ToolCall{planWriteCall(id, plan)}})
		}
		script = append(script, fake.Turn{Content: "done"})
	}
	r := judgedRig(t, nil, "", script...)
	for range turns {
		if e := r.turn("change main.go"); e != "" {
			t.Fatal(e)
		}
	}
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	answers := map[string]string{}
	for _, ev := range evs {
		if ev.Kind == log.KindToolResult {
			var tr log.ToolResult
			ev.Decode(&tr)
			answers[tr.ID] = tr.Value + tr.Error
		}
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = answers[id]
	}
	return out, r.gatePlans()
}

// A plan with no Test: step is turned down, saying so, and not recorded.
func TestPlanWithoutATestStepIsTurnedDown(t *testing.T) {
	t.Parallel()
	answers, plans := planWriteTurns(t, []string{"1. Change: main.go main — print the greeting.\n2. Docs: none — no doc describes main.go.\n3. Check: go test ./..."})
	if len(plans) != 0 {
		t.Fatalf("recorded %+v", plans)
	}
	if !strings.HasPrefix(answers[0], "Plan not recorded") || !strings.Contains(answers[0], "There is no Test: step") {
		t.Fatalf("answer %q", answers[0])
	}
}

// A Test: step that does not say how its test fails today is turned down
// by its number, and so is a step without a kind.
func TestPlanWithAnUnexplainedTestIsTurnedDown(t *testing.T) {
	t.Parallel()
	answers, plans := planWriteTurns(t, []string{"1. Change: main.go main — print the greeting.\n" +
		"2. Test: main_test.go TestGreeting — main prints it.\n" +
		"3. Read main.go again\n" +
		"4. Docs: none — no doc describes main.go.\n" +
		"5. Check: go test ./..."})
	if len(plans) != 0 {
		t.Fatalf("recorded %+v", plans)
	}
	if !strings.Contains(answers[0], "Step 2 is a Test: step that does not say why its test fails on the unchanged code") ||
		!strings.Contains(answers[0], "Step 3 has no kind: start it with Change:, Test:, Docs: or Check:") ||
		strings.Contains(answers[0], "Step 1") || strings.Contains(answers[0], "There is no") {
		t.Fatalf("answer %q", answers[0])
	}
}

// A plan with no Docs: step, or one that does not end with a Check:, is
// turned down.
func TestPlanWithoutDocsOrACheckIsTurnedDown(t *testing.T) {
	t.Parallel()
	answers, plans := planWriteTurns(t, []string{
		"1. Change: main.go main — print the greeting.\n2. Test: main_test.go TestGreeting — main prints it; fails today: main prints nothing.\n3. Check: go test ./...",
		"1. Change: main.go main — print the greeting.\n2. Check: go test ./...\n3. Test: main_test.go TestGreeting — main prints it; fails today: main prints nothing.\n4. Docs: README.md — say what main prints.",
	})
	if len(plans) != 0 {
		t.Fatalf("recorded %+v", plans)
	}
	if !strings.Contains(answers[0], "There is no Docs: step") || strings.Contains(answers[0], "The last step") {
		t.Fatalf("no docs: %q", answers[0])
	}
	if !strings.Contains(answers[1], "The last step, 4, is not a Check: step") || strings.Contains(answers[1], "There is no") {
		t.Fatalf("no check last: %q", answers[1])
	}
}

// A step of a known kind that runs on past the console's title cap, with
// no short name before a dash, is turned down with the step's number, and
// so is a step whose name before its dash is that long. This fails on the
// unchanged code, which recorded the plan: it tests the short-name check.
func TestPlanWithoutShortNamesIsTurnedDown(t *testing.T) {
	t.Parallel()
	answers, plans := planWriteTurns(t, []string{"1. Change: read internal/frontend/rpc/workspaces.go, the desk state builder and the fields it puts in the desk state\n" +
		"2. Test: workspaces_test.go TestDeskStateNamesWorktree — the desk state names the worktree; fails today: it does not.\n" +
		"3. Docs: none — no doc describes the desk state.\n" +
		"4. Check: go build ./... && gofmt -l internal cmd test && go vet ./... && go test -race ./internal/frontend/rpc/",
		"1. Change: internal/frontend/rpc/workspaces.go readWorkspacesDeskState readWorkspacesDeskStateFields and the worktree resolver it consults through the repository layer — gather the desk state's fields.\n" +
			"2. Test: workspaces_test.go TestDeskStateNamesWorktree — the desk state names the worktree; fails today: it does not.\n" +
			"3. Docs: none — no doc describes the desk state.\n" +
			"4. Check: go test ./..."})
	if len(plans) != 0 {
		t.Fatalf("recorded %+v", plans)
	}
	for i := range answers {
		if !strings.HasPrefix(answers[i], "Plan not recorded") {
			t.Fatalf("attempt %d: %q", i+1, answers[i])
		}
		if !strings.Contains(answers[i], "Step 1 has no short name") ||
			strings.Contains(answers[i], "Step 2") || strings.Contains(answers[i], "Step 3") ||
			(i == 0 && strings.Contains(answers[i], "Step 4")) || (i == 1 && strings.Contains(answers[i], "Check:")) {
			t.Fatalf("attempt %d: %q", i+1, answers[i])
		}
	}
}

// A plan says it has no test or no doc to change with a "none" step, and
// that is accepted. This passes on the unchanged code too, which recorded
// any plan: it guards the exception.
func TestNoneStepsAreAccepted(t *testing.T) {
	t.Parallel()
	answers, plans := planWriteTurns(t, []string{"1. Change: README.md — fix the install command.\n2. **Test:** none — only docs change.\n3. Docs: none — README.md is the doc, changed in step 1.\n4. Check: git diff"})
	if len(plans) != 1 || answers[0] != "Plan recorded: 4 steps. It stays in front of you; mark each step with plan-step as its work is done, before you start the next — never several together at the end." {
		t.Fatalf("plans %+v, answer %q", plans, answers[0])
	}
}

// After two refusals in a turn the third plan is recorded as written, with
// what it still lacks, so a model that cannot get the shape right is not
// kept from the work; the next turn checks again.
func TestThirdShapelessPlanIsRecorded(t *testing.T) {
	t.Parallel()
	shapeless := "1. read main.go\n2. edit it"
	answers, plans := planWriteTurns(t, []string{shapeless, shapeless, shapeless}, []string{shapeless})
	for i := 0; i < 2; i++ {
		if !strings.HasPrefix(answers[i], "Plan not recorded") {
			t.Fatalf("attempt %d: %q", i+1, answers[i])
		}
	}
	if !strings.HasPrefix(answers[2], "Plan recorded: 2 steps, as written") ||
		!strings.Contains(answers[2], "Still missing:\n- Step 1 has no kind") ||
		!strings.Contains(answers[2], "- There is no Test: step") || !strings.Contains(answers[2], "- The last step, 2, is not a Check: step") {
		t.Fatalf("third attempt: %q", answers[2])
	}
	if len(plans) != 1 || plans[0].Plan != shapeless || plans[0].Noul != "model" {
		t.Fatalf("plans %+v", plans)
	}
	if !strings.HasPrefix(answers[3], "Plan not recorded") {
		t.Fatalf("the next turn's first attempt: %q", answers[3])
	}
}

// plan-step marks a Test: step by its number, and the next unmarked step
// is named with its kind. This passes on the unchanged code too: it guards
// reading a plan whose steps start with a kind and a colon.
func TestPlanStepMarksATestStep(t *testing.T) {
	t.Parallel()
	r := judgedRig(t, nil, "",
		fake.Turn{ToolCalls: []fake.ToolCall{planWriteCall("w", shapedPlan)}},
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("a", 2, "done", "failed before the change, passes after")}},
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("b", 1, "done", "main prints it")}},
		fake.Turn{Content: "done"})
	if e := r.turn("change main.go"); e != "" {
		t.Fatal(e)
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
	if len(marks) != 2 || marks[0] != (log.PlanStep{Step: 2, Of: 4, State: "done", Note: "failed before the change, passes after"}) {
		t.Fatalf("marks %+v", marks)
	}
	if answers["a"] != "Step 2 is done. Next unmarked: 1. Change: main.go main — print the greeting." ||
		answers["b"] != "Step 1 is done. Next unmarked: 3. Docs: none — no doc describes main.go." {
		t.Fatalf("answers %q", answers)
	}
}

// The gate's planner is asked for the same shape, when it drafts a plan and
// when it revises one, and the judge scores against it: a plan whose tests
// do not say what they check, or that leaves out the docs, does not reach
// the rubric's top level.
func TestPlannerAndJudgeAskForTheShape(t *testing.T) {
	t.Parallel()
	judge := newFakeJudge(t, noul(0.9), score(2), score(3.6))
	r := judgedRig(t, judge, "", fake.Turn{Content: "1. edit main.go"}, fake.Turn{Content: "1. edit main.go and test it"}, fake.Turn{Content: "done"})
	if e := r.turn("make main print a greeting"); e != "" {
		t.Fatal(e)
	}
	for i, what := range []string{"draft", "revision"} {
		msgs := requestMessages(t, r.srv.Requests[i])
		asked := msgs[len(msgs)-1].Content
		for _, want := range []string{"a short name", "\n- Change: a file", "\n- Test: the test file and name", "fails today", "\n- Docs: each file", "\n- Check: the command", "The last step is a Check: step."} {
			if !strings.Contains(asked, want) {
				t.Errorf("the planner's %s request lacks %q:\n%s", what, want, asked)
			}
		}
	}
	judge.mu.Lock()
	body := judge.bodies[1]
	judge.mu.Unlock()
	var sent struct {
		Questions map[string]struct {
			Instructions string   `json:"instructions"`
			Criteria     []string `json:"criteria"`
		} `json:"questions"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatal(err)
	}
	q := sent.Questions["q"]
	if len(q.Criteria) == 0 {
		t.Fatalf("no levels in the judge's request:\n%s", body)
	}
	top := q.Criteria[len(q.Criteria)-1]
	for _, want := range []string{"Change:", "a short name", "Test:", "why it fails on the unchanged code", "Docs:", "Check:"} {
		if !strings.Contains(q.Instructions, want) {
			t.Errorf("the rubric lacks %q: %s", want, q.Instructions)
		}
	}
	if !strings.Contains(top, "each test says what it checks and why it fails today") || !strings.Contains(top, "docs") {
		t.Errorf("the top level does not ask for tests that say what they check and for the docs: %q", top)
	}
}
