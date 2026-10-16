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
// production code to make an old test pass, and a test for each side of an
// added branch.
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
			"if the test's setup contradicts what the request names, fix the setup",
			"A branch you add gets a test for each side of it.",
		} {
			if !strings.Contains(sys, want) {
				t.Errorf("the %s prompt lacks %q", name, want)
			}
		}
	}
}

// A test file is told by its name or its folder.
func TestTestFilesAreToldByNameAndFolder(t *testing.T) {
	t.Parallel()
	r := newRig(t, true)
	src := `(map test-file-path? '("calc/calc_test.go" "tests/helpers.py" "pkg/test_x.py" "pkg/x_test.py" "web/a.test.js" "web/a.spec.ts" "src/calc.go" "src/inspect.go" "docs/testing.md" "main.go"))`
	v, err := r.s.Eval(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := v.String(), "(#t #t #t #t #t #t #f #f #f #f)"; got != want {
		t.Errorf("%s = %s, want %s", src, got, want)
	}
}
