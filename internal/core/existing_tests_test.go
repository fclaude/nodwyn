package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// What the default prompts and loop ask when a change meets an existing test
// (ADR 32, amended 2026-10-16). On commit-bench's Muse runs the wrong side of
// a failing existing test lost two tasks: 94f5cac4 rewrote the row
// `(fs-read "big.txt" 1 0)` "count must be at least 1" to suit a behaviour
// the request never stated, and 855e90e7 added an untested skip to the
// denylist's code instead of fixing the test.

const editedLines = "You changed these expectations of existing tests"

// twoTestsRepo is goRepo with a second test file, other_test.go, whose table
// has two cases.
func twoTestsRepo(t *testing.T) string {
	t.Helper()
	repo := goRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "calc", "other_test.go"), []byte("package calc\n\nvar others = []string{\n\t\"x\",\n\t\"y\",\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "git add calc && git -c commit.gpgsign=false -c user.name=t -c user.email=t@t commit -q -m other")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return repo
}

// An edit that removes a case of an existing test is asked about as it is
// made, not twenty rounds later among the finish review's points: the line,
// and which sentence of the request requires the new expectation. The edit
// stands.
func TestAnEditedExpectationOfAnExistingTestIsAskedAbout(t *testing.T) {
	t.Parallel()
	repo := goRepo(t)
	const rewritten = "package calc\n\nvar cases = []string{\n\t\"a\",\n}\n"
	r := unattendedRigIn(t, repo,
		write("c", "$TMPDIR/check.sh", "grep -q 'func Root' calc/calc.go\n"),
		write("t", "calc/calc_test.go", rewritten),
		shellArgs("r", "bash", "$TMPDIR/check.sh"),
		fake.Turn{Content: "done"}, // the review still names the line
		fake.Turn{Content: "The request drops case b."},
	)
	if e := r.turn("make Root add an x to its path"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if msg := lastMessage(r, 1); strings.Contains(msg, editedLines) {
		t.Fatalf("asked before the test was edited: %q", msg)
	}
	msg := lastMessage(r, 2)
	for _, want := range []string{editedLines + " (the line numbers are the last commit's):\n- calc/calc_test.go:5: \"b\",",
		"Which sentence of the request requires the new expectation, for that input or case?",
		"does not cover an input it does not name", "restore the line and change the code to meet it",
		"say which in your answer", "asked once for each test file"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the question lacks %q: %q", want, msg)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "calc", "calc_test.go")); string(got) != rewritten {
		t.Fatalf("the edit was not left as the model made it:\n%s", got)
	}
	if msg := lastMessage(r, 4); !strings.Contains(msg, "expectations of existing tests:\n- calc/calc_test.go:5: \"b\",") {
		t.Fatalf("the finish review no longer names it: %q", msg)
	}
}

// The question is asked once for each test file, however many of its
// expectations the turn changes, and a second file is asked about in its own
// right: the first file's lines are not said again.
func TestTheQuestionOnAnEditedExpectationIsAskedOncePerFile(t *testing.T) {
	t.Parallel()
	r := unattendedRigIn(t, twoTestsRepo(t),
		write("c", "$TMPDIR/check.sh", "true\n"),
		write("t1", "calc/calc_test.go", "package calc\n\nvar cases = []string{\n\t\"a\",\n\t\"c\",\n}\n"),                                                    // "b" becomes "c"
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "t2", Name: "fs-edit", Args: map[string]any{"path": "calc/calc_test.go", "old": "\t\"a\",\n", "new": ""}}}}, // and "a" goes
		write("t3", "calc/other_test.go", "package calc\n\nvar others = []string{\n\t\"x\",\n}\n"),
		shellArgs("r", "bash", "$TMPDIR/check.sh"),
		fake.Turn{Content: "done"},
		fake.Turn{Content: "The request changes both tables."},
	)
	if e := r.turn("change the tables"); e != "" {
		t.Fatal(e)
	}
	if first := lastMessage(r, 2); !strings.Contains(first, "- calc/calc_test.go:5: \"b\",") || strings.Contains(first, "other_test.go") {
		t.Fatalf("the first file: %q", first)
	}
	if again := lastMessage(r, 3); strings.Contains(again, editedLines) {
		t.Fatalf("asked again of the same file: %q", again)
	}
	other := lastMessage(r, 4)
	if !strings.Contains(other, editedLines) || !strings.Contains(other, "- calc/other_test.go:5: \"y\",") || strings.Contains(other, "calc_test.go") {
		t.Fatalf("the second file: %q", other)
	}
}

