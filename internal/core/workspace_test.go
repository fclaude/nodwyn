package core

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/scheme"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// mounted registers repo in a workspace list of its own with mounts, as
// the console's workspace.save does, and returns the paths to open it with.
func mounted(t *testing.T, repo string, mounts ...config.Mount) config.Paths {
	t.Helper()
	paths := config.PathsUnder(t.TempDir(), t.TempDir())
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := config.SaveRegistered(config.RegistryFile(paths), config.Registered{Name: "work", Path: repo, Mounts: mounts}); err != nil {
		t.Fatal(err)
	}
	return paths
}

// A workspace with mounted folders (ADR 22) has every tool, the defaults'
// own included, and evaluates Scheme; mounted folders are reached by
// absolute path, read-only ones refuse writes, and the previous content of
// a replaced file is kept where git is not the undo.
func TestWorkspaceUsesItsMountedFolders(t *testing.T) {
	t.Parallel()
	repo, ref := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(repo, "status.md"), []byte("old status\n"), 0o644)
	os.WriteFile(filepath.Join(ref, "contract.txt"), []byte("terms\n"), 0o644)
	paths := mounted(t, repo, config.Mount{Path: ref, Access: "read"})
	srv := fake.NewT(t,
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
	s, err := New(Options{Config: cfg, Repo: repo, Paths: paths, Version: "test", Provider: llm.NewOllama(srv.URL), LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	offered := map[string]bool{}
	for _, tool := range s.Tools() {
		offered[tool.Name] = true
	}
	for _, name := range []string{"shell-exec", "read-definition", "second-look"} {
		if !offered[name] {
			t.Fatalf("a workspace does not offer %s", name)
		}
	}
	if v, err := s.Eval(context.Background(), `(+ 1 2)`); err != nil || scheme.Write(v) != "3" {
		t.Fatalf("a workspace did not evaluate Scheme: %v %v", v, err)
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
	if !strings.Contains(raw, "Folders outside it you may also use") || !strings.Contains(raw, ref) {
		t.Fatal("system prompt does not name the mounted folder")
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

// Unattended sessions are told about mounted folders too.
func TestUnattendedPromptNamesMounts(t *testing.T) {
	t.Parallel()
	repo, ref := t.TempDir(), t.TempDir()
	paths := mounted(t, repo, config.Mount{Path: ref, Access: "read"})
	// A --yolo turn that changes nothing is reminded once.
	srv := fake.NewT(t, fake.Turn{Content: "done"}, fake.Turn{Content: "done"})
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = srv.URL, "test-model"
	s, err := New(Options{Config: cfg, Repo: repo, Paths: paths, Version: "test", Provider: llm.NewOllama(srv.URL), AutoApprove: true, LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := &rig{t: t, s: s, srv: srv, repo: repo}
	if e := r.turn("look"); e != "" {
		t.Fatal(e)
	}
	raw := string(srv.Requests[0].Raw)
	if !strings.Contains(raw, "You work unattended") || !strings.Contains(raw, ref) {
		t.Fatal("the unattended prompt does not name the mounted folder")
	}
}

// A folder's own .nodwyn/workspace.toml, which whoever commits to it writes,
// grants nothing: a session there reads only its folder and what the
// person's workspace list mounts, and every confirm answered no changes
// none of that (ADR 22, amended 2026-10-03).
func TestAFoldersOwnWorkspaceFileGrantsNothing(t *testing.T) {
	t.Parallel()
	repo, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "private.txt")
	if err := os.WriteFile(secret, []byte("someone else's notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nodwyn", "workspace.toml"), []byte("kind = \"docs\"\n\n[[mount]]\npath = "+strconv.Quote(outside)+"\naccess = \"write\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := fake.NewT(t, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "r", Name: "fs-read", Args: map[string]any{"path": secret}}}}, fake.Turn{Content: "done"})
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = srv.URL, "test-model"
	paths := config.PathsUnder(t.TempDir(), t.TempDir())
	s, err := New(Options{Config: cfg, Repo: repo, Paths: paths, Version: "test", Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if ws := s.Workspace(); len(ws.Mounts) != 0 {
		t.Fatalf("the folder's own file mounted %+v", ws.Mounts)
	}
	r := &rig{t: t, s: s, srv: srv, repo: repo}
	if e := r.turn("read it"); e != "" {
		t.Fatal(e)
	}
	raw := string(mustJSON(t, s.EventsSoFar()))
	if strings.Contains(raw, "someone else's notes") || !strings.Contains(raw, "outside the repository") {
		t.Fatal("a file outside the folder was read on the folder's own say-so")
	}
	if v, err := s.Eval(context.Background(), `(workspace)`); err != nil || scheme.Write(v) != `(("mounts"))` {
		t.Fatalf("(workspace) is %s %v", scheme.Write(v), err)
	}
}
