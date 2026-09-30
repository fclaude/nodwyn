package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// A workspace (ADR 22 §1) is a root directory plus a user-owned mount table,
// kept in <root>/.nibb/workspace.toml. Without the file a workspace is
// today's repository: kind "code", the root writable, nothing else.

// Workspace kinds. The kind selects tools and framing, never a security
// model: a docs workspace runs no programs and defines no tools.
const (
	KindCode  = "code"
	KindDocs  = "docs"
	KindMixed = "mixed"
)

// Mount is a directory outside the root a workspace may use.
type Mount struct {
	Path   string `toml:"path" json:"path"`     // absolute, or ~/… for the home directory
	Access string `toml:"access" json:"access"` // "read" or "write"
}

// Workspace is .nibb/workspace.toml.
type Workspace struct {
	Kind   string  `toml:"kind" json:"kind"`
	Mounts []Mount `toml:"mount" json:"mounts"`
}

// WorkspaceFile is where a root's workspace settings live.
func WorkspaceFile(root string) string { return filepath.Join(root, ".nibb", "workspace.toml") }

// LoadWorkspace reads root's workspace settings; a missing file is the
// default code workspace. Mount paths come back absolute and cleaned.
func LoadWorkspace(root string) (Workspace, error) {
	raw, err := os.ReadFile(WorkspaceFile(root))
	if errors.Is(err, os.ErrNotExist) {
		return Workspace{Kind: KindCode}, nil
	}
	if err != nil {
		return Workspace{}, err
	}
	return ParseWorkspace(raw, root)
}

// ParseWorkspace validates a workspace file's contents for root.
func ParseWorkspace(raw []byte, root string) (Workspace, error) {
	var ws Workspace
	md, err := toml.Decode(string(raw), &ws)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: %w", err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return Workspace{}, fmt.Errorf("workspace: unknown keys: %v", undec)
	}
	return ws.normalize(root)
}

func (ws Workspace) normalize(root string) (Workspace, error) {
	if ws.Kind == "" {
		ws.Kind = KindCode
	}
	if ws.Kind != KindCode && ws.Kind != KindDocs && ws.Kind != KindMixed {
		return Workspace{}, fmt.Errorf("workspace: kind must be code, docs or mixed, not %q", ws.Kind)
	}
	home, _ := os.UserHomeDir()
	seen := map[string]bool{}
	out := ws.Mounts[:0:0]
	for _, m := range ws.Mounts {
		if m.Access != "read" && m.Access != "write" {
			return Workspace{}, fmt.Errorf("workspace: mount %s: access must be read or write", m.Path)
		}
		path := m.Path
		if path == "~" || strings.HasPrefix(path, "~/") {
			if home == "" {
				return Workspace{}, fmt.Errorf("workspace: mount %s: no home directory", m.Path)
			}
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
		if !filepath.IsAbs(path) {
			return Workspace{}, fmt.Errorf("workspace: mount %s must be absolute or start with ~/", m.Path)
		}
		path = filepath.Clean(path)
		if path == "/" || (home != "" && path == filepath.Clean(home)) {
			return Workspace{}, fmt.Errorf("workspace: mount %s is too broad: name a folder inside it", m.Path)
		}
		if root != "" && (path == root || strings.HasPrefix(path, root+string(filepath.Separator)) || strings.HasPrefix(root, path+string(filepath.Separator))) {
			return Workspace{}, fmt.Errorf("workspace: mount %s overlaps the workspace itself", m.Path)
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, Mount{Path: path, Access: m.Access})
	}
	ws.Mounts = out
	return ws, nil
}

// SaveWorkspace writes root's workspace settings atomically (0600).
func SaveWorkspace(root string, ws Workspace) (Workspace, error) {
	ws, err := ws.normalize(root)
	if err != nil {
		return Workspace{}, err
	}
	var b strings.Builder
	b.WriteString("# This workspace's kind and the folders outside it nibb may use (ADR 22).\n")
	fmt.Fprintf(&b, "kind = %s\n", quote(ws.Kind))
	for _, m := range ws.Mounts {
		fmt.Fprintf(&b, "\n[[mount]]\npath = %s\naccess = %s\n", quote(m.Path), quote(m.Access))
	}
	path := WorkspaceFile(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Workspace{}, err
	}
	return ws, WriteFile(path, []byte(b.String()))
}

// Registered is a workspace the person named (workspaces.toml in the
// config directory): what the console lists and offers to start in.
type Registered struct {
	Name string `toml:"name" json:"name"`
	Path string `toml:"path" json:"path"`
}

type registry struct {
	Workspace []Registered `toml:"workspace"`
}

// RegistryFile is where named workspaces are kept.
func RegistryFile(p Paths) string { return filepath.Join(p.ConfigDir, "workspaces.toml") }

// LoadRegistry reads the named workspaces; missing is none.
func LoadRegistry(path string) ([]Registered, error) {
	var r registry
	_, err := toml.DecodeFile(path, &r)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspaces: %w", err)
	}
	return r.Workspace, nil
}

// SaveRegistered adds or renames the workspace at entry.Path.
func SaveRegistered(path string, entry Registered) error {
	entry.Name = strings.TrimSpace(entry.Name)
	if entry.Name == "" || len(entry.Name) > 60 || strings.ContainsAny(entry.Name, "\n\r\t") {
		return errors.New("workspaces: a name of at most 60 characters on one line is required")
	}
	if !filepath.IsAbs(entry.Path) {
		return errors.New("workspaces: path must be absolute")
	}
	list, err := LoadRegistry(path)
	if err != nil {
		return err
	}
	list = slices.DeleteFunc(list, func(r Registered) bool { return r.Path == entry.Path })
	list = append(list, entry)
	return writeRegistry(path, list)
}

// RemoveRegistered forgets a workspace; its folder and files are untouched.
func RemoveRegistered(path, root string) error {
	list, err := LoadRegistry(path)
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(slices.Clone(list), func(r Registered) bool { return r.Path == root })
	if len(kept) == len(list) {
		return fmt.Errorf("workspaces: %s is not registered", root)
	}
	return writeRegistry(path, kept)
}

func writeRegistry(path string, list []Registered) error {
	var b strings.Builder
	b.WriteString("# Workspaces nibb lists by name. Removing one here forgets the name, not the folder.\n")
	for _, r := range list {
		fmt.Fprintf(&b, "\n[[workspace]]\nname = %s\npath = %s\n", quote(r.Name), quote(r.Path))
	}
	return WriteFile(path, []byte(b.String()))
}