// A test the turn added, and a case it adds to an existing table, have no
// line at the last commit to ask about: the turn is not asked, however often
// it edits them. The one existing test it does rewrite, last, is asked about,
// and only that one.
func TestTheQuestionIsNotAskedOfTestsTheTurnAdded(t *testing.T) {
	t.Parallel()
	r := unattendedRigIn(t, twoTestsRepo(t),
		write("c", "$TMPDIR/check.sh", "true\n"),
		write("n1", "calc/extra_test.go", "package calc\n\nvar extra = []string{\n\t\"p\",\n\t\"q\",\n}\n"),
		write("n2", "calc/extra_test.go", "package calc\n\nvar extra = []string{\n\t\"p\",\n\t\"r\",\n}\n"),          // its own case rewritten
		write("n3", "calc/calc_test.go", "package calc\n\nvar cases = []string{\n\t\"a\",\n\t\"b\",\n\t\"c\",\n}\n"), // a case added to an old table
		write("n4", "calc/other_test.go", "package calc\n\nvar others = []string{\n\t\"x\",\n}\n"),                   // an old case removed
		shellArgs("r", "bash", "$TMPDIR/check.sh"),
		fake.Turn{Content: "done"},
		fake.Turn{Content: "The request drops case y; extra_test.go is part of the change."},
	)
	if e := r.turn("add cases"); e != "" {
		t.Fatal(e)
	}
	var asked []int
	for i := range r.srv.Requests {
		if strings.Contains(lastMessage(r, i), editedLines) {
			asked = append(asked, i)
		}
	}
	if len(asked) != 1 || asked[0] != 5 {
		t.Fatalf("asked after requests %v, want only the one after the existing test was rewritten (5)", asked)
	}
	if msg := lastMessage(r, 5); !strings.Contains(msg, "- calc/other_test.go:5: \"y\",") || strings.Contains(msg, "extra_test.go") || strings.Contains(msg, "calc/calc_test.go") {
		t.Fatalf("asked of more than the existing test: %q", msg)
	}
}

// Both system prompts carry the rule on a failing existing test: decide
// which side is wrong before editing either, the old expectation stands
// unless the request names the new behaviour, no conditional skip in
// production code to make an old test pass, and a test for each side of a
// branch the request asks for, not of one added for any other reason. The
// earlier wording, "a branch you add gets a test for each side", led 855e90e7
// to add a test that pinned its skip (ADR 32, amended 2026-10-16, the
// original tests).
func TestThePromptsDecideWhichSideOfAFailingTestIsWrong(t *testing.T) {
	t.Parallel()
	for name, sys := range map[string]string{
		"unattended":  unattendedRig(t).s.loop.WindowContext().System,
		"interactive": newRig(t, true).s.loop.WindowContext().System,
	} {
		for _, want := range []string{
			"When an existing test fails after your change, decide which side is wrong before you edit either.",
			"The old expectation stands unless the request names the new behaviour for that input or case",
			"Never add a conditional skip or exception to production code (least of all in a guard or denylist) to make an old test pass unless the request asks for it",
			"if the test's setup contradicts what the request names, fix the setup (a fixture is setup; its assertions are not)",
			"A branch the request asks for gets a test for each side of it; a branch you add for any other reason is not tested into place: delete it.",
		} {
			if !strings.Contains(sys, want) {
				t.Errorf("the %s prompt lacks %q", name, want)
			}
		}
		if strings.Contains(sys, "A branch you add gets a test for each side of it.") {
			t.Errorf("the %s prompt still asks for a test of every branch you add", name)
		}
	}
}

