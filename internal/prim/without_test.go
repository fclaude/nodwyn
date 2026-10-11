package prim

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// check-without takes each part of the change out in turn and runs the
// check again: a hunk of a changed code file, a whole new file, a whole
// deleted file. The check here looks only at the first hunk, so it fails
// without that one and passes without the others: those are the parts it
// does not test. A comment-only hunk, a test file and a document are not
// parts, and every file ends as the change left it (ADR 32, amended
// 2026-10-11).
func TestCheckWithoutTakesOutEachPart(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "calc.py", "def one():\n    return 1\n\n\ndef two():\n    return 2\n", 0o644)
	write(t, root, "old.py", "x = 1\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "code")
	changed := "# a comment\ndef one():\n    return 10\n\n\ndef two():\n    return 20\n"
	write(t, root, "calc.py", changed, 0o644)                     // three hunks: a comment, one's return, two's return
	write(t, root, "helper.py", "def help():\n    pass\n", 0o644) // a new file
	write(t, root, "calc_test.py", "assert True\n", 0o644)        // a new test: not a part
	write(t, root, "NOTES.md", "notes\n", 0o644)                  // a document: not a part
	if err := os.Remove(filepath.Join(root, "old.py")); err != nil {
		t.Fatal(err)
	}
	o := k.o
	o.EnvAllow = []string{"PATH"}
	parts, statuses, err := runWithout(context.Background(), o, "sh", []string{"-c", "grep -q 'return 10' calc.py"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for i, p := range parts {
		if statuses[i].err != nil {
			t.Fatalf("%s: %v", p.path, statuses[i].err)
		}
		got[p.kind+" "+p.path+":"+strconv.Itoa(p.line)] = statuses[i].status
	}
	want := map[string]int{
		"hunk calc.py:3":        1, // one's return taken out: the check fails
		"hunk calc.py:7":        0, // two's return taken out: the check still passes
		"new file helper.py:0":  0,
		"deleted file old.py:0": 0,
	}
	if len(got) != len(want) {
		t.Fatalf("parts %v, want %v", got, want)
	}
	for part, status := range want {
		if s, ok := got[part]; !ok || s != status {
			t.Errorf("part %s: status %d (present %v), want %d; all %v", part, s, ok, status, got)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(root, "calc.py")); string(data) != changed {
		t.Errorf("calc.py was not put back:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(root, "helper.py")); err != nil {
		t.Errorf("the new file was not put back: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "old.py")); !os.IsNotExist(err) {
		t.Errorf("the deleted file came back: %v", err)
	}
}

// The runs share one time budget: a check slower than the budget is stopped
// on the first part, the parts after it are not reported, and the files end
// as the change left them.
func TestCheckWithoutStopsAtItsBudget(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "calc.py", "def one():\n    return 1\n\n\ndef two():\n    return 2\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "code")
	changed := "def one():\n    return 10\n\n\ndef two():\n    return 20\n"
	write(t, root, "calc.py", changed, 0o644)
	write(t, root, "helper.py", "def help():\n    pass\n", 0o644)
	defer func(b time.Duration) { withoutBudget = b }(withoutBudget)
	withoutBudget = 200 * time.Millisecond
	o := k.o
	o.EnvAllow = []string{"PATH"}
	start := time.Now()
	parts, statuses, err := runWithout(context.Background(), o, "sh", []string{"-c", "sleep 5"})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("took %s, past the budget", took)
	}
	if len(parts) != 1 || statuses[0].err == nil {
		t.Fatalf("parts %v statuses %v, want the first part alone, timed out", parts, statuses)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "calc.py")); string(data) != changed {
		t.Errorf("calc.py was not put back:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(root, "helper.py")); err != nil {
		t.Errorf("the new file was not put back: %v", err)
	}
}
