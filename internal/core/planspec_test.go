package core

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A plan written as data (planspec.scm, ADR 32, amended 2026-10-13): the
// planner is asked for forms, asked again when its plan leaves out a name
// the request gives, and the plan the model reads is the forms written out
// as numbered steps, with the names after them.
func TestAPlanAsDataKeepsTheRequestsNames(t *testing.T) {
	t.Parallel()
	first := `(change "calc.py" "one" "return 10")
(test "calc_test.py" "test_one" "one() is 10, not 1" (fails-today "it is 1") (run "python3 -m pytest calc_test.py"))
(none docs "no document describes calc")
(check "python3 -m pytest")`
	r := newRig(t, true,
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: first},                    // the planner, without the request's name
		fake.Turn{Content: first + "\n(names \"answer_ten\")"},
		fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"},
	)
	if e := r.planned("make one() return 10 behind the flag answer_ten"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) < 4 {
		t.Fatalf("%d requests", len(r.srv.Requests))
	}
	if again := lastMessage(r, 2); !strings.Contains(again, `the request gives these names and the plan has none of them: "answer_ten"`) {
		t.Fatalf("the planner was not asked again for the name:\n%s", again)
	}
	main := string(r.srv.Requests[3].Raw)
	for _, want := range []string{"1. Change: calc.py one — return 10", "2. Test: calc_test.py test_one — one() is 10, not 1; fails today: it is 1; run: python3 -m pytest calc_test.py",
		"3. Docs: none — no document describes calc", "4. Check: python3 -m pytest", `Names the change produces, as the request spells them: \"answer_ten\".`} {
		if !strings.Contains(main, want) {
			t.Errorf("the plan the model reads lacks %q:\n%s", want, main)
		}
	}
}

// Before an unattended turn ends, the plan's steps are set against what
// happened: a name the plan says the change produces and the change does
// not contain is said, once, and the steps that have their evidence (the
// file changed, the test is in its file and ran after the last edit, the
// check passed after it) are marked by the harness, not the model.
func TestAPlansEvidenceIsCheckedBeforeTheTurnEnds(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	plan := `(change "calc.py" "one" "return 10")
(test "calc_test.py" "test_one" "one() is 10" (fails-today "it is 1") (run "python3 -m pytest calc_test.py"))
(none docs "no document describes calc")
(names "answer_ten")
(check "grep -q 'return 10' calc.py")`
	r := unattendedRigIn(t, repo,
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: plan},                     // the planner
		write("t", "calc_test.py", "def test_one():\n    pass\n"),
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 2\n"),
		shellArgs("c", "bash", "-c", "grep -q 'return 10' calc.py && echo ok # python3 -m pytest calc_test.py"),
		fake.Turn{Content: "done"}, // told the change lacks answer_ten
		fake.Turn{Content: "answer_ten turned out not to be needed: one() returns 10 always."},
	)
	if e := r.planned("make one() return 10 behind the flag answer_ten"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 7 {
		t.Fatalf("%d requests, want 7; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	msg := lastMessage(r, 6)
	if !strings.Contains(msg, "your plan's evidence is missing") || !strings.Contains(msg, `the change does not contain "answer_ten"`) || strings.Contains(msg, "step 1") || strings.Contains(msg, "step 2") {
		t.Fatalf("evidence message: %q", msg)
	}
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	marked := map[int]bool{}
	for _, ev := range evs {
		if ev.Kind == log.KindPlanStep {
			var p log.PlanStep
			if ev.Decode(&p) == nil && p.State == "done" {
				marked[p.Step] = true
			}
		}
	}
	for _, step := range []int{1, 2, 3, 4} {
		if !marked[step] {
			t.Errorf("step %d was not marked by the harness; marked %v", step, marked)
		}
	}
}

// A test step that says its test fails today is run on the unchanged code
// (test-before): a test that passes there too does not show the change, and
// is said so before the turn ends.
func TestAPlannedTestThatPassesBeforeTheChangeIsSaid(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	plan := `(change "calc.py" "one" "return 10")
(test "calc_test.py" "test_one" "one() is 10" (fails-today "it is 1") (run "grep -q 'def test_one' calc_test.py"))
(none docs "no document describes calc")
(check "grep -q 'return 10' calc.py")`
	r := unattendedRigIn(t, repo,
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: plan},                     // the planner
		write("t", "calc_test.py", "def test_one():\n    pass\n"),
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 2\n"),
		shellArgs("c", "bash", "-c", "grep -q 'return 10' calc.py && grep -q 'def test_one' calc_test.py # pytest"),
		fake.Turn{Content: "done"}, // told the test passes on the unchanged code too
		fake.Turn{Content: "the test now calls one()."},
	)
	if e := r.planned("make one() return 10"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 7 {
		t.Fatalf("%d requests, want 7; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if msg := lastMessage(r, 6); !strings.Contains(msg, "step 2 (Test calc_test.py test_one): it passes on the unchanged code too") {
		t.Fatalf("evidence message: %q", msg)
	}
}