// The finish review asks for the changed lines no test executed when the
// change adds a branch and nothing in the turn measured coverage: the check
// passed, which says nothing of which lines it ran. check-without takes out
// whole hunks, and a dead branch lives inside one the check needs (3422faf1,
// dsl20r r3). A change that adds no branch, and a turn that measured coverage
// after its last edit, have nothing more to be asked, and end at their answer.
func TestTheReviewAsksForTheChangedLinesNoTestRan(t *testing.T) {
	t.Parallel()
	const straight = "package calc\n\nfunc evalOr(p string) string { return p }\n\nfunc Root(p string) string { return evalOr(p) + \"x\" }\n"
	const branch = "package calc\n\nfunc evalOr(p string) string { return p }\n\nfunc Root(p string) string {\n\tif p == \"\" {\n\t\treturn \"\"\n\t}\n\treturn evalOr(p) + \"x\"\n}\n"
	check := write("c", "$TMPDIR/check.sh", "grep -q 'evalOr(p) + \"x\"' calc/calc.go\n")
	for _, c := range []struct {
		name  string
		turns []fake.Turn
		asked bool
	}{
		{"a branch and no coverage run", []fake.Turn{check, write("w", "calc/calc.go", branch), shellArgs("r", "bash", "$TMPDIR/check.sh"),
			fake.Turn{Content: "done"}, // the review: the branch
			fake.Turn{Content: "Coverage is not installed here; no test reaches the empty path."}}, true},
		{"no branch", []fake.Turn{check, write("w", "calc/calc.go", straight), shellArgs("r", "bash", "$TMPDIR/check.sh"),
			fake.Turn{Content: "done"}}, false},
		{"coverage measured after the last edit", []fake.Turn{check, write("w", "calc/calc.go", branch), shellArgs("r", "bash", "$TMPDIR/check.sh"),
			shellArgs("v", "bash", "-c", "echo coverage: 100.0% of statements"), fake.Turn{Content: "done"}}, false},
	} {
		r := unattendedRigIn(t, goRepo(t), c.turns...)
		if e := r.turn("make Root add an x to its path"); e != "" {
			t.Fatal(c.name, e)
		}
		if len(r.srv.Requests) != len(c.turns) {
			t.Fatalf("%s: %d requests, want %d; last message %q", c.name, len(r.srv.Requests), len(c.turns), lastMessage(r, len(r.srv.Requests)-1))
		}
		if !c.asked {
			continue
		}
		msg := lastMessage(r, len(c.turns)-1)
		if strings.Contains(msg, "add a test that executes it or delete the line") {
			t.Errorf("%s: the review still offers a test as the way out for a skip: %q", c.name, msg)
		}
		for _, want := range []string{"Your check (bash $TMPDIR/check.sh) passed, which does not show that a test ran each line you changed.",
			"with coverage (go test -coverprofile=$TMPDIR/cover.out", "name the changed lines no test executed",
			"For a line that does what the request asks for, add a test that executes it.",
			"A branch no case reaches (a condition that cannot be true, a skip nothing exercises) is the usual one: delete it, or quote the sentence of the request that requires it and then add the test",
			"a skip or exception you added after an existing test failed gets no test, since the test would pin it",
			"Your next reply without tool calls ends the turn"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: the review lacks %q: %q", c.name, want, msg)
			}
		}
	}
}

