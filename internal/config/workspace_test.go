package config

import (
	"os"
	"path/filepath"
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
	if err := RemoveRegistered(path, "/tmp/n"); err == nil {
		t.Fatal("removed twice")
	}
}

func TestNestedMountsRefused(t *testing.T) {
	root, outer := t.TempDir(), t.TempDir()
	registry := filepath.Join(t.TempDir(), "workspaces.toml")
	if _, err := SaveRegistered(registry, Registered{Name: "x", Path: root, Mounts: []Mount{{Path: outer, Access: "write"}, {Path: filepath.Join(outer, "ref"), Access: "read"}}}); err == nil {
		t.Fatal("nested mounts accepted")
	}
}
