package prim

import (
	"strings"
	"testing"
)

// The view of a long stream is the lean profile's (ADR 32, amended
// 2026-10-17): a full session's shell-exec is the one origin/main has.

// A full session's shell-exec has the description it had, and shows a long
// stream whole up to the cap, saving nothing, though it has a scratch directory
// and the call is the model's; a lean session's has the view.
func TestShellExecIsUnchangedInAFullSession(t *testing.T) {
	full := newViewRig(t, func(o *Options) { o.Lean = false })
	p, _ := full.h.in.Lookup("shell-exec")
	if !strings.Contains(p.Meta.Doc, "output past the cap keeps its first and last halves") || strings.Contains(p.Meta.Doc, "$TMPDIR/out") || strings.Contains(p.Meta.Doc, "tail or head") {
		t.Errorf("a full session's description: %s", p.Meta.Doc)
	}
	if got := full.run(t, emit(330)); got.Stdout != joinLines(1, 330) {
		t.Errorf("a full session was shown %d bytes for %d: %.120q", len(got.Stdout), len(joinLines(1, 330)), got.Stdout)
	}
	if files := full.saved(t); len(files) != 0 {
		t.Errorf("a full session saved output: %v", files)
	}
	// Past the cap, the first and last halves, as before.
	big := full.run(t, `awk 'BEGIN { for (i = 1; i <= 40000; i++) print "line " i }'`)
	if !strings.Contains(big.Stdout, "omitted: only the first") || strings.Contains(big.Stdout, "not shown") || !strings.HasSuffix(big.Stdout, "line 40000\n") {
		t.Errorf("past the cap: %d bytes", len(big.Stdout))
	}
	lean := newViewRig(t)
	pl, _ := lean.h.in.Lookup("shell-exec")
	if !strings.Contains(pl.Meta.Doc, "$TMPDIR/out") || strings.Contains(pl.Meta.Doc, "output past the cap keeps its first and last halves") {
		t.Errorf("a lean session's description: %s", pl.Meta.Doc)
	}
}
