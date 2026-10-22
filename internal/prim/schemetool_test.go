package prim

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/brote/scheme"
)

// schemeRig is a repository with files, and the tools a lean session has:
// the scheme, outline and tests-for tools and fs-read's symbol are offered
// with Options.Lean alone.
func schemeRig(t *testing.T, files map[string]string) (*scheme.Interp, string) {
	t.Helper()
	root, k, git := keepRepo(t)
	k.o.Lean = true
	for name, content := range files {
		write(t, root, name, content, 0o644)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "files")
	in := scheme.New()
	if err := Register(in, k.o); err != nil {
		t.Fatal(err)
	}
	return in, root
}

func evalScheme(t *testing.T, in *scheme.Interp, code string) (string, error) {
	t.Helper()
	ctx := scheme.WithOrigin(context.Background(), scheme.OriginModel)
	v, err := runSchemeTool(ctx, in, code)
	if err != nil {
		return "", err
	}
	if v.Kind == scheme.KString {
		return v.S, nil
	}
	return scheme.Write(v), nil
}

// A program calls the tools as procedures and keeps what matters of their
// results: here two reads and a filter in one call.
func TestSchemeToolCallsTheToolsAsProcedures(t *testing.T) {
	in, _ := schemeRig(t, map[string]string{"a.py": "x = 1\n# TODO fix\ny = 2\n", "b.py": "z = 3\n# TODO again\n"})
	got, err := evalScheme(t, in, `(map (lambda (p)
  (filter (lambda (l) (string-contains? l "TODO")) (string-split (fs-read p) "\n")))
  (list "a.py" "b.py"))`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `(("# TODO fix") ("# TODO again"))`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	got, err = evalScheme(t, in, `(define (n p) (length (string-split (fs-read p) "\n"))) (+ (n "a.py") (n "b.py"))`)
	if err != nil || got != "7" {
		t.Fatalf("got %q %v, want 7 (the two files' lines, with the trailing empty ones)", got, err)
	}
}

// A tool that fails raises an error the program can handle, and one it does
// not handle is the call's error.
func TestSchemeToolErrors(t *testing.T) {
	in, _ := schemeRig(t, map[string]string{"a.py": "x\n"})
	got, err := evalScheme(t, in, `(guard (e (else "failed")) (fs-read "missing.py"))`)
	if err != nil || got != "failed" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := evalScheme(t, in, `(fs-read "missing.py")`); err == nil {
		t.Fatal("an unhandled tool error was swallowed")
	}
	if _, err := evalScheme(t, in, `(+ 1`); err == nil {
		t.Fatal("an unreadable program ran")
	}
}

// What a program may name: the tools it is offered and the language's own
// procedures; no hidden primitive, no strategy procedure, no register-tool,
// and not itself.
func TestSchemeToolOnlyNamesWhatTheModelHas(t *testing.T) {
	in, _ := schemeRig(t, map[string]string{"a.py": "x\n"})
	for _, code := range []string{
		`(register-tool "x" "doc" '() (lambda (c a) 1))`,
		`(scheme "1")`,
		`(check-without "true" '())`,
		`(list (keep-passing))`,
		`(define (f) (original-tests "true" '() '())) 1`,
		`(let ((g primitives)) (quote (keep-passing)))`,
	} {
		_, err := evalScheme(t, in, code)
		if err == nil || !strings.Contains(err.Error(), "is not available here") {
			t.Errorf("%s: err = %v, want 'is not available here'", code, err)
		}
	}
	// The language's own pure procedures and an offered tool are.
	if got, err := evalScheme(t, in, `(string-append "a" (number->string (length (list 1 2))))`); err != nil || got != "a2" {
		t.Errorf("got %q %v", got, err)
	}
}

// A program that never ends is stopped, and its tools with it.
func TestSchemeToolHasATimeBudget(t *testing.T) {
	in, _ := schemeRig(t, map[string]string{"a.py": "x\n"})
	was := schemeToolBudget
	schemeToolBudget = 300 * time.Millisecond
	defer func() { schemeToolBudget = was }()
	_, err := evalScheme(t, in, `(let loop () (loop))`)
	if err == nil || !strings.Contains(err.Error(), "out of time") {
		t.Fatalf("err = %v, want out of time", err)
	}
}

// shell-exec through the program is the same tool: its result is data.
func TestSchemeToolRunsAProgramThroughShellExec(t *testing.T) {
	in, _ := schemeRig(t, map[string]string{"a.py": "x\n"})
	got, err := evalScheme(t, in, `(shell-exec "echo" (list "hello"))`)
	if err != nil || !strings.Contains(got, "hello") {
		t.Fatalf("got %q %v", got, err)
	}
}

// A long result is cut, with the cut said.
func TestSchemeToolCutsALongResult(t *testing.T) {
	in, _ := schemeRig(t, map[string]string{"a.py": "x\n"})
	got, err := evalScheme(t, in, `(apply string-append (map (lambda (i) "0123456789") (iota 5000)))`)
	if err != nil || !strings.Contains(got, "[result truncated at 24 KiB]") || len(got) > schemeToolOutputMax+100 {
		t.Fatalf("len %d, err %v", len(got), err)
	}
}
