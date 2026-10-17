package prim

import (
	"fmt"
	"strings"
	"testing"
)

// fs-edit answers with the lines it changed as the file now reads them,
// numbered, two lines around each: it answered "ok", a stale read of the
// file folded two rounds later, and the model read the file again for its
// next edit (transcript.js 49 times in session 20261005T004207Z-f0ddf504).
func TestEditedRegionsShowTheChangedLines(t *testing.T) {
	var before strings.Builder
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&before, "line %d\n", i)
	}
	after := strings.Replace(before.String(), "line 10\n", "line ten\nline ten and a half\n", 1)
	after = strings.Replace(after, "line 50\n", "", 1)
	got := editedRegions("a.txt", before.String(), after)
	want := `edited a.txt, now 60 lines; the changed lines now read:
8	line 8
9	line 9
10	line ten
11	line ten and a half
12	line 11
13	line 12
…
49	line 48
50	line 49
51	line 51
52	line 52`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := editedRegions("a.txt", "x\n", "x\n"); !strings.Contains(got, "no change") {
		t.Fatalf("an edit that changed nothing: %s", got)
	}
	// A long run shows its ends, not every line just written.
	long := strings.Repeat("new\n", 100)
	got = editedRegions("b.txt", "a\nb\n", "a\n"+long+"b\n")
	if !strings.Contains(got, "now 102 lines (+100)") || !strings.Contains(got, "as written") || strings.Count(got, "\n") > 50 {
		t.Fatalf("a long run:\n%s", got)
	}
}
