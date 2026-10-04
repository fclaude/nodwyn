package rpc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/prim"
)

// A workspace's name and mounted folders go to the workspace list in the
// config directory, never into the folder; a task opened there has its
// mounts and every tool; a kind is no longer a thing to send (ADR 22).
func TestWorkspaceSaveListAndSessions(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	code, docs, ref := t.TempDir(), t.TempDir(), t.TempDir()
	var info workspaceInfo
	json.Unmarshal(c.ok("workspace.save", map[string]any{"path": code, "name": "nodwyn", "mounts": []any{}}), &info)
	json.Unmarshal(c.ok("workspace.save", map[string]any{"path": docs, "name": "Receipts 2026", "mounts": []any{map[string]any{"path": ref, "access": "read"}}}), &info)
	if info.Name != "Receipts 2026" || len(info.Mounts) != 1 || info.Mounts[0].Path != ref {
		t.Fatalf("saved: %+v", info)
	}
	for _, folder := range []string{code, docs} {
		if entries, _ := os.ReadDir(folder); len(entries) != 0 {
			t.Fatalf("saving a workspace wrote into %s: %v", folder, entries)
		}
	}
	registry, err := os.ReadFile(config.RegistryFile(o.Paths))
	if err != nil || !strings.Contains(string(registry), "[[workspace.mount]]") || !strings.Contains(string(registry), ref) {
		t.Fatalf("workspaces.toml: %s %v", registry, err)
	}
	if r := c.call("workspace.save", map[string]any{"path": filepath.Join(docs, "missing"), "name": "x", "mounts": []any{}}); r.Error == nil {
		t.Fatal("saved a folder that does not exist")
	}
	if r := c.call("workspace.save", map[string]any{"path": docs, "name": "x", "mounts": []any{map[string]any{"path": "/", "access": "read"}}}); r.Error == nil {
		t.Fatal("saved a mount of /")
	}
	if r := c.call("workspace.save", map[string]any{"path": docs, "name": "x", "kind": "docs", "mounts": []any{}}); r.Error == nil || !strings.Contains(r.Error.Message, "kind") {
		t.Fatalf("a kind was accepted: %+v", r.Error)
	}
	var list []workspaceInfo
	json.Unmarshal(c.ok("workspace.list", map[string]any{}), &list)
	if len(list) != 2 || list[1].Name != "Receipts 2026" || len(list[1].Mounts) != 1 {
		t.Fatalf("list: %+v", list)
	}
	var details sessionDetails
	json.Unmarshal(c.ok("session.open", map[string]any{"repo": docs}), &details)
	if len(details.Workspace.Mounts) != 1 || details.Workspace.Mounts[0].Path != ref {
		t.Fatalf("session workspace: %+v", details.Workspace)
	}
	shell := false
	for _, tool := range details.Tools {
		shell = shell || tool.Name == "shell-exec"
	}
	if !shell {
		t.Fatal("a workspace session does not offer shell-exec")
	}
	if r := c.call("session.eval", map[string]any{"id": details.ID, "source": "(+ 1 2)"}); r.Error != nil {
		t.Fatalf("a workspace session refused Scheme: %+v", r.Error)
	}
	c.ok("workspace.remove", map[string]any{"path": code})
	json.Unmarshal(c.ok("workspace.list", map[string]any{}), &list)
	if len(list) != 1 {
		t.Fatalf("after remove: %+v", list)
	}
}

