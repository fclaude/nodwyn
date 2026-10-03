package core

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// An unattended run (--yolo) answers ordinary confirms, but never writes a
// protected path: .nodwyn/init.scm runs as the user at the next reload, and a
// git hook at the user's next commit.
func TestUnattendedRunNeverWritesProtectedPaths(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write := func(id, path string) fake.Turn {
		return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "fs-write", Args: map[string]any{"path": path, "content": "x\n"}}}}
	}
	// Unattended, a turn that ends with its change untested is reminded twice
	// (the default evidence loop) before it ends.
	done := fake.Turn{Content: "done"}
	srv := fake.NewT(t, write("n", ".nodwyn/init.scm"), write("g", ".git/hooks/pre-commit"), write("s", "src/ok.txt"), done, done, done)
	t.Cleanup(srv.Close)
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: config.Default(), Repo: repo, Provider: llm.NewOllama(srv.URL), AutoApprove: true, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := &rig{t: t, s: s, srv: srv, repo: repo, logb: logb}
	if e := r.turn("go"); e != "" {
		t.Fatal(e)
	}
	for _, p := range []string{".nodwyn/init.scm", ".git/hooks/pre-commit"} {
		if _, err := os.Stat(filepath.Join(repo, p)); err == nil {
			t.Errorf("%s was written unattended", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "src", "ok.txt")); string(b) != "x\n" {
		t.Error("an ordinary write was refused")
	}
	if !strings.Contains(string(srv.Requests[1].Raw), "protected path .nodwyn/init.scm") {
		t.Errorf("the model was not told why:\n%s", srv.Requests[1].Raw)
	}
}
