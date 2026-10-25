package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/test/fakes/must"
)

func TestManagedEnvelopeRefusesGuessedAndNestedHostCalls(t *testing.T) {
	m := &ManagedProfile{}
	root := t.TempDir()
	for _, name := range []string{"shell-exec", "shell-start", "term-open", "scheme", "policy-write", "git-commit", "request", "credential-store", "project-instructions", "index-search", "unknown-future-tool"} {
		p := &scheme.Prim{Name: name, Meta: scheme.Meta{Host: true}}
		if e := m.Before(root, p, []scheme.Value{scheme.Str("index.html")}); e == nil {
			t.Fatal("allowed", name)
		}
	}
	p := &scheme.Prim{Name: "fs-write", Meta: scheme.Meta{Host: true}}
	for _, path := range []string{"../other/index.html", "/tmp/out.html", ".nodwyn/init.scm", "AGENTS.md", "run.sh", "a/.git/x.json"} {
		if e := m.Before(root, p, []scheme.Value{scheme.Str(path)}); e == nil {
			t.Fatal("allowed", path)
		}
	}
	if e := os.Symlink(t.TempDir(), filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	if e := m.Before(root, p, []scheme.Value{scheme.Str("link/a.html")}); e == nil {
		t.Fatal("allowed link")
	}
	if e := m.Before(root, p, []scheme.Value{scheme.Str("index.html")}); e != nil {
		t.Fatal(e)
	}
}
func TestManagedGateIsBeforeAnyApprovalAndCannotBeLiftedByScheme(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "marker.html")
	must.WriteFile(t, marker, []byte("unchanged"), 0600)
	called := false
	in := scheme.New()
	p := &scheme.Prim{Name: "shell-exec", Meta: scheme.Meta{Host: true, Doc: "A forbidden fake host primitive."}, Handler: func(*scheme.Call) (scheme.Value, error) { called = true; return scheme.Void, nil }}
	if e := in.Register(p); e != nil {
		t.Fatal(e)
	}
	s := &Session{opts: Options{Repo: root, Managed: &ManagedProfile{}}}
	in.SetGate(turnGate{s: s})
	if _, e := in.EvalString(context.Background(), `(define (wrapper) (shell-exec)) (wrapper)`); e == nil {
		t.Fatal("nested call was allowed")
	}
	if called {
		t.Fatal("forbidden primitive executed")
	}
}
