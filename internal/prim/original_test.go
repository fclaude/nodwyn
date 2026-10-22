package prim

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/brote/scheme"
)

// phraseCheck is a repository whose "tests" are files under tests/ holding a
// phrase that calc.txt must contain, and whose check, run.sh, prints a
// pytest-style FAILED line for each test file whose phrase is missing and
// exits 1 if any is. It stands for any ecosystem: original-tests reads the
// check's output, not a language.
func phraseCheck(t *testing.T, files map[string]string) (root string, o Options, git func(args ...string) string) {
	t.Helper()
	root, k, git := keepRepo(t)
	for _, name := range []string{"plants.txt", "app.go", "gone.txt", "Makefile", ".env", "sub/deep.txt"} {
		if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	write(t, root, "run.sh", "#!/bin/sh\nfail=0\nfor f in tests/*.t; do\n  [ -f \"$f\" ] || continue\n  want=$(cat \"$f\")\n  if ! grep -q \"$want\" calc.txt; then echo \"FAILED $f::case - missing $want\"; fail=1; fi\ndone\nexit $fail\n", 0o755)
	for name, content := range files {
		write(t, root, name, content, 0o644)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "repository")
	return root, k.o, git
}

// failuresOf is the failures original-tests names, as "path: test, test".
func failuresOf(res originalResult) []string {
	var out []string
	for _, f := range res.failures {
		out = append(out, f.path+": "+strings.Join(f.tests, ", "))
	}
	return out
}

func fileText(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// The check runs again with the test files the turn edited as the last
// commit had them. 5040f9fe's shape: the error now says more, and the test's
// phrase was cut to match, so the turn's own run passed while the original
// test, which pins the phrase the rewording split, fails. A test the change
// did not break, one that fails on the unchanged code too, a test file the
// turn did not touch and one the turn made are not named; the working tree
// is as the turn left it (ADR 32, amended 2026-10-16).
func TestOriginalTestsNameTheTestsTheChangeBreaks(t *testing.T) {
	root, o, _ := phraseCheck(t, map[string]string{
		"calc.txt":        "stream ended without a terminal response\nunrelated\n",
		"tests/msg.t":     "stream ended without a terminal response",
		"tests/other.t":   "unrelated",
		"tests/broken.t":  "a phrase calc.txt never had",
		"tests/lonely.t":  "unrelated",
		"tests/readme.md": "not a test",
	})
	write(t, root, "calc.txt", "stream with Content-Type \"\" ended without a terminal response\nunrelated\n", 0o644)
	write(t, root, "tests/msg.t", "ended without a terminal response", 0o644)
	write(t, root, "tests/other.t", "unrelated", 0o644) // rewritten to the same text: not edited
	write(t, root, "tests/own.t", "a phrase of the turn's own test that calc.txt lacks", 0o644)
	res, err := originalTests(t.Context(), o, "sh", []string{"run.sh"}, []string{"tests/msg.t", "tests/other.t", "tests/own.t", "README.md", "./tests/msg.t"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := failuresOf(res), []string{"tests/msg.t: tests/msg.t::case"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failures %q, want %q (unrun %v)", got, want, res.unrun)
	}
	if len(res.unrun) != 0 {
		t.Fatalf("unrun %v", res.unrun)
	}
	for name, want := range map[string]string{
		"tests/msg.t": "ended without a terminal response",
		"tests/own.t": "a phrase of the turn's own test that calc.txt lacks",
		"calc.txt":    "stream with Content-Type \"\" ended without a terminal response\nunrelated\n",
	} {
		if got := fileText(t, root, name); got != want {
			t.Errorf("%s is not as the turn left it: %q", name, got)
		}
	}
}

// With no paths the check runs as the turn left it, and the tests it fails
// that the last commit passes are named: psf-requests-2931 broke an existing
// test it did not edit, and put a full run's failures down to the
// environment. The tests the unchanged repository fails, and the turn's own
// new test, are not named.
func TestOriginalTestsNameWhatTheChangeBreaksWithoutAnEditedTest(t *testing.T) {
	root, o, _ := phraseCheck(t, map[string]string{
		"calc.txt":      "alpha beta\n",
		"tests/alpha.t": "alpha",
		"tests/beta.t":  "beta",
		"tests/needs.t": "a service that is not there",
	})
	write(t, root, "calc.txt", "alpha\n", 0o644) // beta is gone
	write(t, root, "tests/own.t", "the turn's own test of something else", 0o644)
	res, err := originalTests(t.Context(), o, "sh", []string{"run.sh"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := failuresOf(res), []string{": tests/beta.t::case"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failures %q, want %q (unrun %v)", got, want, res.unrun)
	}
	if got := fileText(t, root, "tests/own.t"); got != "the turn's own test of something else" {
		t.Errorf("the turn's own test file is not as it was: %q", got)
	}
	if got := fileText(t, root, "calc.txt"); got != "alpha\n" {
		t.Errorf("calc.txt is not as the turn left it: %q", got)
	}
}

// A test file the turn deleted is run too, as a grader puts it back, and is
// gone again afterwards.
func TestOriginalTestsOfADeletedFile(t *testing.T) {
	root, o, _ := phraseCheck(t, map[string]string{
		"calc.txt":    "one\n",
		"tests/one.t": "one",
	})
	write(t, root, "calc.txt", "two\n", 0o644)
	if err := os.Remove(filepath.Join(root, "tests", "one.t")); err != nil {
		t.Fatal(err)
	}
	res, err := originalTests(t.Context(), o, "sh", []string{"run.sh"}, []string{"tests/one.t"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := failuresOf(res), []string{"tests/one.t: tests/one.t::case"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failures %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(root, "tests", "one.t")); !os.IsNotExist(err) {
		t.Fatalf("the deleted test file is back: %v", err)
	}
}

// A check that passes with the test files as they were names nothing.
func TestOriginalTestsAreQuietWhenTheOriginalsPass(t *testing.T) {
	root, o, _ := phraseCheck(t, map[string]string{
		"calc.txt":    "one two\n",
		"tests/one.t": "one",
	})
	write(t, root, "calc.txt", "one two three\n", 0o644)
	write(t, root, "tests/one.t", "one two", 0o644)
	res, err := originalTests(t.Context(), o, "sh", []string{"run.sh"}, []string{"tests/one.t"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.failures) != 0 || len(res.unrun) != 0 {
		t.Fatalf("%v", res)
	}
}

// Failures the unchanged repository has are not the change's: here the
// edited test fails on the last commit too.
func TestOriginalTestsLeaveOutWhatFailsOnTheLastCommit(t *testing.T) {
	root, o, _ := phraseCheck(t, map[string]string{
		"calc.txt":    "one\n",
		"tests/bad.t": "never there",
	})
	write(t, root, "tests/bad.t", "one", 0o644) // passes only as edited
	res, err := originalTests(t.Context(), o, "sh", []string{"run.sh"}, []string{"tests/bad.t"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.failures) != 0 {
		t.Fatalf("failures %v", failuresOf(res))
	}
}

// A check whose output names no test is told by its exit status when the
// last commit passes it, and left unsaid, as unrun, when it fails there too.
func TestOriginalTestsOfACheckThatNamesNoTest(t *testing.T) {
	const quiet = "#!/bin/sh\nwant=$(cat tests/one.t)\ngrep -q \"$want\" calc.txt || { echo 'assertion failed'; exit 3; }\n"
	root, o, _ := phraseCheck(t, map[string]string{"calc.txt": "one\n", "tests/one.t": "one", "quiet.sh": quiet})
	write(t, root, "calc.txt", "two\n", 0o644)
	write(t, root, "tests/one.t", "two", 0o644)
	res, err := originalTests(t.Context(), o, "sh", []string{"quiet.sh"}, []string{"tests/one.t"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := failuresOf(res), []string{"tests/one.t: the check exits 3 (last line: assertion failed)"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failures %q, want %q (unrun %v)", got, want, res.unrun)
	}
	// The same check, failing on the last commit too.
	root2, o2, _ := phraseCheck(t, map[string]string{"calc.txt": "zero\n", "tests/one.t": "one", "quiet.sh": quiet})
	write(t, root2, "tests/one.t", "zero", 0o644)
	res, err = originalTests(t.Context(), o2, "sh", []string{"quiet.sh"}, []string{"tests/one.t"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.failures) != 0 || len(res.unrun) != 1 || !strings.Contains(res.unrun[0], "fails on the last commit too") {
		t.Fatalf("%v", res)
	}
}

// A check that does not finish is unrun, and the files are as the turn left
// them.
func TestOriginalTestsOfACheckThatRunsOutOfTime(t *testing.T) {
	root, o, _ := phraseCheck(t, map[string]string{
		"calc.txt":    "one\n",
		"tests/one.t": "one",
		"slow.sh":     "#!/bin/sh\nsleep 30\n",
	})
	write(t, root, "tests/one.t", "one one", 0o644)
	was := originalTestsBudget
	originalTestsBudget = 300 * time.Millisecond
	defer func() { originalTestsBudget = was }()
	res, err := originalTests(t.Context(), o, "sh", []string{"slow.sh"}, []string{"tests/one.t"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.failures) != 0 || len(res.unrun) != 1 || !strings.Contains(res.unrun[0], "did not finish") {
		t.Fatalf("%v", res)
	}
	if got := fileText(t, root, "tests/one.t"); got != "one one" {
		t.Fatalf("the test file is not as the turn left it: %q", got)
	}
}

// The same on a Go module, run with go test: the primitive does not know Go.
func TestOriginalTestsForAGoModule(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}
	root, o, _ := phraseCheck(t, map[string]string{
		"go.mod":       "module example.com/m\n\ngo 1.22\n",
		"calc/calc.go": "package calc\n\nfunc Msg() string { return \"stream ended without a terminal response\" }\n\nfunc Other() int { return 1 }\n",
		"calc/calc_test.go": "package calc\n\nimport (\n\t\"strings\"\n\t\"testing\"\n)\n\n" +
			"func TestMsg(t *testing.T) {\n\twant := \"stream ended without a terminal response\"\n\tif !strings.Contains(Msg(), want) {\n\t\tt.Fatalf(\"%q\", Msg())\n\t}\n}\n\n" +
			"func TestOther(t *testing.T) {\n\tif Other() != 1 {\n\t\tt.Fatal(Other())\n\t}\n}\n\n" +
			"func TestFailsAnyway(t *testing.T) { t.Fatal(\"needs a service\") }\n",
	})
	write(t, root, "calc/calc_test.go", "package calc\n\nimport (\n\t\"strings\"\n\t\"testing\"\n)\n\n"+
		"func TestMsg(t *testing.T) {\n\twant := \"ended without a terminal response\"\n\tif !strings.Contains(Msg(), want) {\n\t\tt.Fatalf(\"%q\", Msg())\n\t}\n}\n\n"+
		"func TestOther(t *testing.T) {\n\tif Other() != 1 {\n\t\tt.Fatal(Other())\n\t}\n}\n\n"+
		"func TestFailsAnyway(t *testing.T) { t.Fatal(\"needs a service\") }\n\n"+
		"func TestAdded(t *testing.T) { t.Fatal(\"the turn's own test\") }\n", 0o644)
	write(t, root, "calc/calc.go", "package calc\n\nfunc Msg() string { return \"Responses stream with Content-Type \\\"\\\" ended without a terminal response\" }\n\nfunc Other() int { return 1 }\n", 0o644)
	res, err := originalTests(t.Context(), o, "go", []string{"test", "./calc/"}, []string{"calc/calc_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := failuresOf(res), []string{"calc/calc_test.go: TestMsg"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failures %q, want %q; unrun %v", got, want, res.unrun)
	}
}

// And on Python's unittest, as the SWE-bench tasks that began this were.
func TestOriginalTestsForPythonUnittest(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	root, o, _ := phraseCheck(t, map[string]string{
		"calc.py":       "def one():\n    return 1\n",
		"test_calc.py":  "import unittest\nfrom calc import one\n\n\nclass CalcTest(unittest.TestCase):\n    def test_one(self):\n        self.assertEqual(one(), 1)\n",
		"test_other.py": "import unittest\n\n\nclass OtherTest(unittest.TestCase):\n    def test_other(self):\n        self.assertTrue(True)\n",
	})
	write(t, root, "calc.py", "def one():\n    return 2\n", 0o644)
	write(t, root, "test_calc.py", "import unittest\nfrom calc import one\n\n\nclass CalcTest(unittest.TestCase):\n    def test_one(self):\n        self.assertEqual(one(), 2)\n", 0o644)
	res, err := originalTests(t.Context(), o, "python3", []string{"-m", "unittest", "test_calc", "test_other"}, []string{"test_calc.py"})
	if err != nil {
		t.Fatal(err)
	}
	if got := failuresOf(res); len(got) != 1 || !strings.Contains(got[0], "test_one (test_calc.CalcTest") {
		t.Fatalf("failures %q (unrun %v)", got, res.unrun)
	}
}

// (original-tests cmd args paths) answers an alist of the failures and the
// reasons it could not tell, and nothing for paths that are not edited test
// files HEAD has.
func TestOriginalTestsPrimitive(t *testing.T) {
	root, o, _ := phraseCheck(t, map[string]string{
		"calc.txt":    "one\n",
		"tests/one.t": "one",
	})
	in := scheme.New()
	if err := in.Register(originalTestsPrim(o)); err != nil {
		t.Fatal(err)
	}
	eval := func(src string) string {
		t.Helper()
		v, err := in.EvalString(t.Context(), src)
		if err != nil {
			t.Fatal(err)
		}
		return v.String()
	}
	write(t, root, "tests/one.t", "o", 0o644)
	write(t, root, "calc.txt", "two\n", 0o644)
	if got := eval(`(original-tests "sh" '("run.sh") '("nothing.t" "calc.txt"))`); got != `(("failures") ("unrun"))` {
		t.Errorf("paths that are no edited test files: %s", got)
	}
	got := eval(`(original-tests "sh" '("run.sh") '("tests/one.t"))`)
	for _, want := range []string{`("path" . "tests/one.t")`, `("tests" "tests/one.t::case")`, `("unrun")`} {
		if !strings.Contains(got, want) {
			t.Errorf("original-tests lacks %s: %s", want, got)
		}
	}
}

// What a run's output says failed, for each ecosystem, once and in order.
func TestFailureIDsReadEachEcosystemsOutput(t *testing.T) {
	for name, tc := range map[string]struct {
		out  string
		want []string
	}{
		"go":              {"=== RUN   TestA\n--- FAIL: TestA (0.00s)\n    --- FAIL: TestA/case (0.00s)\n--- FAIL: TestB (0.00s)\nFAIL\nFAIL\texample.com/m/calc\t0.01s\n", []string{"TestA", "TestB"}},
		"go package only": {"# example.com/m\nx.go:3:1: syntax error\nFAIL\texample.com/m [build failed]\n", []string{"package example.com/m"}},
		"pytest": {"FAILED tests/test_a.py::TestA::test_x - AssertionError: 1 != 2\nFAILED tests/test_a.py::TestA::test_x - again\nERROR tests/test_b.py - ImportError\n2 failed, 5 passed\n",
			[]string{"tests/test_a.py::TestA::test_x", "tests/test_b.py"}},
		"unittest": {"FAIL: test_one (test_calc.CalcTest.test_one)\nERROR: test_two (test_calc.CalcTest.test_two)\nFAILED (failures=1, errors=1)\n",
			[]string{"test_one (test_calc.CalcTest.test_one)", "test_two (test_calc.CalcTest.test_two)"}},
		"cargo":   {"test calc::tests::adds ... ok\ntest calc::tests::subs ... FAILED\n\ntest result: FAILED. 1 passed; 1 failed\n", []string{"calc::tests::subs"}},
		"jest":    {"FAIL src/a.test.js\n  ● Calc › adds two numbers\n\n    expect(received)\n", []string{"Calc › adds two numbers"}},
		"vitest":  {" FAIL  src/a.test.ts > Calc > adds\n", []string{"src/a.test.ts > Calc > adds"}},
		"rspec":   {"Failed examples:\n\nrspec ./spec/calc_spec.rb:12 # Calc adds\n", []string{"./spec/calc_spec.rb:12"}},
		"nothing": {"all good\n3 passed\n", nil},
	} {
		if got := failureIDs(tc.out); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
