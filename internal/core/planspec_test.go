package core

import (
	"bytes"
	"os/exec"
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

// A planner's slips that the reading forgives: a (run ...) inside
// (fails-today ...), and the closing parenthesis that leaves it short. The
// plan is read as written, without asking the planner again (commit-bench,
// 556d6d12, 2026-10-13).
func TestAPlanReadsDespiteANestedRun(t *testing.T) {
	t.Parallel()
	slipped := `(change "calc.py" "one" "return 10")
(test "calc_test.py" "test_one" "one() is 10, not 1" (fails-today "it is 1" (run "python3 -m pytest calc_test.py"))
(none docs "no document describes calc")
(check "python3 -m pytest")`
	r := newRig(t, true,
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: slipped},                  // the planner, once
		fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"},
	)
	if e := r.planned("make one() return 10"); e != "" {
		t.Fatal(e)
	}
	main := string(r.srv.Requests[2].Raw)
	if !strings.Contains(main, "2. Test: calc_test.py test_one — one() is 10, not 1; fails today: it is 1; run: python3 -m pytest calc_test.py") {
		t.Fatalf("the slipped plan was not read as written:\n%s", main)
	}
}

// The repository's whole check passing after the last edit stands for a
// test step's own run, and a document's edit after it does not make it
// stale: neither is said before the turn ends (commit-bench, 556d6d12: make
// validate passed last, and both test steps were said not to have passed).
func TestAWholeCheckStandsForThePlansTests(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	plan := `(change "calc.py" "one" "return 10")
(test "calc_test.py" "test_one" "one() is 10" (fails-today "it is 1") (run "python3 -m pytest calc_test.py -k test_one"))
(docs "NOTES.md" "say one() is 10")
(check "grep -q 'return 10' calc.py")`
	r := unattendedRigIn(t, repo,
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: plan},                     // the planner
		write("t", "calc_test.py", "def test_one():\n    assert __import__('calc').one() == 10\n"),
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 2\n"),
		shellArgs("c", "bash", "-c", "grep -q 'return 10' calc.py # make validate"),
		write("d", "NOTES.md", "one() is 10\n"),
		fake.Turn{Content: "done"},
	)
	if e := r.planned("make one() return 10"); e != "" {
		t.Fatal(e)
	}
	for i := range r.srv.Requests {
		if msg := lastMessage(r, i); strings.Contains(msg, "your plan's evidence is missing") {
			t.Fatalf("evidence message after a whole check and a document's edit: %q", msg)
		}
	}
}

// A name of the request the plan leaves out and the code already has goes
// into the plan's context without asking the planner again; one the code
// lacks is still asked for (commit-bench: every attempt of dsl3 and dsl6
// spent a planner reply on gpt-6.1-sol, a model's name the code has).
func TestANameTheCodeHasIsContextWithoutAskingAgain(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	cmd := exec.Command("sh", "-c", "printf 'use_cache = True\\n' > flags.py && git add flags.py && git -c commit.gpgsign=false -c user.name=t -c user.email=t@t commit -q -m flags")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	plan := `(change "calc.py" "one" "return 10 behind answer_ten")
(none test "a one-line change")
(none docs "no document describes calc")
(names "answer_ten")
(check "grep -q 'return 10' calc.py")`
	r := unattendedRigIn(t, repo,
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: plan},                     // the planner, once
		fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"},
	)
	if e := r.planned("make one() return 10 behind the flag answer_ten, read with use_cache"); e != "" {
		t.Fatal(e)
	}
	if main := string(r.srv.Requests[2].Raw); !strings.Contains(main, "Names it only uses: use_cache.") || !strings.Contains(main, `Names the change produces, as the request spells them: \"answer_ten\".`) {
		t.Fatalf("the planner was asked again, or the plan lacks the name:\n%s", main)
	}
}

