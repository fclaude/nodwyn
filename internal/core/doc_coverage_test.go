package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every primitive a session registers is documented in docs/scheme.md, not
// only the interpreter's builtins (TestDocCoverage in internal/scheme):
// host primitives and the loop's too (ADR 7).
func TestSessionPrimitivesAreDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "scheme.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	r := newRig(t, true)
	for _, p := range r.s.interp.Prims() {
		if !strings.Contains(text, "`("+p.Name+" ") && !strings.Contains(text, "`("+p.Name+")") && !strings.Contains(text, "`"+p.Name+"`") {
			t.Errorf("primitive %q is not documented in docs/scheme.md", p.Name)
		}
	}
}