func TestRestoreAKeptVersionAsksFirst(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	repo := t.TempDir() // no .git: nodwyn keeps versions
	id := opened(t, c, repo)
	subscribed(t, c, id, 0, 100)
	for _, content := range []string{"first", "second"} {
		eval(c, id, `(fs-write "a.txt" "`+content+`")`)
		p := prompt[event.Confirm](t, c, event.PromptConfirm)
		c.ok("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": "y"})
		finishEval(t, c)
	}
	var versions []prim.Version
	json.Unmarshal(c.ok("versions.list", map[string]any{"repo": repo, "path": "a.txt"}), &versions)
	if len(versions) != 1 {
		t.Fatalf("versions: %+v", versions)
	}
	if r := c.call("session.restore", map[string]any{"id": id, "path": "a.txt", "hash": "0000000000000000000000000000000000000000000000000000000000000000"}); r.Error == nil {
		t.Fatal("restored an unknown version")
	}
	c.ok("session.restore", map[string]any{"id": id, "path": "a.txt", "hash": versions[0].Hash})
	p := prompt[event.Confirm](t, c, event.PromptConfirm)
	if p.Prompt.Path != "a.txt" || p.Prompt.Detail == "" {
		t.Fatalf("restore prompt: %+v", p.Prompt)
	}
	c.ok("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": "y"})
	result := prompt[event.Policy](t, c, event.PolicyResult)
	if !result.Saved || result.Name != "restore" {
		t.Fatalf("restore result: %+v", result)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(b) != "first" {
		t.Fatalf("a.txt: %q", b)
	}
	json.Unmarshal(c.ok("versions.list", map[string]any{"repo": repo, "path": "a.txt"}), &versions)
	if len(versions) != 2 {
		t.Fatalf("the restore itself was not undoable: %+v", versions)
	}
}

// The folder the console last picked for a new task is the daemon's to
// keep: the window's console is on a new loopback port every launch (ADR
// 30), so the browser's own memory cannot carry a pick across a restart,
// and the next daemon reads it back from the config directory.
func TestWorkspaceActiveSurvivesRestart(t *testing.T) {
	o := options(t)
	path, stop := start(t, o)
	c := connect(t, path)
	c.hello()
	var got workspaceActiveResult
	json.Unmarshal(c.ok("workspace.active", map[string]any{}), &got)
	if got.Path != "" {
		t.Fatalf("picked before anything was: %q", got.Path)
	}
	folder := t.TempDir()
	json.Unmarshal(c.ok("workspace.active", map[string]any{"path": folder}), &got)
	if got.Path != folder {
		t.Fatalf("a pick was reported as %q, not %q", got.Path, folder)
	}
	if stored, err := config.LoadLastWorkspace(config.LastWorkspaceFile(o.Paths)); err != nil || stored != folder {
		t.Fatalf("on disk: %q %v", stored, err)
	}
	if r := c.call("workspace.active", map[string]any{"path": "notes"}); r.Error == nil {
		t.Fatal("accepted a relative folder")
	}
	if r := c.call("workspace.active", map[string]any{"path": filepath.Join(folder, "gone")}); r.Error == nil {
		t.Fatal("accepted a folder that does not exist")
	}
	// A restart is a new daemon on the same config directory: it reads the
	// pick back, and a window that has not picked starts there.
	stop()
	path, _ = start(t, o)
	c = connect(t, path)
	c.hello()
	json.Unmarshal(c.ok("workspace.active", map[string]any{}), &got)
	if got.Path != folder {
		t.Fatalf("after a restart: %q, not %q", got.Path, folder)
	}
}

func TestUnsafeRootsAreRefused(t *testing.T) {
	o := options(t)
	os.MkdirAll(o.Paths.ConfigDir, 0o700)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	home, _ := os.UserHomeDir()
	for _, root := range []string{"/", home, o.Paths.ConfigDir, filepath.Dir(o.Paths.ConfigDir)} {
		if r := c.call("session.open", map[string]any{"repo": root}); r.Error == nil {
			t.Errorf("opened a task rooted at %s", root)
		}
	}
	if r := c.call("versions.list", map[string]any{"repo": t.TempDir(), "path": "a"}); r.Error == nil {
		t.Error("listed versions of a folder nodwyn never worked in")
	}
}

// A directory that does not exist yet resolves through the links above it,
// so a root that would hold nodwyn's config is refused whether or not the
// config directory was created (on macOS /tmp and /var are links).
func TestEvalOrResolvesMissingDirectoriesThroughLinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(real)
	if got, want := evalOr(filepath.Join(link, "config", "nodwyn")), filepath.Join(resolved, "config", "nodwyn"); got != want {
		t.Fatalf("evalOr = %q, want %q", got, want)
	}
}