// A plan the model writes again in prose is the plan from then on: a form it
// dropped is not held against the work (commit-bench, dsl8 107251d0: the
// rewritten plan had 7 steps, and the turn was told its forms' step 8 lacked
// its evidence).
func TestAPlanWrittenInProseDropsTheFormsItLeftOut(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	forms := `(change "calc.py" "one" "return 10")
(none test "a one-line change")
(docs "NOTES.md" "say one() is 10")
(check "grep -q 'return 10' calc.py # make validate")`
	prose := "1. Change: calc.py one — return 10.\n2. Test: none — a one-line change.\n3. Docs: none — nothing a person reads changes.\n4. Check: grep -q 'return 10' calc.py # make validate"
	r := unattendedRigIn(t, repo,
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: forms},                    // the planner
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "p", Name: "plan-write", Args: map[string]any{"plan": prose}}}},
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 2\n"),
		shellArgs("c", "bash", "-c", "grep -q 'return 10' calc.py # make validate"),
		fake.Turn{Content: "done"},
		fake.Turn{Content: "done"},
	)
	if e := r.planned("make one() return 10"); e != "" {
		t.Fatal(e)
	}
	for i := range r.srv.Requests {
		if msg := lastMessage(r, i); strings.Contains(msg, "your plan's evidence is missing") {
			t.Fatalf("the planner's forms were held against the work: %q", msg)
		}
	}
}

// Before the turn ends, a document that describes a function the plan's
// change steps name, and that the change left as it was, is said once
// (commit-bench, dsl9 3422faf1: the plan said no document changed, and the
// reference amended ADR 25, which describes the functions changed).
func TestADocumentOnWhatTheChangeAltersIsSaid(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	cmd := exec.Command("sh", "-c", "mkdir -p docs/adr && printf '# Values\\n\\n`one_value` returns 1.\\n' > docs/adr/0001-values.md && git add docs && git -c commit.gpgsign=false -c user.name=t -c user.email=t@t commit -q -m adr")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	plan := `(change "calc.py" "one_value" "return 10")
(none test "a one-line change")
(none docs "nothing describes it")
(check "grep -q 'return 10' calc.py # make validate")`
	r := unattendedRigIn(t, repo,
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: plan},                     // the planner
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 2\n"),
		shellArgs("c", "bash", "-c", "grep -q 'return 10' calc.py # make validate"),
		fake.Turn{Content: "done"},
		fake.Turn{Content: "the ADR describes the old value on purpose: it is history."},
	)
	if e := r.planned("make one() return 10"); e != "" {
		t.Fatal(e)
	}
	if msg := lastMessage(r, 5); !strings.Contains(msg, "documents that describe what your change alters are unchanged: docs/adr/0001-values.md (one_value)") {
		t.Fatalf("evidence message: %q", msg)
	}
}

// A change that edited a document of its own is not told of others: which
// documents it needed is the work's to judge.
func TestAChangeWithADocumentIsNotToldOfOthers(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	cmd := exec.Command("sh", "-c", "mkdir -p docs/adr && printf '# Values\\n\\n`one_value` returns 1.\\n' > docs/adr/0001-values.md && printf 'notes\\n' > NOTES.md && git add docs NOTES.md && git -c commit.gpgsign=false -c user.name=t -c user.email=t@t commit -q -m adr")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	plan := `(change "calc.py" "one_value" "return 10")
(none test "a one-line change")
(docs "NOTES.md" "say one() is 10")
(check "grep -q 'return 10' calc.py # make validate")`
	r := unattendedRigIn(t, repo,
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: plan},                     // the planner
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 2\n"),
		write("d", "NOTES.md", "one() is 10\n"),
		shellArgs("c", "bash", "-c", "grep -q 'return 10' calc.py # make validate"),
		fake.Turn{Content: "done"},
		fake.Turn{Content: "done"},
	)
	if e := r.planned("make one() return 10"); e != "" {
		t.Fatal(e)
	}
	for i := range r.srv.Requests {
		if msg := lastMessage(r, i); strings.Contains(msg, "documents that describe what your change alters") {
			t.Fatalf("told of other documents: %q", msg)
		}
	}
}

