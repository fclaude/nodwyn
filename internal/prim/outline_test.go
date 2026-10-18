package prim

import (
	"os/exec"
	"strings"
	"testing"
)

const goSample = `package calc

// Add adds two numbers.
func Add(a, b int) int { return a + b }

// Calc holds state.
type Calc struct{ total int }

// Reset clears the total.
func (c *Calc) Reset() {
	c.total = 0
}

const Limit = 10
`

const pySample = `"""A module."""
import os

DEFAULT = 3


class Base:
    """The base class.

    More text.
    """

    size = 1

    def run(self, x, y=2, *args, **kw):
        """Run it."""
        return x


@decorated
def helper(a, *, b=None):
    return a


async def fetch():
    pass
`

// outline lists a Go file's definitions with their lines, signatures and the
// first line of their documentation, methods under their type.
func TestOutlineOfAGoFile(t *testing.T) {
	in, _ := schemeRig(t, map[string]string{"calc.go": goSample})
	got, err := evalScheme(t, in, `(outline "calc.go")`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"calc.go: 14 lines, 4 definitions (go/parser)", "func Add(a, b int) int  — Add adds two numbers.", "struct Calc  — Calc holds state.", "  ", "method Reset()  — Reset clears the total.", "const Limit"} {
		if !strings.Contains(got, want) {
			t.Errorf("the outline lacks %q:\n%s", want, got)
		}
	}
	// the method is indented under its type
	i, j := strings.Index(got, "struct Calc"), strings.Index(got, "method Reset")
	if i < 0 || j < i || !strings.Contains(got[i:j], "\n  ") {
		t.Errorf("the method is not under its type:\n%s", got)
	}
}

// Python is read by its own ast module: classes with their bases, methods
// with parameters, decorated functions from their decorator, module and
// class variables, and the first docstring line.
func TestOutlineOfAPythonFile(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	in, _ := schemeRig(t, map[string]string{"m.py": pySample})
	got, err := evalScheme(t, in, `(outline "m.py")`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(python ast)", "var DEFAULT", "class Base  — The base class.", "def run(self, x, y=…, *args, **kw)  — Run it.", "var size", "def helper(a, *, b=…)", "async def fetch()"} {
		if !strings.Contains(got, want) {
			t.Errorf("the outline lacks %q:\n%s", want, got)
		}
	}
	// the decorated function starts at its decorator
	if !strings.Contains(got, "20-22     def helper") {
		t.Errorf("the decorated function's lines are off:\n%s", got)
	}
}

// fs-read with symbol returns the definition's lines, not the file, and a
// bare name that two definitions share is refused with the qualified names.
func TestFsReadBySymbol(t *testing.T) {
	in, _ := schemeRig(t, map[string]string{"calc.go": goSample, "two.go": "package p\n\ntype A struct{}\n\nfunc (A) Run() {}\n\ntype B struct{}\n\nfunc (B) Run() {}\n"})
	got, err := evalScheme(t, in, `(fs-read "calc.go" 0 0 "Calc.Reset")`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "method Calc.Reset (in Calc)") || !strings.Contains(got, "c.total = 0") || strings.Contains(got, "func Add") {
		t.Errorf("got:\n%s", got)
	}
	if _, err := evalScheme(t, in, `(fs-read "two.go" 0 0 "Run")`); err == nil || !strings.Contains(err.Error(), "A.Run") || !strings.Contains(err.Error(), "B.Run") {
		t.Errorf("an ambiguous name: %v", err)
	}
	if _, err := evalScheme(t, in, `(fs-read "calc.go" 0 0 "Nope")`); err == nil || !strings.Contains(err.Error(), "no definition named") {
		t.Errorf("a missing name: %v", err)
	}
	// without symbol, fs-read is as before
	if got, err := evalScheme(t, in, `(fs-read "calc.go" 3 2)`); err != nil || !strings.Contains(got, "[lines 3-4 of 14]") {
		t.Errorf("ranged read: %q %v", got, err)
	}
}

// A language nothing parses is outlined by line patterns, nested by
// indentation.
func TestOutlineFallsBackToLinePatterns(t *testing.T) {
	if _, err := exec.LookPath("ctags"); err == nil {
		t.Skip("ctags would read it")
	}
	in, _ := schemeRig(t, map[string]string{"a.js": "export function top() {\n  return 1\n}\n\nclass K {\n    method() {}\n}\n"})
	got, err := evalScheme(t, in, `(outline "a.js")`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "(line patterns)") || !strings.Contains(got, "function top") || !strings.Contains(got, "class K") {
		t.Errorf("got:\n%s", got)
	}
}

// tests-for ranks the test files by the rarest names they share with the
// file, and gives the command that runs each where the layout says: a
// Django tree (tests/runtests.py) gets a runtests label, not a path.
func TestTestsForRanksByWhatTheFileDefines(t *testing.T) {
	in, _ := schemeRig(t, map[string]string{
		"pkg/widgets.py":                    "class SelectDateWidget:\n    pass\n\n\ndef render_widget_attrs():\n    pass\n\n\nclass Widget:\n    pass\n",
		"tests/runtests.py":                 "# the runner\n",
		"tests/forms_tests/test_dates.py":   "from pkg.widgets import SelectDateWidget, render_widget_attrs\n\n\ndef test_a():\n    SelectDateWidget()\n    render_widget_attrs()\n",
		"tests/forms_tests/test_other.py":   "from pkg.widgets import Widget\n\n\ndef test_b():\n    Widget()\n",
		"tests/forms_tests/test_nope.py":    "def test_c():\n    assert 1\n",
		"tests/forms_tests/test_widgets.py": "import pkg.widgets\n\n\ndef test_d():\n    pkg.widgets\n",
	})
	got, err := evalScheme(t, in, `(tests-for "pkg/widgets.py")`)
	if err != nil {
		t.Fatal(err)
	}
	i, j := strings.Index(got, "test_dates.py"), strings.Index(got, "test_other.py")
	if i < 0 || j < 0 || i > j {
		t.Errorf("the file that names more of the definitions should come first:\n%s", got)
	}
	if strings.Contains(got, "test_nope.py") {
		t.Errorf("a test that names nothing was listed:\n%s", got)
	}
	if !strings.Contains(got, "run: python tests/runtests.py forms_tests.test_dates") {
		t.Errorf("no Django label:\n%s", got)
	}
	if !strings.Contains(got, "test_widgets.py") {
		t.Errorf("a test that imports the module by its name was not listed:\n%s", got)
	}
}
