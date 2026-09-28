package prim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/scheme"
)

func requireRipgrep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Fatal("retrieval tests require ripgrep (rg) on PATH")
	}
}
func grepResult(t *testing.T, v scheme.Value) searchResult {
	t.Helper()
	raw, err := scheme.ToJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	var result searchResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestIndexGrepGuardedSearch(t *testing.T) {
	requireRipgrep(t)
	h := newHarness(t)
	for name, text := range map[string]string{"alpha.txt": "hello one\nother\nhello two\n", "key.pem": "hello protected", "credentials": "hello credential", "binary": "hello\x00hidden", ".hidden": "hello hidden", ".ignore": "*\n"} {
		if err := os.WriteFile(filepath.Join(h.root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	os.Symlink("alpha.txt", filepath.Join(h.root, "linked.txt"))
	os.Symlink("credentials", filepath.Join(h.root, "alias.txt"))
	v := h.mustEval(t, `(index-grep "." "hello")`)
	if !v.Taint.Has(scheme.TagFile) {
		t.Fatal("missing file provenance")
	}
	got := grepResult(t, v)
	if len(got.Matches) != 3 || got.Matches[0].Path != "README.md" || got.Matches[1].Line != 1 || got.Matches[2].Line != 3 || got.Skipped != 3 || got.Truncated {
		t.Fatalf("%+v", got)
	}
	if len(h.prompts) != 0 {
		t.Fatal("read-only search prompted")
	}
	if !strings.Contains(h.logBuf.String(), `"prim":"fs-read"`) {
		t.Fatal("nested read missing guard audit")
	}
	for _, src := range []string{`(index-grep ".env" "secret")`, `(index-grep "escape-link" "outside")`, `(index-grep ".." "outside")`} {
		_, err := h.eval(t, src)
		var denied *scheme.Denied
		if !errors.As(err, &denied) {
			t.Fatalf("%s: %v", src, err)
		}
	}
	for _, path := range []string{"linked.txt", "alias.txt"} {
		got = grepResult(t, h.mustEval(t, fmt.Sprintf("(index-grep %q %q)", path, "hello")))
		if len(got.Matches) != 0 || got.Skipped != 1 {
			t.Fatalf("symlink %s: %+v", path, got)
		}
	}
	// Each query reads the current file, with no disk or cross-session cache.
	os.WriteFile(filepath.Join(h.root, "alpha.txt"), []byte("fresh needle\n"), 0600)
	got = grepResult(t, h.mustEval(t, `(index-grep "alpha.txt" "fresh")`))
	if len(got.Matches) != 1 || got.Matches[0].Text != "fresh needle" {
		t.Fatalf("%+v", got)
	}
}

func TestIndexGrepRejectsBypassAndInvalidQueries(t *testing.T) {
	requireRipgrep(t)
	h := newHarness(t)
	// Neither user configuration nor a regex that looks like an option can turn
	// a fixed search into a preprocessor invocation or filesystem write.
	config := filepath.Join(t.TempDir(), "rg-config")
	os.WriteFile(config, []byte("--pre=touch\n--hidden\n"), 0600)
	t.Setenv("RIPGREP_CONFIG_PATH", config)
	if got := grepResult(t, h.mustEval(t, `(index-grep "." "--pre=touch")`)); len(got.Matches) != 0 {
		t.Fatal(got)
	}
	os.Mkdir(filepath.Join(h.root, "empty"), 0700)
	if _, err := h.eval(t, `(index-grep "empty" "[")`); err == nil {
		t.Fatal("invalid regex accepted in empty directory")
	}
	h.in.SetGate(guard.New(nil, nil, Checkers(DefaultOptions(h.root), nil, nil)...).WithSchemeEval(h.in.Apply))
	_, err := h.in.EvalString(context.Background(), `(register-check "no-read" "deny" (lambda (name args) (equal? name "fs-read")))`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.eval(t, `(index-grep "README.md" "hello")`)
	denied(t, err, "no-read")
	t.Setenv("PATH", t.TempDir())
	if _, err = h.eval(t, `(index-grep "README.md" "hello")`); err == nil || !strings.Contains(err.Error(), "install ripgrep") {
		t.Fatal(err)
	}
}

func TestIndexGrepBoundsAndCancellation(t *testing.T) {
	requireRipgrep(t)
	h := newHarness(t)
	o := DefaultOptions(h.root)
	o.MaxOutput = 1 << 20
	in := scheme.New()
	if err := Register(in, o); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(h.root, "many.txt"), []byte(strings.Repeat("hit\n", 200)), 0600)
	v, err := in.EvalString(context.Background(), `(index-grep "many.txt" "hit")`)
	if err != nil {
		t.Fatal(err)
	}
	got := grepResult(t, v)
	if len(got.Matches) != 100 || !got.Truncated {
		t.Fatalf("%+v", got)
	}
	for i := 0; i < searchFileLimit; i++ {
		os.WriteFile(filepath.Join(h.root, fmt.Sprintf("f%d", i)), nil, 0600)
	}
	if _, err = h.eval(t, `(index-grep "." "hello")`); err == nil || !strings.Contains(err.Error(), "narrow the path") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = in.EvalString(ctx, `(index-grep "." "hello")`); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// A subprocess already running also obeys cancellation and retains no partial
	// result. This helper stands in for rg; it is not an exposed command option.
	sleeper := filepath.Join(t.TempDir(), "wait")
	os.WriteFile(sleeper, []byte("#!/bin/sh\nexec /bin/sleep 10\n"), 0700)
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = runRipgrep(ctx, sleeper, h.root, "", 1024)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatal(err, time.Since(start))
	}
	// Structured output never silently accepts a prefix, including exact-boundary
	// writes followed by another chunk.
	b := searchOutput{limit: 3}
	b.Write([]byte("abc"))
	b.Write([]byte("d"))
	if !b.overflow || b.buffer.String() != "abc" {
		t.Fatal(b)
	}
	rg, _ := exec.LookPath("rg")
	if _, err := runRipgrep(context.Background(), rg, h.root, "hello\n", 3, "--json", "hello", "-"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatal(err)
	}
}

// Contents are matched in Go on the guarded reads: a glob narrows the
// candidates, context adds the lines around a match, and a large tree
// that the old 128-file limit refused is searched.
func TestIndexGrepGlobContextAndLargerTrees(t *testing.T) {
	requireRipgrep(t)
	h := newHarness(t)
	os.MkdirAll(filepath.Join(h.root, "pkg"), 0o755)
	os.WriteFile(filepath.Join(h.root, "pkg", "a.py"), []byte("one\ntwo\nneedle here\nfour\nfive\n"), 0o600)
	os.WriteFile(filepath.Join(h.root, "pkg", "b.txt"), []byte("needle in text\n"), 0o600)
	os.WriteFile(filepath.Join(h.root, "pkg", "empty.py"), nil, 0o600)
	got := grepResult(t, h.mustEval(t, `(index-grep "pkg" "needle" "*.py" 2)`))
	if len(got.Matches) != 1 || got.Matches[0].Path != "pkg/a.py" || got.Matches[0].Line != 3 ||
		strings.Join(got.Matches[0].Before, "|") != "one|two" || strings.Join(got.Matches[0].After, "|") != "four|five" {
		t.Fatalf("glob and context: %+v", got)
	}
	if got := grepResult(t, h.mustEval(t, `(index-grep "pkg" "needle" "*.py" 9)`)); len(got.Matches[0].Before) != 2 || len(got.Matches[0].After) != 2 {
		t.Fatalf("context over 5 is capped, not refused: %+v", got)
	}
	if got := grepResult(t, h.mustEval(t, `(index-grep "pkg" "^$")`)); len(got.Matches) != 0 {
		t.Fatalf("an empty file matched: %+v", got)
	}
	if got := grepResult(t, h.mustEval(t, `(index-grep "pkg" "needle" "")`)); len(got.Matches) != 2 {
		t.Fatalf("empty glob is every file: %+v", got)
	}
	for src, want := range map[string]string{
		`(index-grep "pkg" "needle" "*.py" -1)`: "context is 0 to 5 lines",
		`(index-grep "pkg" "(")`:                "invalid regular expression",
	} {
		if _, err := h.eval(t, src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", src, err)
		}
	}
	os.MkdirAll(filepath.Join(h.root, "tree"), 0o755)
	for i := 0; i < 400; i++ {
		os.WriteFile(filepath.Join(h.root, "tree", fmt.Sprintf("m%03d.py", i)), []byte(fmt.Sprintf("x = %d\n", i)), 0o600)
	}
	got = grepResult(t, h.mustEval(t, `(index-grep "tree" "= 399$" "*.py")`))
	if len(got.Matches) != 1 || got.Files != 400 {
		t.Fatalf("400-file tree: %+v", got)
	}
}

// fs-find lists names by glob, sorted, without protected or credential
// names, and never escapes the repository.
func TestFsFind(t *testing.T) {
	requireRipgrep(t)
	h := newHarness(t)
	os.MkdirAll(filepath.Join(h.root, "src", "deep"), 0o755)
	for _, name := range []string{"src/b.py", "src/a.py", "src/deep/c.py", "src/notes.txt", "src/key.pem", "src/.hidden.py", "src/.git/x.py"} {
		os.MkdirAll(filepath.Dir(filepath.Join(h.root, name)), 0o755)
		os.WriteFile(filepath.Join(h.root, name), []byte("x"), 0o600)
	}
	raw, err := scheme.ToJSON(h.mustEval(t, `(fs-find "src" "*.py")`))
	if err != nil {
		t.Fatal(err)
	}
	var got findResult
	json.Unmarshal(raw, &got)
	if strings.Join(got.Files, ",") != "src/a.py,src/b.py,src/deep/c.py" || got.Count != 3 || got.Truncated {
		t.Fatalf("%+v", got)
	}
	raw, _ = scheme.ToJSON(h.mustEval(t, `(fs-find "src" "*")`))
	json.Unmarshal(raw, &got)
	if all := strings.Join(got.Files, ","); strings.Contains(all, "key.pem") || strings.Contains(all, ".git") || strings.Contains(all, ".hidden") {
		t.Fatalf("a credential-looking name was listed: %+v", got)
	}
	if _, err := h.eval(t, `(fs-find ".." "*")`); err == nil {
		t.Fatal("fs-find escaped the repository")
	}
	if _, err := h.eval(t, `(fs-find "README.md" "*")`); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file path: %v", err)
	}
	if len(h.prompts) != 0 {
		t.Fatal("fs-find prompted")
	}
}