// A new file the model writes is marked for the next commit as it is
// written, so a generator that lists the tracked files counts it (commit-bench,
// dsl8 107251d0 and dsl13 3422faf1: the book's catalogue missed a new test
// file and a new scenario); one the plan does not name is still said to be
// new before the turn ends.
func TestANewFileIsListedByGitAndStillSaidNew(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	plan := `(change "calc.py" "one" "return 10")
(test "calc_test.py" "test_one" "one() is 10" (fails-today "it is 1") (run "grep -q 'return 10' calc.py && grep -q test_one calc_test.py"))
(none docs "no document describes calc")
(check "grep -q 'return 10' calc.py && echo checked")`
	r := unattendedRigIn(t, repo,
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: plan},                     // the planner
		write("t", "calc_test.py", "def test_one():\n    pass\n"),
		write("s", "scratch_repro.py", "print(1)\n"),
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 2\n"),
		shellArgs("x", "bash", "-c", "grep -q 'return 10' calc.py && grep -q test_one calc_test.py"),
		shellArgs("c", "bash", "-c", "grep -q 'return 10' calc.py && echo checked"),
		fake.Turn{Content: "done"},
		fake.Turn{Content: "done"}, // twice told to test: a grep is no test run
		fake.Turn{Content: "done"},
		fake.Turn{Content: "done"},
		fake.Turn{Content: "done"},
	)
	if e := r.planned("make one() return 10"); e != "" {
		t.Fatal(e)
	}
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "calc_test.py") || !strings.Contains(string(out), "scratch_repro.py") {
		t.Errorf("git ls-files after the turn:\n%s", out)
	}
	told := false
	for i := range r.srv.Requests {
		if msg := lastMessage(r, i); strings.Contains(msg, "scratch_repro.py") && !strings.Contains(msg, "calc_test.py") {
			told = true
		}
	}
	if !told {
		t.Errorf("the new file the plan does not name was not said to be new")
	}
}

// The forms a prose rewrite still holds keep their checks, under the new
// plan's numbers: the planned test, now step 1, is marked by the harness when
// a run of it passes (commit-bench: the model wrote its own plan in 4 of 6
// attempts, and dsl9 dropped every form with it).
func TestAPlanWrittenInProseKeepsTheFormsItHolds(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	forms := `(change "calc.py" "one" "return 10")
(test "calc_test.py" "test_one" "one() is 10" (fails-today "it is 1") (run "grep -q 'return 10' calc.py && grep -q test_one calc_test.py"))
(docs "NOTES.md" "say one() is 10")
(check "grep -q 'return 10' calc.py # make validate")`
	prose := "1. Test: calc_test.py test_one — one() is 10; fails today: it is 1.\n2. Change: calc.py one — return 10.\n3. Docs: none — nothing a person reads changes.\n4. Check: grep -q 'return 10' calc.py # make validate"
	r := unattendedRigIn(t, repo,
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: forms},                    // the planner
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "p", Name: "plan-write", Args: map[string]any{"plan": prose}}}},
		write("t", "calc_test.py", "def test_one():\n    pass\n"),
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 2\n"),
		shellArgs("x", "bash", "-c", "grep -q 'return 10' calc.py && grep -q test_one calc_test.py"),
		shellArgs("c", "bash", "-c", "grep -q 'return 10' calc.py # make validate"),
		fake.Turn{Content: "done"},
		fake.Turn{Content: "done"},
	)
	if e := r.planned("make one() return 10"); e != "" {
		t.Fatal(e)
	}
	for i := range r.srv.Requests {
		if msg := lastMessage(r, i); strings.Contains(msg, "your plan's evidence is missing") {
			t.Fatalf("a dropped form was held against the work: %q", msg)
		}
	}
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	marked := false
	for _, ev := range evs {
		var p log.PlanStep
		if ev.Kind == log.KindPlanStep && ev.Decode(&p) == nil && p.Step == 1 && strings.Contains(p.Note, "test_one passed") {
			marked = true
		}
	}
	if !marked {
		t.Errorf("the test step was not marked under the new plan's number 1")
	}
}
