package prim

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// goModule is a git repository with a Go module committed: files, by path.
func goModule(t *testing.T, files map[string]string) (root string, o Options, git func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}
	root, k, git := keepRepo(t)
	for name := range map[string]bool{"plants.txt": true, "app.go": true, "gone.txt": true, "Makefile": true, ".env": true, "sub/deep.txt": true} {
		if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	write(t, root, "go.mod", "module example.com/m\n\ngo 1.22\n", 0o644)
	for name, content := range files {
		write(t, root, name, content, 0o644)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "module")
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

// The tests of a test file the turn edited run as the last commit had them,
// against the change. 5040f9fe's shape: the error now says more, and the
// test's want was rewritten to match, so the turn's own run passed while the
// original test, which pins the phrase the rewording split, fails. A test
// HEAD does not declare (one the turn added to the file, or in a file of
// its own), a test the change did not break, one that fails on the unchanged
// code too, and a file the turn did not touch are not named; the working
// tree is as the turn left it (ADR 32, amended 2026-10-16).
func TestOriginalTestsNameTheTestsTheChangeBreaks(t *testing.T) {
	root, o, _ := goModule(t, map[string]string{
		"calc/calc.go": "package calc\n\nfunc Msg() string { return \"stream ended without a terminal response\" }\n\nfunc Other() int { return 1 }\n",
		"calc/calc_test.go": "package calc\n\nimport (\n\t\"strings\"\n\t\"testing\"\n)\n\n" +
			"func TestMsg(t *testing.T) {\n\twant := \"stream ended without a terminal response\"\n\tif !strings.Contains(Msg(), want) {\n\t\tt.Fatalf(\"%q\", Msg())\n\t}\n}\n\n" +
			"func TestOther(t *testing.T) {\n\tif Other() != 1 {\n\t\tt.Fatal(Other())\n\t}\n}\n\n" +
			"func TestFailsAnyway(t *testing.T) { t.Fatal(\"needs a service\") }\n",
		"calc/untouched_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestUntouched(t *testing.T) { t.Fatal(\"not edited, not run\") }\n",
	})
	const edited = "package calc\n\nimport (\n\t\"strings\"\n\t\"testing\"\n)\n\n" +
		"func TestMsg(t *testing.T) {\n\twant := \"ended without a terminal response\"\n\tif !strings.Contains(Msg(), want) {\n\t\tt.Fatalf(\"%q\", Msg())\n\t}\n}\n\n" +
		"func TestOther(t *testing.T) {\n\tif Other() != 1 {\n\t\tt.Fatal(Other())\n\t}\n}\n\n" +
		"func TestFailsAnyway(t *testing.T) { t.Fatal(\"needs a service\") }\n\n" +
		"func TestAdded(t *testing.T) { t.Fatal(\"the turn's own test\") }\n"
	write(t, root, "calc/calc_test.go", edited, 0o644)
	write(t, root, "calc/calc.go", "package calc\n\nfunc Msg() string { return \"Responses stream with Content-Type \\\"\\\" ended without a terminal response\" }\n\nfunc Other() int { return 1 }\n", 0o644)
	write(t, root, "calc/added_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestInAFileOfItsOwn(t *testing.T) { t.Fatal(\"added\") }\n", 0o644)
	res, err := originalTests(t.Context(), o, []string{"calc/calc_test.go", "calc/added_test.go", "README.md", "calc/untouched_test.go", "./calc/calc_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := failuresOf(res), []string{"calc/calc_test.go: TestMsg"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failures %q, want %q (unrun %v)", got, want, res.unrun)
	}
	if len(res.unrun) != 0 {
		t.Fatalf("unrun %v", res.unrun)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "calc", "calc_test.go")); string(got) != edited {
		t.Fatalf("the working tree's test file was touched:\n%s", got)
	}
}

// A test file the turn deleted is run too: a grader puts it back. One whose
// tests do not build against the change (the change removed what they call)
// is unrun, not named, as a signature the request changes would otherwise
// name every test that calls it.
func TestOriginalTestsOfADeletedFileAndOfOneThatDoesNotBuild(t *testing.T) {
	root, o, _ := goModule(t, map[string]string{
		"calc/calc.go":      "package calc\n\nfunc Root() int { return 1 }\n\nfunc Helper() int { return 2 }\n",
		"calc/calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestRoot(t *testing.T) {\n\tif Root() != 1 {\n\t\tt.Fatal(Root())\n\t}\n}\n",
		"util/util.go":      "package util\n\nfunc Gone() int { return 1 }\n",
		"util/util_test.go": "package util\n\nimport \"testing\"\n\nfunc TestGone(t *testing.T) {\n\tif Gone() != 1 {\n\t\tt.Fatal(Gone())\n\t}\n}\n",
	})
	write(t, root, "calc/calc.go", "package calc\n\nfunc Root() int { return 5 }\n\nfunc Helper() int { return 2 }\n", 0o644)
	if err := os.Remove(filepath.Join(root, "calc", "calc_test.go")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "util/util.go", "package util\n\nfunc Replaced() int { return 1 }\n", 0o644)
	write(t, root, "util/util_test.go", "package util\n\nimport \"testing\"\n\nfunc TestReplaced(t *testing.T) {\n\tif Replaced() != 1 {\n\t\tt.Fatal(Replaced())\n\t}\n}\n", 0o644)
	res, err := originalTests(t.Context(), o, []string{"calc/calc_test.go", "util/util_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := failuresOf(res), []string{"calc/calc_test.go: TestRoot"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failures %q, want %q", got, want)
	}
	if want := []string{"util"}; !reflect.DeepEqual(res.unrun, want) {
		t.Fatalf("unrun %v, want %v", res.unrun, want)
	}
}

// (original-tests paths) answers an alist of the failures and the
// directories it could not run, and nothing for a tree whose edited tests
// still pass or whose paths are no Go tests.
func TestOriginalTestsPrimitive(t *testing.T) {
	root, o, _ := goModule(t, map[string]string{
		"calc/calc.go":      "package calc\n\nfunc Root() int { return 1 }\n",
		"calc/calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestRoot(t *testing.T) {\n\tif Root() != 1 {\n\t\tt.Fatal(Root())\n\t}\n}\n",
		"tests/test_x.py":   "def test_x():\n    assert 1 == 1\n",
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
	write(t, root, "tests/test_x.py", "def test_x():\n    assert 2 == 2\n", 0o644)
	write(t, root, "calc/calc_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestRoot(t *testing.T) {\n\tif Root() != 5 {\n\t\tt.Fatal(Root())\n\t}\n}\n", 0o644)
	write(t, root, "calc/calc.go", "package calc\n\nfunc Root() int { return 5 }\n", 0o644)
	if got := eval(`(original-tests '("tests/test_x.py" "nothing_test.go"))`); got != `(("failures") ("unrun"))` {
		t.Errorf("paths that are no edited Go tests: %s", got)
	}
	got := eval(`(original-tests '("calc/calc_test.go"))`)
	for _, want := range []string{`("path" . "calc/calc_test.go")`, `("tests" "TestRoot")`, `("unrun")`} {
		if !strings.Contains(got, want) {
			t.Errorf("original-tests lacks %s: %s", want, got)
		}
	}
}
