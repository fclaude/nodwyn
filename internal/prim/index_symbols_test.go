package prim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/scheme"
)

func TestIndexSymbolsGuardAndFreshSource(t *testing.T) {
	h := newHarness(t)
	file := filepath.Join(h.root, "main.go")
	os.WriteFile(file, []byte("package p\nfunc Before() {}\n"), 0600)
	v := h.mustEval(t, `(index-symbols "main.go")`)
	if !v.Taint.Has(scheme.TagFile) || !strings.Contains(scheme.Write(v), "Before") {
		t.Fatal(scheme.Write(v))
	}
	if !strings.Contains(h.logBuf.String(), `"prim":"fs-read"`) {
		t.Fatal("missing nested guard record")
	}
	os.WriteFile(file, []byte("package p\nfunc After() {}\n"), 0600)
	if got := scheme.Write(h.mustEval(t, `(index-symbols "main.go")`)); !strings.Contains(got, "After") || strings.Contains(got, "Before") {
		t.Fatal(got)
	}
	os.Symlink("main.go", filepath.Join(h.root, "alias.go"))
	if _, err := h.eval(t, `(index-symbols "alias.go")`); err == nil {
		t.Fatal("symlink accepted")
	}
	_, err := h.eval(t, `(index-symbols "../outside.go")`)
	denied(t, err, "outside")
	if _, err := h.eval(t, `(index-symbols "README.md")`); err == nil || !strings.Contains(err.Error(), "index-grep") {
		t.Fatal(err)
	}
	h.in.SetGate(guard.New(nil, nil, Checkers(DefaultOptions(h.root), nil, nil)...).WithSchemeEval(h.in.Apply))
	if _, err := h.in.EvalString(context.Background(), `(register-check "no-source" "deny" (lambda (name args) (equal? name "fs-read")))`); err != nil {
		t.Fatal(err)
	}
	_, err = h.eval(t, `(index-symbols "main.go")`)
	denied(t, err, "no-source")
}
