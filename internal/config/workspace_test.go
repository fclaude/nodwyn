package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceDefaultsAndValidation(t *testing.T) {
	root := t.TempDir()
	ws, err := LoadWorkspace(root)
	if err != nil || ws.Kind != KindCode || len(ws.Mounts) != 0 {
		t.Fatalf("default: %+v %v", ws, err)
	}
	home, _ := os.UserHomeDir()
	ref := filepath.Join(t.TempDir(), "reference")
	saved, err := SaveWorkspace(root, Workspace{Kind: KindDocs, Mounts: []Mount{{Path: ref, Access: "read"}, {Path: "~/Sync/handbook", Access: "write"}, {Path: ref, Access: "read"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Mounts) != 2 || saved.Mounts[1].Path != filepath.Join(home, "Sync", "handbook") {
		t.Fatalf("saved: %+v", saved)
	}
	loaded, err := LoadWorkspace(root)
	if err != nil || loaded.Kind != KindDocs || len(loaded.Mounts) != 2 || loaded.Mounts[0].Access != "read" {
		t.Fatalf("loaded: %+v %v", loaded, err)
	}
	for _, bad := range []Workspace{
		{Kind: "shell"},
		{Mounts: []Mount{{Path: "relative", Access: "read"}}},
		{Mounts: []Mount{{Path: "/", Access: "read"}}},
		{Mounts: []Mount{{Path: "~", Access: "read"}}},
		{Mounts: []Mount{{Path: ref, Access: "execute"}}},
		{Mounts: []Mount{{Path: filepath.Join(root, "sub"), Access: "write"}}},
		{Mounts: []Mount{{Path: filepath.Dir(root), Access: "read"}}},
	} {
		if _, err := SaveWorkspace(root, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if _, err := ParseWorkspace([]byte("kind = \"docs\"\nshell = true\n"), root); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatal("unknown key accepted")
	}
}

func TestRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.toml")
	if list, err := LoadRegistry(path); err != nil || list != nil {
		t.Fatal(list, err)
	}
	if err := SaveRegistered(path, Registered{Name: "Receipts 2026", Path: "/tmp/r"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveRegistered(path, Registered{Name: "Notes", Path: "/tmp/n"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveRegistered(path, Registered{Name: "Receipts", Path: "/tmp/r"}); err != nil {
		t.Fatal(err)
	}
	list, _ := LoadRegistry(path)
	if len(list) != 2 || list[1].Name != "Receipts" {
		t.Fatalf("%+v", list)
	}
	if err := SaveRegistered(path, Registered{Name: "", Path: "/tmp/x"}); err == nil {
		t.Fatal("empty name")
	}
	if err := RemoveRegistered(path, "/tmp/n"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRegistered(path, "/tmp/n"); err == nil {
		t.Fatal("removed twice")
	}
}
