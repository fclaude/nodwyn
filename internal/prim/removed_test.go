package prim

import (
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// fakeChange is a change given as the files HEAD has and the files there are
// now, for the finish notes that read one (changeFiles).
func fakeChange(head, now map[string]string) changeFiles {
	seen := map[string]bool{}
	var paths []string
	for _, files := range []map[string]string{head, now} {
		for p := range files {
			if !seen[p] && head[p] != now[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	sort.Strings(paths)
	get := func(files map[string]string) func(string) ([]byte, bool) {
		return func(p string) ([]byte, bool) {
			s, ok := files[p]
			return []byte(s), ok
		}
	}
	return changeFiles{
		paths: paths,
		head:  get(head),
		now:   get(now),
		dir: func(d string) []string {
			var out []string
			for p := range now {
				if path.Dir(p) == d {
					out = append(out, p)
				}
			}
			sort.Strings(out)
			return out
		},
	}
}

func defNames(defs []removedDef) []string {
	var out []string
	for _, d := range defs {
		out = append(out, d.kind+" "+d.name+" "+d.path)
	}
	return out
}

// The definitions HEAD has in a changed file that its package no longer
// defines are named: dsl16 and Pi deleted evalOr, which the hidden test of
// 67a1f950 called, once they had rerouted its callers (ADR 32, amended
// 2026-10-15). A name in another case counts (a test calls it by name), and
// so do a method, a constant and a deleted file's functions. What is not
// removed is not named: a function moved to another file of the package, a
// name the request gives (whole, or as words: "max-retries" for
// MaxRetries), a test, a file that does not parse now. Python's and
// Scheme's definitions are read too.
func TestRemovedDefinitionsAreTheOnesGoneFromThePackage(t *testing.T) {
	head := map[string]string{
		"rpc/a.go": `package rpc

const MaxRetries = 3
const limit = 2

type T struct{}

func (t *T) m() {}

func evalOr(p string) string { return p }

func rootRefusal(p string) string { return evalOr(p) }

func moved() {}

func helper() {}
`,
		"rpc/gone.go":      "package rpc\n\nfunc goneHelper() {}\n",
		"rpc/a_test.go":    "package rpc\n\nfunc TestOld(t *testing.T) {}\n",
		"rpc/broken.go":    "package rpc\n\nfunc stays() {}\n",
		"tools/mod.py":     "def kept():\n    pass\n\n\nclass Old:\n    pass\n\n\ndef dropped():\n    pass\n",
		"loop/x.scm":       "(define (kept) 1)\n(define dropped-define 2)\n",
		"docs/guide.md":    "# evalOr\n",
		"rpc/unchanged.go": "package rpc\n\nfunc other() {}\n",
	}
	now := map[string]string{
		"rpc/a.go": `package rpc

type T struct{}

func rootRefusal(p string) string { return resolve(p) }

func Helper() {}
`,
		"rpc/b.go":         "package rpc\n\nfunc moved() {}\n\nfunc resolve(p string) string { return p }\n",
		"rpc/a_test.go":    "package rpc\n",
		"rpc/broken.go":    "package rpc\n\nfunc stays( {\n",
		"tools/mod.py":     "def kept():\n    return 1\n",
		"loop/x.scm":       "(define (kept) 2)\n",
		"docs/guide.md":    "# resolve\n",
		"rpc/unchanged.go": "package rpc\n\nfunc other() {}\n",
	}
	// At most removalsMaxDefs (6) are named, in path and line order: the
	// Python and Scheme files are read alone below.
	only := func(files map[string]string, dir string) map[string]string {
		out := map[string]string{}
		for p, s := range files {
			if strings.HasPrefix(p, dir) {
				out[p] = s
			}
		}
		return out
	}
	got := defNames(removedDefinitions(fakeChange(only(head, "rpc/"), only(now, "rpc/")), "Drop max-retries from the RPC client."))
	want := []string{
		"const limit rpc/a.go",
		"method T.m rpc/a.go",
		"func evalOr rpc/a.go",
		"func helper rpc/a.go",
		"func goneHelper rpc/gone.go",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("removed definitions:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	delete(head, "rpc/a.go")
	delete(now, "rpc/a.go")
	delete(head, "rpc/gone.go")
	got = defNames(removedDefinitions(fakeChange(head, now), ""))
	want = []string{"define dropped-define loop/x.scm", "def Old tools/mod.py", "def dropped tools/mod.py"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("removed definitions in Python and Scheme:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A change that removes nothing, or whose removals the request asks for, has
// nothing to say: on the benchmark's patches the request's own words named
// the removed WorkspaceKind ("the guard's workspace-kind checker is gone")
// and firstKnown ("judged only the first known finding"). A plain word for a
// method's type (server) does not name its methods.
func TestRemovedDefinitionsTheRequestNamesAreNotNamed(t *testing.T) {
	head := map[string]string{"g/k.go": `package g

type WorkspaceKind struct{}

func (k WorkspaceKind) Check() {}

func firstKnown() {}

type server struct{}

func (s *server) knownFolder() {}

func kept() {}
`}
	now := map[string]string{"g/k.go": "package g\n\ntype server struct{}\n\nfunc kept() { _ = 1 }\n"}
	request := "The guard's workspace-kind checker is gone. TextSecret judged only the first known finding. The server checks every folder."
	if got := defNames(removedDefinitions(fakeChange(head, now), request)); !reflect.DeepEqual(got, []string{"method server.knownFolder g/k.go"}) {
		t.Fatalf("removed definitions %v, want only server.knownFolder", got)
	}
	same := map[string]string{"g/k.go": head["g/k.go"]}
	edited := map[string]string{"g/k.go": strings.Replace(head["g/k.go"], "func kept() {}", "func kept() { _ = 2 }", 1)}
	if got := removedDefinitions(fakeChange(same, edited), ""); len(got) != 0 {
		t.Fatalf("an edit that removes nothing named %v", defNames(got))
	}
}

// The expectations of existing tests that a change removes or rewrites are
// named: a case of a table, or an assertion that carries its expectation.
// Pi deleted the negative case its new JWT pattern broke (ef04de9c) and
// turned the visible `1 0` error case into `1 -1` (94f5cac4), and its own
// runs then passed (ADR 32, amended 2026-10-15). Not named: a case gofmt
// realigned, or whose trailing comment changed, one moved to another test
// file, a reworded t.Errorf, a comment, a setup line, and lines added.
func TestRemovedExpectationsAreCasesAndAssertions(t *testing.T) {
	head := map[string]string{
		"l1/l1_test.go": "package l1\n\nfunc TestFalsePositives(t *testing.T) {\n\tfor _, s := range []string{\n" +
			"\t\t\"eyJhbGciOiJSUzI1NiJ9.eyJ0eXAi:JWT.x\", // malformed (has a colon)\n" +
			"\t\t\"1.2.3\",                               // version\n" +
			"\t\t\"api.openai.com\",                      // host\n" +
			"\t} {\n\t\tif len(Scan(s)) != 0 {\n\t\t\tt.Errorf(\"false positive on %q\", s)\n\t\t}\n\t}\n}\n",
		"prim/range_test.go": "package prim\n\n// The errors, by call.\nvar rangeErrors = map[string]string{\n" +
			"\t`(fs-read \"big.txt\" 1 0)`: \"count must be at least 1\",\n" +
			"\t`(fs-read \"big.txt\" 2 x)`: \"count is a number\",\n}\n\n" +
			"func TestRange(t *testing.T) {\n\tdir := t.TempDir()\n\tassert.Equal(t, 3, count(dir))\n}\n",
		"prim/a_test.go":   "package prim\n\nvar cases = []string{\n\t\"moves\",\n\t\"stays\",\n}\n",
		"prim/b_test.go":   "package prim\n\nvar more = []string{\n\t\"other\",\n}\n",
		"py/test_calc.py":  "def test_one():\n    assert one() == 1\n",
		"prim/new_test.go": "",
	}
	delete(head, "prim/new_test.go")
	now := map[string]string{
		"l1/l1_test.go": "package l1\n\nfunc TestFalsePositives(t *testing.T) {\n\tfor _, s := range []string{\n" +
			"\t\t\"1.2.3\",          // a version\n" +
			"\t\t\"api.openai.com\", // host\n" +
			"\t} {\n\t\tif len(Scan(s)) != 0 {\n\t\t\tt.Errorf(\"%q was flagged\", s)\n\t\t}\n\t}\n}\n",
		"prim/range_test.go": "package prim\n\nvar rangeErrors = map[string]string{\n" +
			"\t`(fs-read \"big.txt\" 1 -1)`: \"count must be at least 1\",\n" +
			"\t`(fs-read \"big.txt\" 2 x)`:  \"count is a number\",\n}\n\n" +
			"func TestRange(t *testing.T) {\n\tdir := filepath.Join(t.TempDir(), \"x\")\n\tassert.Equal(t, 4, count(dir))\n}\n",
		"prim/a_test.go":   "package prim\n\nvar cases = []string{\n\t\"stays\",\n}\n",
		"prim/b_test.go":   "package prim\n\nvar more = []string{\n\t\"other\",\n\t\"moves\",\n}\n",
		"py/test_calc.py":  "def test_one():\n    assert one() == 2\n",
		"prim/new_test.go": "package prim\n\nvar added = []string{\n\t\"new\",\n}\n",
	}
	var got []string
	for _, l := range removedExpectations(fakeChange(head, now)) {
		got = append(got, fmt.Sprintf("%s:%d %s", l.path, l.line, l.text))
	}
	want := []string{
		"l1/l1_test.go:5 \"eyJhbGciOiJSUzI1NiJ9.eyJ0eXAi:JWT.x\", // malformed (has a colon)",
		"prim/range_test.go:11 assert.Equal(t, 3, count(dir))",
		"prim/range_test.go:5 `(fs-read \"big.txt\" 1 0)`: \"count must be at least 1\",",
		"py/test_calc.py:2 assert one() == 1",
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("removed expectations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// (change-removals request) reads the working tree against HEAD and answers
// with an alist of definitions and expectations.
func TestChangeRemovalsReadsTheWorkingTree(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "rpc/a.go", "package rpc\n\nfunc evalOr() int { return 1 }\n\nfunc Root() int { return evalOr() }\n", 0o644)
	write(t, root, "rpc/a_test.go", "package rpc\n\nvar cases = []int{\n\t1,\n\t2,\n}\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "rpc")
	write(t, root, "rpc/a.go", "package rpc\n\nfunc Root() int { return 1 }\n", 0o644)
	write(t, root, "rpc/a_test.go", "package rpc\n\nvar cases = []int{\n\t1,\n}\n", 0o644)
	in := scheme.New()
	if err := in.Register(changeRemovalsPrim(k.o)); err != nil {
		t.Fatal(err)
	}
	v, err := in.EvalString(t.Context(), `(change-removals "make Root return 1")`)
	if err != nil {
		t.Fatal(err)
	}
	got := v.String()
	for _, want := range []string{`("name" . "evalOr")`, `("path" . "rpc/a.go")`, `("kind" . "func")`, `("line" . 3)`, `("path" . "rpc/a_test.go")`, `("line" . 5)`, `("text" . "2,")`} {
		if !strings.Contains(got, want) {
			t.Errorf("change-removals lacks %s: %s", want, got)
		}
	}
	v, err = in.EvalString(t.Context(), `(change-removals "remove evalOr")`)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.String(); !strings.Contains(got, `("definitions")`) {
		t.Errorf("a removal the request names was named: %s", got)
	}
}
