package prim

import (
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fclaude/brote/scheme"
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

// The expectations of a test are also the literal a want variable holds and
// the phrase a Contains check looks for, not only the cases of a table and
// testify's asserts. On 5040f9fe the request added the response's
// Content-Type to an error; the model reworded the error so that the phrase an
// existing test pins was split, and edited that test's `want := "..."` and
// not a case or an assert, so nothing named it (ADR 32, amended 2026-10-16).
// Not named: a want that is computed, a table's opening line, a comparison,
// a literal that only changed its variable's name, and a case that moved
// from a map to a direct assertion, which the replay of 94f5cac4 named.
func TestRemovedExpectationsAreAlsoWantsAndPhraseChecks(t *testing.T) {
	head := map[string]string{
		"llm/responses_test.go": "package llm\n\nfunc TestMissingTerminal(t *testing.T) {\n" +
			"\t\twant := \"Responses stream ended without a terminal response\"\n" +
			"\t\tswitch {\n\t\tcase failed:\n\t\t\twant = \"the response failed: quota refused\"\n\t\t}\n" +
			"\t\tif err == nil || !strings.Contains(err.Error(), want) {\n\t\t\tt.Fatal(err)\n\t\t}\n" +
			"\t\tif !strings.HasPrefix(err.Error(), \"openai: \") {\n\t\t\tt.Fatal(err)\n\t\t}\n" +
			"\t\twantErr := true\n\t\texpected := 3\n\t\twantMsg := \"kept phrase\"\n" +
			"\t\twant2 := compute()\n\t\twant3 := []string{\n\t\tvar wantTyped string = \"typed\"\n}\n",
		"idx/idx_test.go": "package idx\n\nfunc TestErrors(t *testing.T) {\n" +
			"\tfor src, want := range map[string]string{\n" +
			"\t\t`(index-grep \"pkg\" \"(\")`: \"invalid regular expression\",\n\t} {\n\t\tcheck(src, want)\n\t}\n}\n",
		"py/test_calc.py": "def test_one():\n    expected = \"one\"\n    want_n = 1\n    assert_it(expected, want_n)\n",
	}
	now := map[string]string{
		"llm/responses_test.go": "package llm\n\nfunc TestMissingTerminal(t *testing.T) {\n" +
			"\t\twant := \"ended without a terminal response\"\n" +
			"\t\tswitch {\n\t\tcase failed:\n\t\t\twant = \"the response failed: quota refused\"\n\t\t}\n" +
			"\t\tif err == nil || !strings.Contains(err.Error(), want) {\n\t\t\tt.Fatal(err)\n\t\t}\n" +
			"\t\tif !strings.HasPrefix(err.Error(), \"openai: bad\") {\n\t\t\tt.Fatal(err)\n\t\t}\n" +
			"\t\twantErr := false\n\t\texpected := 4\n\t\tphrase := \"kept phrase\"\n" +
			"\t\twant2 := other()\n\t\twant3 := []string{\n\t\tvar wantTyped string = \"typed\"\n}\n",
		"idx/idx_test.go": "package idx\n\nfunc TestErrors(t *testing.T) {\n" +
			"\tif _, err := eval(`(index-grep \"pkg\" \"(\")`); err == nil || !strings.Contains(err.Error(), \"invalid regular expression\") {\n\t\tt.Errorf(\"%v\", err)\n\t}\n}\n",
		"py/test_calc.py": "def test_one():\n    expected = \"uno\"\n    want_n = 1\n    assert_it(expected, want_n)\n",
	}
	var got []string
	for _, l := range removedExpectations(fakeChange(head, now)) {
		got = append(got, fmt.Sprintf("%s:%d %s", l.path, l.line, l.text))
	}
	want := []string{
		"llm/responses_test.go:4 want := \"Responses stream ended without a terminal response\"",
		"llm/responses_test.go:12 if !strings.HasPrefix(err.Error(), \"openai: \") {",
		"llm/responses_test.go:15 wantErr := true",
		"llm/responses_test.go:16 expected := 3",
		"py/test_calc.py:2 expected = \"one\"",
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("removed expectations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// What an expectation line is: the forms above, and not a comparison, a
// computed want, a rune that holds a quote, or a comment.
func TestExpectationLinesOfWantsAndPhraseChecks(t *testing.T) {
	for _, c := range []struct {
		rel, line string
		want      bool
	}{
		{"a_test.go", `want := "x"`, true},
		{"a_test.go", `var want string = "x"`, true},
		{"a_test.go", `wantErr = true`, true},
		{"a_test.go", `expected := 3`, true},
		{"a_test.go", `expectedMsg := ` + "`raw`", true},
		{"a_test.go", `want := []string{"a", "b"}`, true},
		{"a_test.go", `want_err := nil`, true},
		{"a_test.go", `if !strings.Contains(err.Error(), "phrase") {`, true},
		{"a_test.go", `if strings.HasSuffix(got, "x") {`, true},
		{"a_test.go", `if !bytes.Contains(out, []byte("x")) {`, true},
		{"a_test.go", `want := compute("x")`, false},
		{"a_test.go", `want := []string{`, false},
		{"a_test.go", `want == "x"`, false},
		{"a_test.go", `wanted := "x"`, false},
		{"a_test.go", `wantX.y = "x"`, false},
		{"a_test.go", `if !strings.Contains(err.Error(), want) {`, false},
		{"a_test.go", `case strings.Contains(body, "response.failed"):`, false},
		{"a_test.go", `if req.session != key || inBody != (setting == "") || strings.Contains(req.body, id) {`, false},
		{"a_test.go", `want := "x" // a comment`, true},
		{"a_test.go", `// want := "x"`, false},
		{"a_test.go", `if strings.IndexRune(s, '"') >= 0 {`, false},
		{"test_a.py", `expected = "x"`, true},
		{"test_a.py", `want_n = 2`, true},
		{"test_a.py", `expected = compute()`, false},
		{"test_a.py", `if "x" in text:`, false},
	} {
		if got := expectationLine(c.rel, c.line); got != c.want {
			t.Errorf("expectationLine(%q, %q) = %v, want %v", c.rel, c.line, got, c.want)
		}
	}
}

// A line's string literals are Go's "..." and `...` and Python's also '...',
// as written; a rune, a comment and a literal that does not close are none.
// The expectations of test files of other languages and names are read too
// (ADR 32, amended 2026-10-16): Django's tests.py, Jest's x.test.js,
// Rust's assert_eq!, and a const expected.
func TestRemovedExpectationsOfOtherLanguages(t *testing.T) {
	head := map[string]string{
		"tests/forms/tests.py": "class T(TestCase):\n    def test_a(self):\n        self.assertEqual(f(1), 'one')\n",
		"web/cart.test.js":     "test('adds', () => {\n  const expected = 'two';\n  expect(add(1, 1)).toBe(2); // sum\n});\n",
		"src/lib.rs":           "pub fn f() {}\n",
		"tests/it.rs":          "#[test]\nfn adds() {\n    assert_eq!(add(1, 1), 2);\n}\n",
		"docs/tests/readme.md": "assert x == 1\n",
		"src/main.js":          "expect(1).toBe(1)\n",
	}
	now := map[string]string{
		"tests/forms/tests.py": "class T(TestCase):\n    def test_a(self):\n        self.assertEqual(f(1), 'uno')\n",
		"web/cart.test.js":     "test('adds', () => {\n  const expected = 'three';\n  expect(add(1, 1)).toBe(3); // sum\n});\n",
		"src/lib.rs":           "pub fn f() {}\n",
		"tests/it.rs":          "#[test]\nfn adds() {\n    assert_eq!(add(1, 1), 3);\n}\n",
		"docs/tests/readme.md": "assert x == 2\n",
		"src/main.js":          "expect(1).toBe(2)\n",
	}
	var got []string
	for _, l := range removedExpectations(fakeChange(head, now)) {
		got = append(got, fmt.Sprintf("%s:%d %s", l.path, l.line, l.text))
	}
	want := []string{
		"tests/forms/tests.py:3 self.assertEqual(f(1), 'one')",
		"tests/it.rs:3 assert_eq!(add(1, 1), 2);",
		"web/cart.test.js:2 const expected = 'two';",
		"web/cart.test.js:3 expect(add(1, 1)).toBe(2); // sum",
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expectations %q, want %q", got, want)
	}
}

func TestStringLiterals(t *testing.T) {
	if got := stringLiterals("a_test.go", `f("a\"b", `+"`c`"+`, '"', 'x') // "z"`); !reflect.DeepEqual(got, []string{`a\"b`, "c"}) {
		t.Errorf("Go literals: %q", got)
	}
	if got := stringLiterals("a_test.py", `f("a", 'b') # "z"`); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Python literals: %q", got)
	}
	if got := stringLiterals("a_test.go", `f("open`); len(got) != 0 {
		t.Errorf("an open literal: %q", got)
	}
	if !kept([]string{"a", "b"}, [][]string{{"b"}, {"b", "a", "c"}}) || kept([]string{"a", "b"}, [][]string{{"a"}, {"b"}}) || kept(nil, [][]string{{"a"}}) || kept([]string{""}, [][]string{{""}}) {
		t.Error("a line is kept when one added line holds all its literals, and only then")
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
