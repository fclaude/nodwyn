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
// check again: a hunk of a changed code file, a whole new file. The check
// here looks only at the first hunk, so it fails without that one and passes
// without the others: those are the parts it does not test. A comment-only
// hunk, a test file and a document are not parts, and every file ends as the
// change left it (ADR 32, amended 2026-10-11).
func TestCheckWithoutTakesOutEachPart(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "calc.py", "def one():\n    return 1\n\n\ndef two():\n    return 2\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "code")
	changed := "# a comment\ndef one():\n    return 10\n\n\ndef two():\n    return 20\n"
	write(t, root, "calc.py", changed, 0o644)                     // three hunks: a comment, one's return, two's return
	write(t, root, "helper.py", "def help():\n    pass\n", 0o644) // a new file
	write(t, root, "calc_test.py", "assert True\n", 0o644)        // a new test: not a part
	write(t, root, "NOTES.md", "notes\n", 0o644)                  // a document: not a part
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
		if statuses[i].status == 0 && !statuses[i].same {
			t.Errorf("%s:%d: a check that prints nothing printed something else", p.path, p.line)
		}
		got[p.kind+" "+p.path+":"+strconv.Itoa(p.line)] = statuses[i].status
	}
	want := map[string]int{
		"hunk calc.py:3":       1, // one's return taken out: the check fails
		"hunk calc.py:7":       0, // two's return taken out: the check still passes
		"new file helper.py:0": 0,
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
}

// What a change deletes is no part: a deleted file, and a hunk that only
// deletes lines (here two() and the comment above it, with a comment added
// in their place). A check that still passes with a deletion put back cannot
// be extended to fail without code that is gone, and on commit-bench such
// parts were 7 of the 69 named, one message naming nine deleted empty logs
// (ADR 32, amended 2026-10-15). The hunk beside them that changes one()'s
// return is still a part.
func TestCheckWithoutLeavesDeletionsAlone(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "calc.py", "def one():\n    return 1\n\n\n# two is two\ndef two():\n    return 2\n\n\ndef three():\n    return 3\n", 0o644)
	write(t, root, "old.py", "x = 1\n", 0o644)
	write(t, root, "empty.log", "", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "code")
	changed := "def one():\n    return 10\n\n\n# two is gone\n\n\ndef three():\n    return 3\n"
	write(t, root, "calc.py", changed, 0o644)
	for _, f := range []string{"old.py", "empty.log"} {
		if err := os.Remove(filepath.Join(root, f)); err != nil {
			t.Fatal(err)
		}
	}
	o := k.o
	o.EnvAllow = []string{"PATH"}
	parts, statuses, err := runWithout(context.Background(), o, "sh", []string{"-c", "grep -q 'return 10' calc.py"})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].kind != "hunk" || parts[0].line != 2 || statuses[0].status != 1 {
		var got []string
		for _, p := range parts {
			got = append(got, p.kind+" "+p.path+":"+strconv.Itoa(p.line))
		}
		t.Fatalf("parts %v, want only one()'s return (hunk calc.py:2, which the check fails without)", got)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "calc.py")); string(data) != changed {
		t.Errorf("calc.py was not put back:\n%s", data)
	}
	for _, f := range []string{"old.py", "empty.log"} {
		if _, err := os.Stat(filepath.Join(root, f)); !os.IsNotExist(err) {
			t.Errorf("the deleted %s came back: %v", f, err)
		}
	}
}

// A check whose exit status cannot fail (`...; echo EXIT=$?`, `|| true`)
// still tells a part apart when it prints something else without it: the
// part counts as tested, and only the parts it prints the same without are
// the check's blind spots (amended 2026-10-12).
func TestCheckWithoutComparesWhatTheCheckPrints(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "calc.py", "def one():\n    return 1\n\n\ndef two():\n    return 2\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "code")
	write(t, root, "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 20\n", 0o644)
	o := k.o
	o.EnvAllow = []string{"PATH"}
	parts, statuses, err := runWithout(context.Background(), o, "sh", []string{"-c", "grep -c 'return 10' calc.py; echo EXIT=0"})
	if err != nil {
		t.Fatal(err)
	}
	same := map[int]bool{}
	for i, p := range parts {
		if statuses[i].err != nil || statuses[i].status != 0 {
			t.Fatalf("%s:%d: status %d, error %v; the check always exits 0", p.path, p.line, statuses[i].status, statuses[i].err)
		}
		same[p.line] = statuses[i].same
	}
	if len(same) != 2 || same[2] || !same[6] {
		t.Errorf("same by line %v, want line 2 (one's return, which the check counts) printing something else and line 6 the same", same)
	}
}