// The planner is asked to keep unstated behaviour out of its steps and to
// list a rule's cases by clause and by position, in the plan language and in
// the prose shape; plan-write's description says the same. On 94f5cac4 the
// plan copied "count 0 stays and means default", which the request never
// states, and on 3422faf1 it said only "drop an overlapping token".
func TestThePlannerKeepsUnstatedBehaviourApartAndListsCases(t *testing.T) {
	t.Parallel()
	asked := []string{"A step carries only what the request states", "an assumption an existing test contradicts is dropped",
		"For each rule the request states, list its cases: one per clause, and for a range, an overlap or an edge one per position (inside, crossing the start, crossing the end, equal, empty)."}
	forms := `(change "calc.py" "one" "a skipped start is line 1")
(test "calc_test.py" "test_one" "start 0 is line 1; a count given as 0 stays an error" (fails-today "start 0 is refused") (run "python3 -m pytest calc_test.py"))
(docs "README.md" "the start rule")
(assumes "a count of 0 means the default" "test_one expects an error for count 0, so it is dropped")
(check "python3 -m pytest")`

	// The plan language.
	r := newRig(t, true,
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: forms},                    // the planner
		fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"},
	)
	if e := r.planned("make a skipped start mean line 1"); e != "" {
		t.Fatal(e)
	}
	planner := string(r.srv.Requests[1].Raw)
	for _, want := range []string{"(assumes \\\"behaviour the request does not state\\\"", "no step relies on it until an existing test was checked against it",
		"For each rule the request states, the test steps list its cases: one per clause, and for a range, an overlap or an edge one per position (inside, crossing the start, crossing the end, equal, empty)."} {
		if !strings.Contains(planner, want) {
			t.Errorf("the planner's request lacks %q:\n%s", want, planner)
		}
	}
	main := string(r.srv.Requests[2].Raw)
	const line = "Assumptions, which the request does not state (check each against the existing tests before relying on it): a count of 0 means the default (checked against: test_one expects an error for count 0, so it is dropped)."
	if !strings.Contains(main, line) {
		t.Errorf("the plan the model reads lacks %q:\n%s", line, main)
	}
	if !strings.Contains(main, "A step carries only what the request states: behaviour it does not state goes on an unnumbered line after the steps") {
		t.Errorf("plan-write's description lacks the rule on assumptions:\n%s", main)
	}
	if plans := r.gatePlans(); len(plans) == 0 || len(plans[0].Steps()) != 4 {
		t.Errorf("the assumptions line is a step of the plan: %+v", plans)
	}

	// The prose shape, where the plan language is off.
	r = trustedRigIn(t, t.TempDir(), "(set! plan-language #f)\n",
		fake.Turn{Content: "main.go declares package main."}, // the explorer
		fake.Turn{Content: "1. Edit main.go."},               // the planner
		fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"},
	)
	if e := r.planned("change main.go"); e != "" {
		t.Fatal(e)
	}
	prose := string(r.srv.Requests[1].Raw)
	for _, want := range asked {
		if !strings.Contains(prose, want) {
			t.Errorf("the prose planner's request lacks %q:\n%s", want, prose)
		}
	}
	if !strings.Contains(prose, "goes on an unnumbered line after the steps, \\\"Assumptions: ...\\\"") {
		t.Errorf("the prose planner's request does not name the assumptions line:\n%s", prose)
	}
}

// A test file is told by its name or its folder, a source file for coverage
// is one with a coverage extension that is no test, and a line is a branch
// by the words that open one.
func TestTestFilesAndBranchesAreToldApart(t *testing.T) {
	t.Parallel()
	r := newRig(t, true)
	for src, want := range map[string]string{
		`(map test-file-path? '("calc/calc_test.go" "tests/helpers.py" "pkg/test_x.py" "pkg/x_test.py" "web/a.test.js" "web/a.spec.ts" "src/calc.go" "src/inspect.go" "docs/testing.md" "main.go"))`: "(#t #t #t #t #t #t #f #f #f #f)",
		`(map coverage-source? '("src/calc.go" "calc/calc_test.go" "web/a.js" "web/a.test.js" "README.md" "Makefile" "pkg/test_x.py"))`:                                                              "(#t #f #t #f #f #f #f)",
		`(list (branchy? '("x := 1" "return y")) (branchy? '("x := 1" "\tif x > 1 {")) (branchy? '("a = b or c")) (branchy? '("f(a) && g(b)")))`:                                                     "(#f #t #t #t)",
	} {
		v, err := r.s.Eval(t.Context(), src)
		if err != nil {
			t.Fatal(src, err)
		}
		if got := v.String(); got != want {
			t.Errorf("%s = %s, want %s", src, got, want)
		}
	}
}
