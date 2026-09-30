package core

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A docs workspace (ADR 22): no programs, no user Scheme, mounted folders
// by absolute path, read-only ones refusing writes, and the previous content
// of a replaced file kept where git is not the undo.
func TestDocsWorkspaceProfile(t *testing.T) {
	repo, ref := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(repo, "status.md"), []byte("old status\n"), 0o644)
	os.WriteFile(filepath.Join(ref, "contract.txt"), []byte("terms\n"), 0o644)
	if _, err := config.SaveWorkspace(repo, config.Workspace{Kind: config.KindDocs, Mounts: []config.Mount{{Path: ref, Access: "read"}}}); err != nil {
		t.Fatal(err)
	}
	srv := fake.New(
		fake.Turn{ToolCalls: []fake.ToolCall{
			{ID: "r", Name: "fs-read", Args: map[string]any{"path": filepath.Join(ref, "contract.txt")}},
			{ID: "w", Name: "fs-write", Args: map[string]any{"path": "status.md", "content": "new status\n"}},
			{ID: "x", Name: "fs-write", Args: map[string]any{"path": filepath.Join(ref, "contract.txt"), "content": "changed\n"}},
		}},
		fake.Turn{Content: "done"},
	)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = srv.URL, "test-model"
	s, err := New(Options{Config: cfg, Repo: repo, Version: "test", Provider: llm.NewOllama(srv.URL), LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, tool := range s.Tools() {
		if tool.Name == "shell-exec" {
			t.Fatal("a docs workspace offers shell-exec")
		}
	}
	if err := s.StartEval(`(+ 1 2)`, "unix:test"); err == nil {
		t.Fatal("a docs workspace evaluated Scheme")
	}
	r := &rig{t: t, s: s, srv: srv, repo: repo}
	s.Send("update the status")
	evs := r.drain("y")
	if b, _ := os.ReadFile(filepath.Join(repo, "status.md")); string(b) != "new status\n" {
		t.Fatalf("status.md: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(ref, "contract.txt")); string(b) != "terms\n" {
		t.Fatal("wrote into a read-only mount")
	}
	var version, readOK, denied bool
	for _, e := range evs {
		switch e.Kind {
		case "file.version":
			version = strings.Contains(string(mustJSON(t, e.Payload)), "status.md")
		case "tool.result":
			raw := string(mustJSON(t, e.Payload))
			readOK = readOK || (strings.Contains(raw, `"id":"r"`) && strings.Contains(raw, "terms"))
			denied = denied || (strings.Contains(raw, `"id":"x"`) && strings.Contains(raw, "read-only"))
		}
	}
	if !version || !readOK || !denied {
		t.Fatalf("version %v, mounted read %v, read-only denial %v; events: %s", version, readOK, denied, kinds(evs))
	}
	raw := string(srv.Requests[0].Raw)
	if !strings.Contains(raw, "folder of documents") || !strings.Contains(raw, ref) {
		t.Fatal("system prompt does not describe the workspace")
	}
	_ = event.Status
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
