package prim

import (
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// An old text that occurs more than once says where: the line each begins on
// and what it holds, so the next try copies the lines around the one it
// means. In the benchmark's transcripts the model retried with more context
// five times of five, and read the file in between four of five.
func TestAnAmbiguousEditSaysWhere(t *testing.T) {
	content := "def a():\n    if not c or not f:\n        return []\n\ndef b():\n    if not c or not f:\n        return []\n"
	_, err := applyEdit(content, "    if not c or not f:\n        return []", "x", false)
	if err == nil {
		t.Fatal("an ambiguous edit was applied")
	}
	for _, want := range []string{"occurs 2 times", "line 2: if not c or not f:", "line 6: if not c or not f:", "replace_all"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
}

// A longer old text that matches for its first lines and then differs says
// where it stops matching, with both sides: a dropped blank line (django's
// 15916) cost two more calls.
func TestAnEditThatStopsMatchingSaysWhere(t *testing.T) {
	content := "class A:\n    x = 1\n\n    def f(self):\n        return 1\n"
	_, err := applyEdit(content, "class A:\n    x = 1\n    def f(self):\n        return 1", "y", false)
	if err == nil {
		t.Fatal("an edit that does not match was applied")
	}
	for _, want := range []string{"the first 2 lines of old match lines 1-2", "then old line 3 is `def f(self):` where the file has `` (line 3)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
}

// An edit with neither old/new nor edits shows what a valid call is.
func TestAnEditWithoutOldAndNewShowsAnExample(t *testing.T) {
	_, _, err := editsOf(nil)
	if err == nil || !strings.Contains(err.Error(), "For example") || !strings.Contains(err.Error(), `"edits": [{"old"`) {
		t.Fatalf("err = %v", err)
	}
}

// fs-write says what it wrote, and warns when most of an existing file went:
// a whole-file rewrite dropped 117 of 426 lines and answered "ok".
func TestWriteSaysWhatItWroteAndWarnsOnALargeShrink(t *testing.T) {
	lines := func(n int) string { return strings.Repeat("x\n", n) }
	if got := writeAnswer("a.py", "", lines(3), false); got != "wrote a.py: a new file of 3 lines" {
		t.Errorf("new file: %q", got)
	}
	if got := writeAnswer("a.py", lines(100), lines(95), true); got != "wrote a.py: 95 lines (it had 100)" {
		t.Errorf("small change: %q", got)
	}
	got := writeAnswer("a.py", lines(100), lines(60), true)
	if !strings.Contains(got, "60 lines (it had 100)") || !strings.Contains(got, "lost 40 of its 100 lines") || !strings.Contains(got, "fs-edit") {
		t.Errorf("large shrink: %q", got)
	}
	if got := writeAnswer("a.py", lines(10), lines(2), true); strings.Contains(got, "lost") {
		t.Errorf("a short file warned: %q", got)
	}
}

// A tree with more candidate files than the limit is searched for source
// files instead of refused, and the answer says so; a glob still chooses.
func TestIndexGrepFallsBackToSourceFilesInALargeTree(t *testing.T) {
	root, k, _ := keepRepo(t)
	write(t, root, "pkg/code.py", "needle = 1\n", 0o644)
	write(t, root, "pkg/notes.txt", "needle in notes\n", 0o644)
	k.o.Lean = true
	was := searchFileLimit
	searchFileLimit = 3
	defer func() { searchFileLimit = was }()
	for i := 0; i < 4; i++ {
		write(t, root, "docs/page"+string(rune('a'+i))+".txt", "other\n", 0o644)
	}
	in := scheme.New()
	if err := Register(in, k.o); err != nil {
		t.Fatal(err)
	}
	v, err := in.EvalString(t.Context(), `(index-grep "." "needle")`)
	if err != nil {
		t.Fatal(err)
	}
	got := v.String()
	if !strings.Contains(got, "pkg/code.py") || strings.Contains(got, "notes.txt") || !strings.Contains(got, "only source and configuration files were searched") {
		t.Fatalf("got %s", got)
	}
	if _, err := in.EvalString(t.Context(), `(index-grep "." "needle" "*.txt")`); err == nil {
		t.Fatal("a glob that still matches too many files was not refused")
	}
}

// shell-exec adds a note where its bare numbers mislead.
func TestShellNotes(t *testing.T) {
	in, _ := schemeRig(t, map[string]string{"a.py": "x\ny\n"})
	for src, want := range map[string]string{
		`(shell-exec "bash" (list "-c" "sed -n '10,20p' a.py"))`:             "past the end of the file",
		`(shell-exec "bash" (list "-c" "yes | head -n 1; exit 141"))`:        "exit 141",
		`(shell-exec "bash" (list "-c" "cat a.py"))`:                         "",
		`(shell-exec "bash" (list "-c" "true"))`:                             "",
		`(shell-exec "bash" (list "-c" "sed -n '10,20p' a.py > /dev/null"))`: "",
	} {
		v, err := in.EvalString(t.Context(), src)
		if err != nil {
			t.Fatal(err)
		}
		got := v.String()
		if (want == "") == strings.Contains(got, `"note"`) || (want != "" && !strings.Contains(got, want)) {
			t.Errorf("%s = %s, want a note containing %q", src, got, want)
		}
	}
}
