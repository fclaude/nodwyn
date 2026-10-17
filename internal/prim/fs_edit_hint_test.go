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

// An old that holds credential-shaped text of its own is a guess at what the
// file holds, since the model reads that as a placeholder (ADR 25): the error
// says to write the placeholder, and never carries the file's own text. An old
// that fails for another reason, or a placeholder made up for a file with no
// credential in it, gets no such hint.
func TestFsEditCredentialHints(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	file := filepath.Join(h.root, "fixture.go")
	const key = "sk-ant-abcdefgh1234"
	os.WriteFile(file, []byte("name := \"credential\"\nbody := \""+key+"\"\n"), 0o644)

	_, err := h.eval(t, `(fs-edit "fixture.go" "body := \"sk-ant-zzzzzzzz9999\"" "body := \"x\"")`)
	if err == nil || !strings.Contains(err.Error(), "old contains credential-shaped text") || !strings.Contains(err.Error(), "Write the placeholder") {
		t.Fatalf("guessed credential in old: %v", err)
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("the error quotes the credential: %v", err)
	}
	// An edit in a list says which one failed and still gives the hint.
	_, err = h.eval(t, `(fs-edit "fixture.go" "" "" #f (list (list (cons "old" "body := \"sk-ant-zzzzzzzz9999\"") (cons "new" "x"))))`)
	if err == nil || !strings.Contains(err.Error(), "edit 1 of 1 failed") || !strings.Contains(err.Error(), "old contains credential-shaped text") {
		t.Fatalf("guessed credential in a listed edit: %v", err)
	}
	if _, err := h.eval(t, `(fs-edit "fixture.go" "nothing like it" "x")`); err == nil || strings.Contains(err.Error(), "credential-shaped") {
		t.Fatalf("a plain miss was blamed on a credential: %v", err)
	}
	// A placeholder the model made up, in a file with no credential: a miss like any other.
	os.WriteFile(file, []byte("name := \"credential\"\n"), 0o644)
	if _, err := h.eval(t, `(fs-edit "fixture.go" "body := \"[REDACTED anthropic-key]\"" "x")`); err == nil || strings.Contains(err.Error(), "credential-shaped") || !strings.Contains(err.Error(), "read the region again") {
		t.Fatalf("a placeholder with no credential behind it: %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != "name := \"credential\"\n" {
		t.Fatalf("a failed edit changed the file: %q", b)
	}
}
