package prim

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The names a request gives: hyphenated, underscored and dotted words,
// names with a capital inside, spans in backquotes; not links, issue
// numbers or versions. The request of commit-bench task 556d6d12, whose
// session-id a planner dropped.
func TestRequestNamesAreTheNamesARequestGives(t *testing.T) {
	got := requestNames("opencode found the same miss and now sends the headers Codex names its session by, session-id, thread-id and x-client-request-id (anomalyco/opencode#53767). A request whose credential was granted chatgpt.tokens.use.direct now carries the session's cache key as those headers too, unless cache_key is off; see https://example.com/a-b and v1.2.3. `X-Session-Id` stays, and readResponses too.")
	want := []string{"session-id", "thread-id", "x-client-request-id", "chatgpt.tokens.use.direct", "cache_key", "X-Session-Id", "readResponses"}
	// A marker and a file every task names, and a product's name, are not
	// asked for.
	for _, n := range requestNames("Read AGENTS.md; end with NODWYN_RUN_COMPLETE. ChatGPT and OpenAI call it.") {
		t.Errorf("asked for %q", n)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("requestNames = %q, want %q", got, want)
	}
}

// A name is found whole and in any case: session-id is in "Session-Id:" and
// in a quoted "session-id", not in X-Session-Id.
func TestHasNameFindsWholeNames(t *testing.T) {
	for _, c := range []struct {
		text string
		want bool
	}{
		{`req.Header.Set("Session-Id", key)`, true},
		{`req.Header.Set("session-id", key)`, true},
		{`req.Header.Set("X-Session-Id", key)`, false},
		{`hreq.Header.Set("session-identity", key)`, false},
		{"session-id\n", true},
	} {
		if got := hasName(c.text, "session-id"); got != c.want {
			t.Errorf("hasName(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// plan-evidence sets a plan's names and paths against the change: a name
// only the change's unchanged lines hold is missing, and so is one only a
// removed line held; a new file counts whole.
func TestPlanEvidenceReadsTheChange(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "calc.py", "def one():\n    return 1  # session-id\n", 0o644)
	write(t, root, "docs.md", "notes\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "code")
	write(t, root, "calc.py", "def one():\n    return 10  # X-Session-Id\n", 0o644)
	write(t, root, "flags.py", "THREAD = 'thread-id'\n", 0o644)
	text, changed, err := changeText(context.Background(), k.o)
	if err != nil {
		t.Fatal(err)
	}
	if hasName(text, "session-id") || !hasName(text, "thread-id") || !hasName(text, "X-Session-Id") {
		t.Errorf("change text:\n%s", text)
	}
	if !changed["calc.py"] || !changed["flags.py"] || changed["docs.md"] {
		t.Errorf("changed paths %v", changed)
	}
}

// test-before runs a command on a copy of HEAD with the working tree's test
// file put in: the unchanged code, so a test of the change fails there, and
// one that passes there does not show the change.
func TestTestBeforeRunsOnTheUnchangedCode(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "calc.py", "def one():\n    return 1\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "code")
	write(t, root, "calc.py", "def one():\n    return 10\n", 0o644)
	write(t, root, "calc_test.py", "# checks one() is 10\n", 0o644)
	o := k.o
	o.EnvAllow = []string{"PATH"}
	status, err := runTestBefore(context.Background(), o, "calc_test.py", "sh", []string{"-c", "grep -q 'return 10' calc.py"})
	if err != nil || status == 0 {
		t.Fatalf("a check of the change on the unchanged code: status %d, error %v; want it to fail", status, err)
	}
	status, err = runTestBefore(context.Background(), o, "calc_test.py", "sh", []string{"-c", "grep -q 'checks one' calc_test.py"})
	if err != nil || status != 0 {
		t.Fatalf("the test file is not in the copy: status %d, error %v", status, err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "calc.py")); string(data) != "def one():\n    return 10\n" {
		t.Errorf("the working tree changed: %q", data)
	}
}

// The lines of a check's output that say something failed, masked so that
// two runs of the same failure read alike: make validate's on commit-bench,
// run once with a change and once without, and a passing line left out.
func TestFailureLinesAreHowAFailureReads(t *testing.T) {
	with := failureLines("PASS  gofmt                  4 changed Go files\nFAIL  make accept            staticcheck: No such file or directory (exit 127, 29s)\nvalidate: FAIL (1 failed, 0 warnings)\n")
	without := failureLines("PASS  gofmt   0 changed Go files\nFAIL  make accept   staticcheck: No such file or directory (exit 127, 31.2s)\n\nvalidate: FAIL (1 failed, 0 warnings)\nok  \tpkg\t0.41s\n")
	if !reflect.DeepEqual(with, without) || len(with) != 2 {
		t.Errorf("failureLines:\n%q\n%q", with, without)
	}
	if got := failureLines("--- PASS: TestErrorHandling (0.00s)\nok  \tgithub.com/x/y\t0.3s\n"); len(got) != 0 {
		t.Errorf("a passing run's lines: %q", got)
	}
}

// check-before runs the check on a clone of HEAD: the working tree's change
// is not in it, and git there sees nothing changed.
func TestCheckBeforeRunsOnACleanCloneOfHead(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "calc.py", "def one():\n    return 1\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "code")
	write(t, root, "calc.py", "def one():\n    return 10\n", 0o644)
	o := k.o
	o.EnvAllow = []string{"PATH", "HOME"}
	status, out, err := runCheckBefore(context.Background(), o, "sh", []string{"-c", "grep -q 'return 1$' calc.py && test -z \"$(git status --porcelain)\" && echo error: none here >&2; exit 3"})
	if err != nil || status != 3 {
		t.Fatalf("status %d, error %v, output %q", status, err, out)
	}
	if lines := failureLines(out); !reflect.DeepEqual(lines, []string{"error: none here"}) {
		t.Errorf("the run on the clone printed %q", out)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "calc.py")); string(data) != "def one():\n    return 10\n" {
		t.Errorf("the working tree changed: %q", data)
	}
}
