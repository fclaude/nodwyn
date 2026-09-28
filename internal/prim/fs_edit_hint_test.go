package prim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// replace_all replaces every occurrence, and its preview shows them all; an
// old text that matches only once whitespace is ignored gets an error that
// quotes the file's exact lines, so the next try can copy them.
func TestFsEditReplaceAllAndWhitespaceHints(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	file := filepath.Join(h.root, "code.py")
	os.WriteFile(file, []byte("def f():\n\treturn  x + 1   \n\ny = x\nz = x\n"), 0o644)

	h.mustEval(t, `(fs-edit "code.py" "= x" "= w" #t)`)
	if b, _ := os.ReadFile(file); string(b) != "def f():\n\treturn  x + 1   \n\ny = w\nz = w\n" {
		t.Fatalf("replace_all: %q", b)
	}
	if detail := h.prompts[len(h.prompts)-1].Detail; strings.Count(detail, "+y = w")+strings.Count(detail, "+z = w") != 2 {
		t.Fatalf("the preview does not show every replacement:\n%s", detail)
	}
	if _, err := h.eval(t, `(fs-edit "code.py" "w" "v")`); err == nil || !strings.Contains(err.Error(), "occurs 2 times") || !strings.Contains(err.Error(), "replace_all") {
		t.Fatalf("ambiguous edit: %v", err)
	}
	_, err := h.eval(t, `(fs-edit "code.py" "    return x + 1\n" "    return x + 2\n")`)
	if err == nil || !strings.Contains(err.Error(), "ignoring whitespace it matches lines 2-2") || !strings.Contains(err.Error(), "Those lines are exactly:\n\treturn  x + 1   \n") {
		t.Fatalf("whitespace hint: %v", err)
	}
	os.WriteFile(file, []byte("a = 1\nb = 2\na = 1\n"), 0o644)
	if _, err := h.eval(t, `(fs-edit "code.py" "a =  1" "a = 3")`); err == nil || !strings.Contains(err.Error(), "matches 2 places (first at line 1)") {
		t.Fatalf("two whitespace matches: %v", err)
	}
	if _, err := h.eval(t, `(fs-edit "code.py" "nothing like it" "x")`); err == nil || !strings.Contains(err.Error(), "read the region again with fs-read") {
		t.Fatalf("no match at all: %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != "a = 1\nb = 2\na = 1\n" {
		t.Fatalf("a failed edit changed the file: %q", b)
	}
}
