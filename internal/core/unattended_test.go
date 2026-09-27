package core

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/llm"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

// An unattended run (--yolo) answers ordinary confirms, but never writes a
// protected path: .nibb/init.scm runs as the user at the next reload, and a
// git hook at the user's next commit.
func TestUnattendedRunNeverWritesProtectedPaths(t *testing.T) {
	repo := t.TempDir()
	write := func(id, path string) fake.Turn {
		return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "fs-write", Args: map[string]any{"path": path, "content": "x\n"}}}}
	}
	srv := fake.New(write("n", ".nibb/init.scm"), write("g", ".git/hooks/pre-commit"), write("s", "src/ok.txt"), fake.Turn{Content: "done"})
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
	for _, p := range []string{".nibb/init.scm", ".git/hooks/pre-commit"} {
		if _, err := os.Stat(filepath.Join(repo, p)); err == nil {
			t.Errorf("%s was written unattended", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "src", "ok.txt")); string(b) != "x\n" {
		t.Error("an ordinary write was refused")
	}
	if !strings.Contains(string(srv.Requests[1].Raw), "protected path .nibb/init.scm") {
		t.Errorf("the model was not told why:\n%s", srv.Requests[1].Raw)
	}
}
