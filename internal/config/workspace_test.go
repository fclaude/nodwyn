package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A workspace's mounts are the person's, kept with its name in the
// registry; a folder nobody named has none, and nothing is written into the
// folder itself (ADR 22, amended 2026-10-03).
func TestWorkspaceMountsLiveInTheRegistry(t *testing.T) {
	root := t.TempDir()
	registry := filepath.Join(t.TempDir(), "workspaces.toml")
	for _, from := range []string{"", registry} {
		if ws, err := LoadWorkspace(from, root); err != nil || len(ws.Mounts) != 0 {
			t.Fatalf("an unnamed folder: %+v %v", ws, err)
		}
	}
	home, _ := os.UserHomeDir()
	ref := filepath.Join(t.TempDir(), "reference")
	saved, err := SaveRegistered(registry, Registered{Name: "Receipts", Path: root, Mounts: []Mount{{Path: ref, Access: "read"}, {Path: "~/Sync/handbook", Access: "write"}, {Path: ref, Access: "read"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Mounts) != 2 || saved.Mounts[1].Path != filepath.Join(home, "Sync", "handbook") {
		t.Fatalf("saved: %+v", saved)
	}
	loaded, err := LoadWorkspace(registry, root+string(filepath.Separator))
	if err != nil || len(loaded.Mounts) != 2 || loaded.Mounts[0] != (Mount{Path: ref, Access: "read"}) || loaded.Mounts[1].Access != "write" {
		t.Fatalf("loaded: %+v %v", loaded, err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("saving a workspace wrote into its folder: %v", entries)
	}
	// Renaming keeps nothing it was not given: the mounts are what the save
	// says.
	if _, err := SaveRegistered(registry, Registered{Name: "Receipts 2026", Path: root}); err != nil {
		t.Fatal(err)
	}
	if loaded, _ := LoadWorkspace(registry, root); len(loaded.Mounts) != 0 {
		t.Fatalf("after a save without mounts: %+v", loaded)
	}
	for _, bad := range [][]Mount{
		{{Path: "relative", Access: "read"}},
		{{Path: "/", Access: "read"}},
		{{Path: "~", Access: "read"}},
		{{Path: ref, Access: "execute"}},
		{{Path: filepath.Join(root, "sub"), Access: "write"}},
		{{Path: filepath.Dir(root), Access: "read"}},
	} {
		if _, err := SaveRegistered(registry, Registered{Name: "x", Path: root, Mounts: bad}); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	// A mount written into the registry by hand is checked as it is read.
	raw := "[[workspace]]\nname = \"x\"\npath = " + quote(root) + "\n\n[[workspace.mount]]\npath = \"/\"\naccess = \"read\"\n"
	if err := os.WriteFile(registry, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkspace(registry, root); err == nil || !strings.Contains(err.Error(), "too broad") {
		t.Fatalf("a mount of / was read: %v", err)
	}
}

func TestRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.toml")
	if list, err := LoadRegistry(path); err != nil || list != nil {
		t.Fatal(list, err)
	}
	if _, err := SaveRegistered(path, Registered{Name: "Receipts 2026", Path: "/tmp/r"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveRegistered(path, Registered{Name: "Notes", Path: "/tmp/n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveRegistered(path, Registered{Name: "Receipts", Path: "/tmp/r"}); err != nil {
		t.Fatal(err)
	}
	list, _ := LoadRegistry(path)
	if len(list) != 2 || list[1].Name != "Receipts" {
		t.Fatalf("%+v", list)
	}
	if _, err := SaveRegistered(path, Registered{Name: "", Path: "/tmp/x"}); err == nil {
		t.Fatal("empty name")
	}
	if err := RemoveRegistered(path, "/tmp/n"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRegistered(path, "/tmp/n"); err != nil {
		t.Fatalf("forgetting twice should be harmless: %v", err)
	}
}

// A folder is one workspace however its path is spelled: saving it again
// as /a/./b or /a//b/ replaces its entry rather than adding a second one
// that sessions never read, and removing it by another spelling removes it.
func TestRegistryKeysOnTheCleanPath(t *testing.T) {
	root, before, after := t.TempDir(), t.TempDir(), t.TempDir()
	registry := filepath.Join(t.TempDir(), "workspaces.toml")
	if _, err := SaveRegistered(registry, Registered{Name: "w", Path: root, Mounts: []Mount{{Path: before, Access: "read"}}}); err != nil {
		t.Fatal(err)
	}
	other := filepath.Dir(root) + "/./" + filepath.Base(root) + "//"
	saved, err := SaveRegistered(registry, Registered{Name: "w", Path: other, Mounts: []Mount{{Path: after, Access: "write"}}})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Path != CanonicalPath(root) {
		t.Fatalf("saved as %s, not %s", saved.Path, root)
	}
	list, err := LoadRegistry(registry)
	if err != nil || len(list) != 1 || list[0].Path != CanonicalPath(root) {
		t.Fatalf("registry: %+v %v", list, err)
	}
	ws, err := LoadWorkspace(registry, root)
	if err != nil || len(ws.Mounts) != 1 || ws.Mounts[0].Path != after || ws.Mounts[0].Access != "write" {
		t.Fatalf("a session there has %+v %v; want the mounts saved last", ws, err)
	}
	if err := RemoveRegistered(registry, filepath.Dir(root)+"//"+filepath.Base(root)); err != nil {
		t.Fatal(err)
	}
	if list, _ := LoadRegistry(registry); len(list) != 0 {
		t.Fatalf("left after removing: %+v", list)
	}
}

func TestForgetFolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.toml")
	named, unnamed := t.TempDir(), t.TempDir()
	if _, err := SaveRegistered(path, Registered{Name: "Named", Path: named}); err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{named, unnamed} {
		file := filepath.Join(folder, "keep.txt")
		if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := RemoveRegistered(path, folder); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(raw), "forgotten = [") || !strings.Contains(string(raw), quote(CanonicalPath(folder))) {
			t.Fatalf("forgotten folder not recorded: %s %v", raw, err)
		}
		if b, err := os.ReadFile(file); err != nil || string(b) != "keep" {
			t.Fatalf("forget changed the folder: %q %v", b, err)
		}
	}
	if _, err := SaveRegistered(path, Registered{Name: "Restored", Path: named}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "forgotten = ["+quote(CanonicalPath(unnamed))+"]") {
		t.Fatalf("adding did not clear just its forgotten marker: %s %v", raw, err)
	}
}

// A marker only hides a suggestion: one that is not an absolute path is
// skipped, so a session in any folder still loads its workspace, and using a
// forgotten folder clears just its own marker.
func TestForgottenFolderComesBackWhenUsed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.toml")
	used, other := t.TempDir(), t.TempDir()
	marked := "forgotten = [\"relative/folder\", " + quote(used) + ", " + quote(other) + "]\n"
	if err := os.WriteFile(path, []byte(marked), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkspace(path, t.TempDir()); err != nil {
		t.Fatalf("a bad marker refused a session elsewhere: %v", err)
	}
	if err := ClearForgotten(path, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != marked {
		t.Fatalf("a folder without a marker rewrote the registry: %q %v", raw, err)
	}
	if err := ClearForgotten(path, used); err != nil {
		t.Fatal(err)
	}
	forgotten, err := LoadForgotten(path)
	if err != nil || !slices.Equal(forgotten, []string{CanonicalPath(other)}) {
		t.Fatalf("forgotten after using %s: %v %v", used, forgotten, err)
	}
	if err := ClearForgotten("", used); err != nil {
		t.Fatalf("no registry: %v", err)
	}
}

func TestNestedMountsRefused(t *testing.T) {
	root, outer := t.TempDir(), t.TempDir()
	registry := filepath.Join(t.TempDir(), "workspaces.toml")
	if _, err := SaveRegistered(registry, Registered{Name: "x", Path: root, Mounts: []Mount{{Path: outer, Access: "write"}, {Path: filepath.Join(outer, "ref"), Access: "read"}}}); err == nil {
		t.Fatal("nested mounts accepted")
	}
}

// Whether the model may search a folder's earlier tasks is kept with its
// entry, off unless saved on, and read back with the workspace (ADR 50).
func TestWorkspaceKeepsWhetherEarlierTasksAreSearched(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "workspaces.toml")
	root := t.TempDir()
	if ws, err := LoadWorkspace(registry, root); err != nil || ws.EarlierTasks {
		t.Fatalf("a folder nobody named: %+v, %v", ws, err)
	}
	if _, err := SaveRegistered(registry, Registered{Name: "code", Path: root}); err != nil {
		t.Fatal(err)
	}
	if ws, _ := LoadWorkspace(registry, root); ws.EarlierTasks {
		t.Fatal("on without being turned on")
	}
	if _, err := SaveRegistered(registry, Registered{Name: "code", Path: root, EarlierTasks: true}); err != nil {
		t.Fatal(err)
	}
	if ws, _ := LoadWorkspace(registry, root); !ws.EarlierTasks {
		t.Fatal("turned on, read as off")
	}
	raw, _ := os.ReadFile(registry)
	if !strings.Contains(string(raw), "earlier_tasks = true") {
		t.Fatalf("workspaces.toml: %s", raw)
	}
}