// A check that does not pass again with the whole change in place says
// nothing about the parts: check-without fails rather than report them.
func TestCheckWithoutNeedsTheCheckToPass(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "calc.py", "def one():\n    return 1\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "code")
	changed := "def one():\n    return 10\n"
	write(t, root, "calc.py", changed, 0o644)
	o := k.o
	o.EnvAllow = []string{"PATH"}
	if _, _, err := runWithout(context.Background(), o, "sh", []string{"-c", "exit 3"}); err == nil {
		t.Fatal("no error for a check that exits 3 with the whole change")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "calc.py")); string(data) != changed {
		t.Errorf("calc.py changed:\n%s", data)
	}
}

// The runs share one time budget, the run with the whole change included: a
// check slower than what is left is stopped, the parts after it are not
// reported, and the files end as the change left them.
func TestCheckWithoutStopsAtItsBudget(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "calc.py", "def one():\n    return 1\n\n\ndef two():\n    return 2\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "code")
	changed := "def one():\n    return 10\n\n\ndef two():\n    return 20\n"
	write(t, root, "calc.py", changed, 0o644)
	write(t, root, "helper.py", "def help():\n    pass\n", 0o644)
	defer func(b time.Duration) { withoutBudget = b }(withoutBudget)
	withoutBudget = time.Second
	o := k.o
	o.EnvAllow = []string{"PATH"}
	start := time.Now()
	// Fast while two's return is in place, slow without it.
	parts, statuses, err := runWithout(context.Background(), o, "sh", []string{"-c", "grep -q 'return 20' calc.py || sleep 5"})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("took %s, past the budget", took)
	}
	if len(parts) != 2 || statuses[0].err != nil || statuses[1].err == nil {
		t.Fatalf("parts %v statuses %v, want one's return run and two's timed out, helper.py not reached", parts, statuses)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "calc.py")); string(data) != changed {
		t.Errorf("calc.py was not put back:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(root, "helper.py")); err != nil {
		t.Errorf("the new file was not put back: %v", err)
	}
}

// Two runs of the same code print the same once durations, (cached), clock
// times, hex addresses and long numbers are masked; a different result does
// not.
func TestCheckOutputMasksWhatDiffersBetweenRuns(t *testing.T) {
	digest := func(text string) string {
		var c checkOutput
		// Written in pieces, as a pipe delivers it.
		for len(text) > 0 {
			n := min(7, len(text))
			_, _ = c.Write([]byte(text[:n]))
			text = text[n:]
		}
		return string(c.sum())
	}
	same := [][2]string{
		{"ok  \tgithub.com/x/y\t0.412s\n", "ok  \tgithub.com/x/y\t(cached)\n"},
		{"PASS  make book  book (1m28s)\n", "PASS  make book  book (2m3s)\n"},
		{"full output in /tmp/nodwyn-validate-639603802\n", "full output in /tmp/nodwyn-validate-3560882220\n"},
		{"panic at 0xc000123abc\n12:01:33 done\n", "panic at 0xc000999def\n12:04:07 done\n"},
		{"a\n\nb\r\n", "a\nb\n"},
	}
	for _, c := range same {
		if digest(c[0]) != digest(c[1]) {
			t.Errorf("%q and %q differ", c[0], c[1])
		}
	}
	differ := [][2]string{
		{"ok  \tgithub.com/x/y\t0.412s\n", "FAIL\tgithub.com/x/y\t0.412s\n"},
		{"EXIT=0\n", "EXIT=1\n"},
		{"validate: PASS (0 warnings)\n", "validate: FAIL (1 failed, 0 warnings)\n"},
		{"1\n", "0\n"},
	}
	for _, c := range differ {
		if digest(c[0]) == digest(c[1]) {
			t.Errorf("%q and %q are the same", c[0], c[1])
		}
	}
}
