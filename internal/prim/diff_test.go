package prim

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	a := "one\ntwo\nthree\nfour\nfive\nsix\nseven\n"
	b := "one\ntwo\n3\nfour\nfive\nsix\nseven\neight\n"
	got := Unified("f", a, b, 1)
	want := "--- f\n+++ f\n@@ -2,3 +2,3 @@\n two\n-three\n+3\n four\n@@ -7,1 +7,2 @@\n seven\n+eight\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if d := Unified("f", "same\n", "same\n", 3); d != "--- f\n+++ f\n" {
		t.Fatalf("identical: %q", d)
	}
	if d := Unified("f", "", "new\nfile\n", 3); !strings.Contains(d, "+new\n+file\n") {
		t.Fatalf("new file: %q", d)
	}
	if d := Unified("f", "gone\n", "", 3); !strings.Contains(d, "-gone\n") {
		t.Fatalf("deleted: %q", d)
	}
}

// Every edit script must transform a into b.
func TestMyersEditScriptIsCorrect(t *testing.T) {
	cases := [][2]string{
		{"a b c d e f", "a x c d y f g"},
		{"", "a"}, {"a", ""}, {"a b", "b a"}, {"x x x", "x x"},
		{"1 2 3 4 5 6 7 8 9", "9 8 7 6 5 4 3 2 1"},
	}
	for _, c := range cases {
		a, b := strings.Fields(c[0]), strings.Fields(c[1])
		var rebuilt []string
		ai := 0
		ops, ok := myers(a, b)
		if !ok {
			t.Fatalf("%v: no script", c)
		}
		for _, o := range ops {
			switch o.kind {
			case ' ':
				if a[ai] != o.line {
					t.Fatalf("%v: equal op mismatch", c)
				}
				rebuilt = append(rebuilt, o.line)
				ai++
			case '-':
				ai++
			case '+':
				rebuilt = append(rebuilt, o.line)
			}
		}
		if strings.Join(rebuilt, " ") != c[1] || ai != len(a) {
			t.Fatalf("%v: script does not produce b: %v", c, rebuilt)
		}
	}
}

// Two files of 3,000 lines with nothing in common took 564 MB for one diff,
// since a full copy of the search state was kept for every edit. The state
// kept now is what each round can have reached, and a change past
// maxDiffEdits is summarised: this one is, in a few MB.
func TestUnifiedOfAFullRewriteIsBoundedInMemory(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&a, "old line %d\n", i)
		fmt.Fprintf(&b, "new line %d\n", i)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := Unified("f", a.String(), b.String(), 3)
	runtime.ReadMemStats(&after)
	if !strings.Contains(got, "diff too large to show") {
		t.Fatalf("a rewrite of 6,000 changed lines: %.200q", got)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 32<<20 {
		t.Fatalf("the diff allocated %d MB", alloc>>20)
	}
}

// A change of a few lines in a long file still shows as one hunk, whatever
// the file's size, and a change big enough to be worth reading still diffs.
func TestUnifiedFindsASmallEditInALongFileAndABigOneInAShortFile(t *testing.T) {
	var a, b []string
	for i := 0; i < 2900; i++ {
		a = append(a, fmt.Sprintf("line %d", i))
		b = append(b, fmt.Sprintf("line %d", i))
	}
	b[1500] = "changed"
	got := Unified("f", strings.Join(a, "\n")+"\n", strings.Join(b, "\n")+"\n", 1)
	if want := "@@ -1500,3 +1500,3 @@\n line 1499\n-line 1500\n+changed\n line 1501\n"; !strings.HasSuffix(got, want) || strings.Count(got, "@@") != 2 {
		t.Fatalf("got %q", got)
	}
	var c, d strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&c, "old %d\n", i)
		fmt.Fprintf(&d, "new %d\n", i)
	}
	if got := Unified("f", c.String(), d.String(), 3); strings.Contains(got, "too large") || strings.Count(got, "\n-old") != 1000 {
		t.Fatalf("a 2,000-edit rewrite: %.200q", got)
	}
}

// A new file or a deleted one is only added or only removed lines: it is shown
// whole whatever the edit limit, which bounds a search, not a listing.
func TestUnifiedOfANewOrADeletedFileIsShownWhole(t *testing.T) {
	text := strings.Repeat("a line\n", 3000)
	if got := Unified("f", "", text, 3); strings.Contains(got, "too large") || strings.Count(got, "\n+a line") != 3000 || !strings.Contains(got, "@@ -1,0 +1,3000 @@") {
		t.Fatalf("a new file of 3,000 lines: %.120q", got)
	}
	if got := Unified("f", text, "", 3); strings.Contains(got, "too large") || strings.Count(got, "\n-a line") != 3000 {
		t.Fatalf("a deleted file of 3,000 lines: %.120q", got)
	}
	if got := Unified("f", "keep\n"+text, "keep\n", 1); strings.Contains(got, "too large") || strings.Count(got, "\n-a line") != 3000 {
		t.Fatalf("3,000 lines removed after a kept one: %.120q", got)
	}
}
