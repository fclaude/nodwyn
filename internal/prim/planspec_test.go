package prim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/scheme"
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

// The harness around a request and the environment it reports give names the
// change does not produce: a task template's fields, its product's name and a
// platform string. Backquoted, a key is still asked for.
func TestRequestNamesLeaveOutATemplatesFieldsAndAPlatformString(t *testing.T) {
	got := requestNames("You are solving one SWE-bench Verified task.\n\nTarget:\n- instance_id: pytest-dev__pytest-5787\n- repository: pytest-dev/pytest\n- base_commit: abc\n\nIssue:\nThe serializer drops the chain. System: Darwin-18.2.0-x86_64-i386-64bit, compiler clang-1000.11.45.5. Fix `base_commit` handling in report_to_serializable.")
	want := []string{"pytest-dev", "base_commit", "report_to_serializable"}
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
	// A name the unchanged code holds whole, and a phrase, are not asked of
	// the change; one the unchanged code holds only inside another is.
	if !headHasName(context.Background(), k.o, "session-id") || headHasName(context.Background(), k.o, "thread-id") {
		t.Errorf("headHasName: session-id is in HEAD's calc.py, thread-id is not")
	}
	write(t, root, "calc.py", "def one():\n    return 10  # X-Session-Id session-id\n", 0o644)
	git("commit", "-q", "-am", "session")
	write(t, root, "calc.py", "def one():\n    return 11\n", 0o644)
	in := scheme.New()
	if err := in.Register(planEvidence(k.o)); err != nil {
		t.Fatal(err)
	}
	v, err := in.EvalString(context.Background(), `(plan-evidence '("session-id" "cache_key" "store = false" "Session") '())`)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.String(); !strings.Contains(got, `"cache_key"`) || strings.Contains(got, "session-id") || strings.Contains(got, "store") || !strings.Contains(got, `"Session"`) {
		t.Errorf("plan-evidence asked the change for %s", got)
	}
	// A step that names a directory is changed by an edit to a file in it.
	write(t, root, "sub/deep.txt", "deeper\n", 0o644)
	v, err = in.EvalString(context.Background(), `(plan-evidence '() '("sub/" "sub" "nothere/"))`)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.String(); !strings.Contains(got, `("unchanged" "nothere/")`) {
		t.Errorf("a directory with a changed file in it: %s", got)
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
	// The change's test support goes in with the test: a fake it rewrote is
	// the test's, not the code under test (commit-bench, dsl13 107251d0).
	write(t, root, "test/fakes/server.py", "refuse = True\n", 0o644)
	status, err = runTestBefore(context.Background(), o, "calc_test.py", "sh", []string{"-c", "grep -q 'refuse = True' test/fakes/server.py && grep -q 'return 10' calc.py"})
	if err != nil || status == 0 {
		t.Fatalf("with the new fake and the old code: status %d, error %v; want it to fail on the code", status, err)
	}
	status, err = runTestBefore(context.Background(), o, "calc_test.py", "sh", []string{"-c", "grep -q 'refuse = True' test/fakes/server.py"})
	if err != nil || status != 0 {
		t.Fatalf("the changed fake is not in the copy: status %d, error %v", status, err)
	}
}

// The documents that hold a name whole, as HEAD has them, most names first;
// a name in too many documents says nothing, and code is no document.
func TestDocsMentioningFindsWhereABehaviourIsWritten(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "docs/adr/0001.md", "`Scanner.Scan` lists blocks first; TextSecret judges one.\n", 0o644)
	write(t, root, "docs/guide.tex", "TextSecret guards egress.\n", 0o644)
	write(t, root, "scan.go", "func (s Scanner) Scan() {} // Scanner.Scan TextSecret\n", 0o644)
	for i := 0; i <= docsMentioningCommon; i++ {
		write(t, root, fmt.Sprintf("docs/n%d.md", i), "every page says Overview\n", 0o644)
	}
	git("add", ".")
	git("commit", "-q", "-m", "docs")
	write(t, root, "docs/adr/0025-ingress-redaction.md", "L1 is the scanner every place uses.\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "adr")
	got, err := docsMentioning(context.Background(), k.o, []string{"Scanner.Scan", "TextSecret", "Overview", "Run", "nowhere"}, []string{"redact", "l1"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"docs/adr/0001.md", "Scanner.Scan, TextSecret"}, {"docs/adr/0025-ingress-redaction.md", "redact"}, {"docs/guide.tex", "TextSecret"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("docsMentioning = %q, want %q", got, want)
	}
}

// git-intend marks a new file as intended for the next commit, so git
// ls-files lists it; a tracked, an ignored and an outside path are left alone.
func TestGitIntendMakesANewFileListed(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, ".gitignore", "*.log\n", 0o644)
	git("add", ".gitignore")
	git("commit", "-q", "-m", "ignore")
	write(t, root, "sub/new_test.go", "package sub\n", 0o644)
	write(t, root, "run.log", "x\n", 0o644)
	in := scheme.New()
	if err := in.Register(gitIntend(k.o)); err != nil {
		t.Fatal(err)
	}
	v, err := in.EvalString(context.Background(), `(git-intend '("sub/new_test.go" "plants.txt" "run.log" "../escape.txt" "missing.go"))`)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.String(); got != `("sub/new_test.go")` {
		t.Errorf("git-intend marked %s", got)
	}
	if out := git("ls-files"); !strings.Contains(out, "sub/new_test.go") || strings.Contains(out, "run.log") {
		t.Errorf("ls-files:\n%s", out)
	}
}
