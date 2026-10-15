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

func TestForgetWorkspaceHistoryAndLastPick(t *testing.T) {
	o := options(t)
	socket, _ := start(t, o)
	c := connect(t, socket)
	c.hello()
	repo := t.TempDir()
	id := opened(t, c, repo)
	forgotten := func() []string {
		t.Helper()
		var list []string
		if err := json.Unmarshal(c.ok("workspace.forgotten", map[string]any{}), &list); err != nil {
			t.Fatal(err)
		}
		return list
	}
	c.ok("session.close", map[string]any{"id": id})
	c.ok("workspace.active", map[string]any{"path": repo})
	c.ok("workspace.remove", map[string]any{"path": repo})
	var last workspaceActiveResult
	if err := json.Unmarshal(c.ok("workspace.active", map[string]any{}), &last); err != nil || last.Path != "" {
		t.Fatalf("forgotten last pick: %+v %v", last, err)
	}
	if got := forgotten(); len(got) != 1 || got[0] != config.CanonicalPath(repo) {
		t.Fatalf("forgotten list: %v", got)
	}
	// workspace.list keeps its one result type, the named workspaces.
	var named []workspaceInfo
	if err := json.Unmarshal(c.ok("workspace.list", map[string]any{}), &named); err != nil || len(named) != 0 {
		t.Fatalf("workspace.list: %+v %v", named, err)
	}
	// The task is kept, so it resumes. Resuming a task there, starting one,
	// or naming the folder is using it again, and each brings it back.
	c.ok("session.resume", map[string]any{"id": id})
	if got := forgotten(); len(got) != 0 {
		t.Fatalf("resuming a task left the folder forgotten: %v", got)
	}
	c.ok("workspace.remove", map[string]any{"path": repo})
	opened(t, c, repo)
	if got := forgotten(); len(got) != 0 {
		t.Fatalf("a new task left the folder forgotten: %v", got)
	}
	c.ok("workspace.remove", map[string]any{"path": repo})
	c.ok("workspace.save", map[string]any{"path": repo, "name": "Again", "mounts": []any{}})
	if err := json.Unmarshal(c.ok("workspace.list", map[string]any{}), &named); err != nil || len(named) != 1 || len(forgotten()) != 0 {
		t.Fatalf("re-added: %+v %v", named, err)
	}
	if r := c.call("workspace.remove", map[string]any{"path": "relative"}); r.Error == nil {
		t.Fatal("forgot a relative path")
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
func TestVersionsDiff(t *testing.T) {
	o := options(t)
	socket, _ := start(t, o)
	c := connect(t, socket)
	c.hello()
	repo := t.TempDir()
	opened(t, c, repo)
	abs := filepath.Join(repo, "a.txt")
	before := "before\n"
	if err := os.WriteFile(abs, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	v := &prim.Versions{Dir: prim.VersionsDir(repo)}
	if err := v.Keep(abs); err != nil {
		t.Fatal(err)
	}
	list, err := prim.ListVersions(v.Dir, abs)
	if err != nil || len(list) != 1 {
		t.Fatalf("versions: %+v %v", list, err)
	}
	for _, tc := range []struct {
		name, current, want string
		missing             bool
	}{
		{"replacement", "after\n", "+after", false},
		{"addition", "before\nadded\n", "+added", false},
		{"identical", before, "", false},
		{"deletion", "", "-before", true},
		{"large bytes", strings.Repeat("x", 128<<10+1), "diff too large", false},
		{"large lines", strings.Repeat("x\n", 6001), "diff too large", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.missing {
				if err := os.Remove(abs); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(abs, []byte(tc.current), 0o600); err != nil {
				t.Fatal(err)
			}
			var result struct {
				Label string `json:"label"`
				Diff  string `json:"diff"`
			}
			if err := json.Unmarshal(c.ok("versions.diff", map[string]any{"repo": repo, "path": "a.txt", "hash": list[0].Hash}), &result); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Label, "Kept version → current file") || !strings.Contains(result.Diff, tc.want) {
				t.Fatalf("comparison: %+v", result)
			}
			if tc.name == "identical" && strings.Contains(result.Diff, "@@") {
				t.Fatalf("identical diff: %s", result.Diff)
			}
		})
	}
	if err := os.WriteFile(abs, []byte(strings.Repeat("x", 128<<10+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := v.Keep(abs); err != nil {
		t.Fatal(err)
	}
	list, err = prim.ListVersions(v.Dir, abs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(c.ok("versions.diff", map[string]any{"repo": repo, "path": "a.txt", "hash": list[0].Hash})), "diff too large") {
		t.Fatal("oversized kept version was rendered")
	}
}

func TestVersionsDiffRejectsUnsafeInputs(t *testing.T) {
	o := options(t)
	socket, _ := start(t, o)
	c := connect(t, socket)
	var hello struct {
		Methods []string `json:"methods"`
	}
	if err := json.Unmarshal(c.ok("hello", map[string]any{"protocol": 1}), &hello); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(hello.Methods, ","), "versions.diff") {
		t.Fatal("versions.diff is not available")
	}
	repo := t.TempDir()
	opened(t, c, repo)
	abs := filepath.Join(repo, "a.txt")
	if err := os.WriteFile(abs, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := &prim.Versions{Dir: prim.VersionsDir(repo)}
	if err := v.Keep(abs); err != nil {
		t.Fatal(err)
	}
	list, err := prim.ListVersions(v.Dir, abs)
	if err != nil || len(list) != 1 {
		t.Fatalf("versions: %+v %v", list, err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(repo, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ repo, path, hash string }{
		{repo, "../outside", list[0].Hash}, {repo, "/etc/passwd", list[0].Hash},
		{repo, "escape/a.txt", list[0].Hash}, {repo, ".env", list[0].Hash},
		{repo, "a.txt", "../index.jsonl"}, {repo, "a.txt", strings.Repeat("0", 64)},
		{repo, "other.txt", list[0].Hash}, {t.TempDir(), "a.txt", list[0].Hash},
	} {
		if r := c.call("versions.diff", map[string]any{"repo": tc.repo, "path": tc.path, "hash": tc.hash}); r.Error == nil {
			t.Errorf("accepted unsafe input: %+v", tc)
		}
	}
}

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
	if got.Path != config.CanonicalPath(folder) {
		t.Fatalf("a pick was reported as %q, not %q", got.Path, folder)
	}
	if stored, err := config.LoadLastWorkspace(config.LastWorkspaceFile(o.Paths)); err != nil || stored != config.CanonicalPath(folder) {
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
	if got.Path != config.CanonicalPath(folder) {
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

// workspace.save turns searching a folder's earlier tasks on and off, and a
// save that does not say keeps it as it was, as Home's naming of a folder
// and an older console send none; workspace.list reports it (ADR 50).
func TestWorkspaceSaveKeepsEarlierTasksUnlessGiven(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	code := t.TempDir()
	save := func(params map[string]any) workspaceInfo {
		t.Helper()
		var info workspaceInfo
		params["path"], params["mounts"] = code, []any{}
		if err := json.Unmarshal(c.ok("workspace.save", params), &info); err != nil {
			t.Fatal(err)
		}
		return info
	}
	if info := save(map[string]any{"name": "code"}); info.EarlierTasks {
		t.Fatal("on without being turned on")
	}
	if info := save(map[string]any{"name": "code", "earlier_tasks": true}); !info.EarlierTasks {
		t.Fatal("not turned on")
	}
	if info := save(map[string]any{"name": "renamed"}); !info.EarlierTasks || info.Name != "renamed" {
		t.Fatalf("a save without it turned it off: %+v", info)
	}
	var list []workspaceInfo
	json.Unmarshal(c.ok("workspace.list", map[string]any{}), &list)
	if len(list) != 1 || !list[0].EarlierTasks {
		t.Fatalf("list: %+v", list)
	}
	if info := save(map[string]any{"name": "renamed", "earlier_tasks": false}); info.EarlierTasks {
		t.Fatal("not turned off")
	}
}
