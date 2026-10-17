package core

import (
	"os/exec"
	"strings"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// What the finish review may ask a model to write (ADR 32, amended
// 2026-10-16). On commit-bench 6e18b7ba (dsl22) one review asked for the
// plan's tests that passed on the unchanged code, for tests of six one-line
// hunks the check passed without, and for the lines of the change no test
// executed, and the model spent 95 of the run's 167 calls (1.64M of 2.38M
// cost-eq) writing and extending tests, more than a third of them for
// branches of its own defensive code. The coverage point is gone, and the
// other asks are bounded: a few named items, once in a run.

// manyPartsRepo is a git repository with calc.py committed, five functions
// that each return their number.
func manyPartsRepo(t *testing.T) string {
	t.Helper()
	repo := gitWorkTree(t)
	var src strings.Builder
	for i := 1; i <= 5; i++ {
		if i > 1 {
			src.WriteString("\n\n")
		}
		src.WriteString("def f" + string(rune('0'+i)) + "():\n    return " + string(rune('0'+i)) + "\n")
	}
	cmd := exec.Command("sh", "-c", "cat > calc.py && git add calc.py && git -c commit.gpgsign=false -c user.name=t -c user.email=t@t commit -q -m code")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(src.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return repo
}

// The change a check that looks at f1 alone passes without, in four more
// hunks: f2's is the biggest.
const manyPartsChanged = "def f1():\n    return 10\n\n\ndef f2():\n    x = 20\n    return x\n\n\ndef f3():\n    return 30\n\n\ndef f4():\n    return 40\n\n\ndef f5():\n    return 50\n"

// The coverage point is gone: a change that adds a branch, passes its check
// and measures no coverage ends at its answer, and no request asks for the
// lines no test ran. It was 62 of 6e18b7ba's calls.
func TestTheReviewDoesNotAskForTheChangedLinesNoTestRan(t *testing.T) {
	t.Parallel()
	const branch = "package calc\n\nfunc evalOr(p string) string { return p }\n\nfunc Root(p string) string {\n\tif p == \"\" {\n\t\treturn \"\"\n\t}\n\treturn evalOr(p) + \"x\"\n}\n"
	r := unattendedRigIn(t, goRepo(t),
		write("c", "$TMPDIR/check.sh", "grep -q 'evalOr(p) + \"x\"' calc/calc.go\n"),
		write("w", "calc/calc.go", branch),
		shellArgs("r", "bash", "$TMPDIR/check.sh"),
		fake.Turn{Content: "done"},
	)
	if e := r.turn("make Root add an x to its path"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 4 {
		t.Fatalf("%d requests, want 4; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	for _, phrase := range []string{"ran each line you changed", "name the changed lines no test executed", "-coverprofile"} {
		if n := requestsMention(r, phrase); n != 0 {
			t.Errorf("%d requests still say %q", n, phrase)
		}
	}
}

// The parts point names the largest three parts the check passed without
// and says how many more there were; it says to write a case only for a
// part the request asks for, and nothing else.
func TestTheUntestedPartsPointNamesTheLargestThree(t *testing.T) {
	t.Parallel()
	r := unattendedRigIn(t, manyPartsRepo(t),
		write("c", "$TMPDIR/check.sh", "grep -q 'return 10' calc.py\n"),
		write("w", "calc.py", manyPartsChanged),
		shellArgs("t", "bash", "$TMPDIR/check.sh"),
		fake.Turn{Content: "done"}, // told the check does not test four parts
		fake.Turn{Content: "f2 to f5 are part of the change."},
	)
	if e := r.turn("make every function return ten times its number"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	msg := lastMessage(r, 4)
	for _, want := range []string{"calc.py:6 (hunk, +2 -1 lines); calc.py:11 (hunk, +1 -1 lines); calc.py:15 (hunk, +1 -1 lines); 1 more of the same kind are not named.",
		"Only these parts are asked about.",
		"For a part that implements what the request asks for, add the input, case or step that needs it to your check so that it fails without the part, and run it again; do not test anything else because of this message."} {
		if !strings.Contains(msg, want) {
			t.Errorf("the point lacks %q: %q", want, msg)
		}
	}
	if strings.Contains(msg, "calc.py:19") {
		t.Errorf("a fourth part is named: %q", msg)
	}
}

// An item the review asked about is not asked about in a later review of
// the run, those past the limit included: the model answered the review
// once.
func TestAnItemTheReviewAskedAboutIsNotAskedAboutAgain(t *testing.T) {
	t.Parallel()
	r := newRig(t, true)
	for _, c := range []struct{ src, want string }{
		{`(finish-ask (lambda (x) x) '(1 2 3 4))`, "((1 2 3) . 1)"},
		{`(finish-ask (lambda (x) x) '(1 2 3 4))`, "(() . 0)"},
		{`(finish-ask (lambda (x) x) '(4 5))`, "((5) . 0)"},
		{`finish-asked`, "(5 1 2 3 4)"},
	} {
		v, err := r.s.Eval(t.Context(), c.src)
		if err != nil {
			t.Fatal(c.src, err)
		}
		if got := v.String(); got != c.want {
			t.Errorf("%s = %s, want %s", c.src, got, c.want)
		}
	}
}

// The plan's tests the change lacks are named three at a time: five test
// steps whose tests were never written give three lines and a count, and the
// rest are the model's to say it does not need.
func TestThePlansUnwrittenTestsAreNamedThreeAtATime(t *testing.T) {
	t.Parallel()
	plan := `(change "calc.py" "one" "return 10")
(test "a_test.py" "test_a" "a" (fails-today "it is 1") (run "true"))
(test "b_test.py" "test_b" "b" (fails-today "it is 1") (run "true"))
(test "c_test.py" "test_c" "c" (fails-today "it is 1") (run "true"))
(test "d_test.py" "test_d" "d" (fails-today "it is 1") (run "true"))
(test "e_test.py" "test_e" "e" (fails-today "it is 1") (run "true"))
(none docs "no document describes calc")
(check "grep -q 'return 10' calc.py")`
	r := unattendedRigIn(t, codeRepo(t),
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: plan},                     // the planner
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 2\n"),
		shellArgs("c", "bash", "-c", "grep -q 'return 10' calc.py # python3 -m pytest"),
		fake.Turn{Content: "done"}, // told which of the plan's tests are missing
		fake.Turn{Content: "None of the tests is needed: the check covers the change."},
	)
	if e := r.planned("make one() return 10"); e != "" {
		t.Fatal(e)
	}
	msg := lastMessage(r, len(r.srv.Requests)-1)
	if n := strings.Count(msg, ": the test is not in that file"); n != 3 {
		t.Errorf("%d tests named, want 3: %q", n, msg)
	}
	for _, want := range []string{"step 2 (Test a_test.py test_a)", "step 4 (Test c_test.py test_c)",
		"2 more of the plan's tests are not named here: say in your report which of them the request asks for"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the point lacks %q: %q", want, msg)
		}
	}
	if strings.Contains(msg, "test_d") {
		t.Errorf("a fourth test is named: %q", msg)
	}
}
