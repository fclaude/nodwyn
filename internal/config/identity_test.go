package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceAliasesShareSettings(t *testing.T) {
	base := t.TempDir()
	real, alias := filepath.Join(base, "real"), filepath.Join(base, "alias")
	if err := os.Mkdir(real, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(base, "workspaces.toml")
	mount := t.TempDir()
	// Use the old on-disk spelling to prove compatibility, not just new saves.
	raw := fmt.Sprintf("[[workspace]]\nname='old'\npath=%q\n[[workspace.mount]]\npath=%q\naccess='read'\n", alias, mount)
	if err := os.WriteFile(registry, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	ws, err := LoadWorkspace(registry, real)
	if err != nil || len(ws.Mounts) != 1 || ws.Mounts[0].Path != mount {
		t.Fatalf("alias lost mounts: %+v, %v", ws, err)
	}
	if _, err := SaveRegistered(registry, Registered{Name: "new", Path: real, Mounts: ws.Mounts}); err != nil {
		t.Fatal(err)
	}
	list, err := LoadRegistry(registry)
	if err != nil || len(list) != 1 || list[0].Name != "new" {
		t.Fatalf("alias duplicated workspace: %+v, %v", list, err)
	}
	if !(MCPServer{Workspaces: []string{alias}}).In(real) {
		t.Fatal("alias lost MCP authorisation")
	}
}
