package prim

import (
	"fmt"
	"strings"
)

// What fs-edit answers: the lines it changed, as the file now reads them,
// numbered. It answered "ok" until 2026-10-05, and a stale read of the
// file folds two rounds after an edit (hygiene.scm), so the model read the
// file again to make its next edit: transcript.js 49 times in session
// 20261005T004207Z-f0ddf504, and "old text not found" clustered after
// those folds.
const (
	editContext    = 2    // unchanged lines shown around each change
	editRunLines   = 40   // a changed run longer than this shows its ends
	editShownLines = 120  // the most lines one answer shows
	editMiddle     = 4000 // changed lines past which the whole stretch counts as one run
)

// editedRegions says where the edit of path left before as after: each run
// of changed lines with editContext lines around it, numbered as the file
// now is. Runs closer than twice the context are shown as one.
func editedRegions(path, before, after string) string {
	a, b := splitLines(before), splitLines(after)
	// What both keep at the start and the end is no change; only what is
	// between is diffed, so a large file with a small edit costs little.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var runs [][2]int // changed lines of b, 0-based, end exclusive; empty at a deletion
	added, removed := -1, -1
	midA, midB := a[pre:len(a)-suf], b[pre:len(b)-suf]
	switch {
	case len(midA) == 0 && len(midB) == 0:
		return fmt.Sprintf("%s: no change (the new text is the old)", path)
	case len(midA)+len(midB) > editMiddle:
		runs = append(runs, [2]int{pre, len(b) - suf})
	default:
		ops, ok := myers(midA, midB)
		if !ok {
			runs = append(runs, [2]int{pre, len(b) - suf})
			break
		}
		added, removed = 0, 0
		for _, o := range ops {
			switch o.kind {
			case '+':
				added++
			case '-':
				removed++
			}
		}
		// Walk the edit script with the line of b it has reached: a
		// deletion is a change at that line, an insertion the line itself.
		at, open := pre, false
		for _, o := range ops {
			switch o.kind {
			case ' ':
				open = false
				at++
			case '-', '+':
				if !open {
					runs = append(runs, [2]int{at, at})
					open = true
				}
				if o.kind == '+' {
					at++
					runs[len(runs)-1][1] = at
				}
			}
		}
	}
	// Each run with its context, merged with the next when they touch.
	var shown [][2]int
	for _, r := range runs {
		lo, hi := max(0, r[0]-editContext), min(len(b), r[1]+editContext)
		if n := len(shown); n > 0 && lo <= shown[n-1][1] {
			shown[n-1][1] = max(shown[n-1][1], hi)
			continue
		}
		shown = append(shown, [2]int{lo, hi})
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "edited %s, now %d lines", path, len(b))
	switch {
	case added >= 0:
		fmt.Fprintf(&sb, " (+%d -%d)", added, removed)
	case len(b) != len(a):
		fmt.Fprintf(&sb, " (%+d)", len(b)-len(a))
	}
	sb.WriteString("; the changed lines now read:\n")
	budget := editShownLines
	for i, s := range shown {
		if budget <= 0 {
			fmt.Fprintf(&sb, "… %d more changed places, from line %d: read them with fs-read\n", len(shown)-i, s[0]+1)
			break
		}
		lines := b[s[0]:s[1]]
		if len(lines) == 0 { // the end of the file was removed
			fmt.Fprintf(&sb, "(the file now ends at line %d)\n", len(b))
			continue
		}
		for j := 0; j < len(lines); j++ {
			if len(lines) > editRunLines && j == editRunLines/2 {
				skip := len(lines) - editRunLines/2 - editRunLines/4
				fmt.Fprintf(&sb, "… lines %d-%d as written …\n", s[0]+j+1, s[0]+j+skip)
				j += skip - 1
				continue
			}
			fmt.Fprintf(&sb, "%d\t%s\n", s[0]+j+1, lines[j])
			budget--
		}
		if i < len(shown)-1 {
			sb.WriteString("…\n")
		}
	}
	return strings.TrimSuffix(sb.String(), "\n")
}
