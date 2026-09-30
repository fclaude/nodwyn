package rpc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/prim"
)

func TestWorkspaceSaveListAndDocsSessions(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	code, docs, ref := t.TempDir(), t.TempDir(), t.TempDir()
	var info workspaceInfo
	json.Unmarshal(c.ok("workspace.save", map[string]any{"path": code, "name": "nodwyn", "kind": "code", "mounts": []any{}}), &info)
	if _, err := os.Stat(config.WorkspaceFile(code)); err == nil {
		t.Fatal("naming a code repository wrote a workspace file into it")
	}
	json.Unmarshal(c.ok("workspace.save", map[string]any{"path": docs, "name": "Receipts 2026", "kind": "docs", "mounts": []any{map[string]any{"path": ref, "access": "read"}}}), &info)
	if info.Kind != "docs" || len(info.Mounts) != 1 {
		t.Fatalf("saved: %+v", info)
	}
	if r := c.call("workspace.save", map[string]any{"path": filepath.Join(docs, "missing"), "name": "x", "kind": "docs", "mounts": []any{}}); r.Error == nil {
		t.Fatal("saved a folder that does not exist")
	}
	if r := c.call("workspace.save", map[string]any{"path": docs, "name": "x", "kind": "shell", "mounts": []any{}}); r.Error == nil {
		t.Fatal("saved an unknown kind")
	}
	var list []workspaceInfo
	json.Unmarshal(c.ok("workspace.list", map[string]any{}), &list)
	if len(list) != 2 || list[1].Name != "Receipts 2026" || list[1].Kind != "docs" {
		t.Fatalf("list: %+v", list)
	}
	var details sessionDetails
	json.Unmarshal(c.ok("session.open", map[string]any{"repo": docs}), &details)
	if details.Workspace.Kind != "docs" || len(details.Workspace.Mounts) != 1 {
		t.Fatalf("session workspace: %+v", details.Workspace)
	}
	for _, tool := range details.Tools {
		if tool.Name == "shell-exec" {
			t.Fatal("docs session offers shell-exec")
		}
	}
	if r := c.call("session.eval", map[string]any{"id": details.ID, "source": "(+ 1 2)"}); r.Error == nil {
		t.Fatal("docs session evaluated Scheme")
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
