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

// A workspace (ADR 22 §1) is a root directory plus a mount table: the
// folders outside it a session there may use. The table is the person's,
// kept with the workspace's name in workspaces.toml in the config directory,
// which only the console on this computer writes and no model can reach: a
// folder holds no settings of its own, so a repository someone else commits
// cannot widen what a session there may read or write (ADR 22, amended
// 2026-10-03). A folder nobody named has no mounts.

// Mount is a directory outside the root a workspace may use.
type Mount struct {
	Path   string `toml:"path" json:"path"`     // absolute, or ~/… for the home directory
	Access string `toml:"access" json:"access"` // "read" or "write"
}

// Workspace is what a session's folder may use beyond itself.
type Workspace struct {
	Mounts []Mount `json:"mounts"`
}

// LoadWorkspace is the workspace at root as the registry at registry names
// it: its mounts, absolute and cleaned, or none for a folder it does not
// name. An empty registry path is no registry.
func LoadWorkspace(registry, root string) (Workspace, error) {
	if registry == "" {
		return Workspace{}, nil
	}
	list, err := LoadRegistry(registry)
	if err != nil {
		return Workspace{}, err
	}
	root = filepath.Clean(root)
	for _, r := range list {
		if filepath.Clean(r.Path) == root {
			return Workspace{Mounts: r.Mounts}.normalize(root)
		}
	}
	return Workspace{}, nil
}

func (ws Workspace) normalize(root string) (Workspace, error) {
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
		for _, o := range out {
			if strings.HasPrefix(path, o.Path+string(filepath.Separator)) || strings.HasPrefix(o.Path, path+string(filepath.Separator)) {
				return Workspace{}, fmt.Errorf("workspace: mounts %s and %s overlap: mount one of them", o.Path, path)
			}
		}
		seen[path] = true
		out = append(out, Mount{Path: path, Access: m.Access})
	}
	ws.Mounts = out
	return ws, nil
}

// Registered is a workspace the person named (workspaces.toml in the
// config directory): what the console lists and offers to start in, and the
// folders outside it its sessions may use.
type Registered struct {
	Name   string  `toml:"name" json:"name"`
	Path   string  `toml:"path" json:"path"`
	Mounts []Mount `toml:"mount" json:"mounts"`
}

type registry struct {
	Workspace []Registered `toml:"workspace"`
}

// RegistryFile is where named workspaces are kept.
func RegistryFile(p Paths) string { return filepath.Join(p.ConfigDir, "workspaces.toml") }

// LastWorkspaceFile is where the folder the console last picked for a new
// task is kept, beside the registry it belongs to: what a window or tab
// that has not picked yet starts in. Only the daemon writes it, on a
// workspace.active from a console on this computer, and the person may edit
// it by hand; the config directory is private to the guard, so neither a
// model nor a repository can (ADR 22).
func LastWorkspaceFile(p Paths) string { return filepath.Join(p.ConfigDir, "last-workspace.toml") }

// LoadLastWorkspace reads the folder the console last picked for a new
// task; missing is none.
func LoadLastWorkspace(path string) (string, error) {
	var last struct {
		Folder string `toml:"folder"`
	}
	_, err := toml.DecodeFile(path, &last)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("last workspace: %w", err)
	}
	if last.Folder == "" {
		return "", nil
	}
	return filepath.Clean(last.Folder), nil
}

// SaveLastWorkspace records the folder the console last picked for a new
// task, or forgets the pick when folder is empty.
func SaveLastWorkspace(path, folder string) error {
	if folder != "" && !filepath.IsAbs(folder) {
		return errors.New("last workspace: path must be absolute")
	}
	var b strings.Builder
	b.WriteString("# The folder the console last picked for a new task, which a fresh\n")
	b.WriteString("# window or tab starts in. The daemon writes it; editing it by hand is fine.\n")
	fmt.Fprintf(&b, "folder = %s\n", quote(folder))
	return WriteFile(path, []byte(b.String()))
}

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

// SaveRegistered adds the workspace at entry.Path, or renames it and sets
// its mounts, and returns it as saved: its mounts absolute and cleaned.
func SaveRegistered(path string, entry Registered) (Registered, error) {
	entry.Name = strings.TrimSpace(entry.Name)
	if entry.Name == "" || len(entry.Name) > 60 || strings.ContainsAny(entry.Name, "\n\r\t") {
		return Registered{}, errors.New("workspaces: a name of at most 60 characters on one line is required")
	}
	if !filepath.IsAbs(entry.Path) {
		return Registered{}, errors.New("workspaces: path must be absolute")
	}
	// A folder is one entry however its path is spelled, as LoadWorkspace
	// finds it: another spelling replaces it rather than adding one that
	// no session reads.
	entry.Path = filepath.Clean(entry.Path)
	ws, err := Workspace{Mounts: entry.Mounts}.normalize(entry.Path)
	if err != nil {
		return Registered{}, err
	}
	entry.Mounts = ws.Mounts
	list, err := LoadRegistry(path)
	if err != nil {
		return Registered{}, err
	}
	list = slices.DeleteFunc(list, func(r Registered) bool { return filepath.Clean(r.Path) == entry.Path })
	list = append(list, entry)
	return entry, writeRegistry(path, list)
}

// RemoveRegistered forgets a workspace; its folder and files are untouched.
func RemoveRegistered(path, root string) error {
	list, err := LoadRegistry(path)
	if err != nil {
		return err
	}
	root = filepath.Clean(root)
	kept := slices.DeleteFunc(slices.Clone(list), func(r Registered) bool { return filepath.Clean(r.Path) == root })
	if len(kept) == len(list) {
		return fmt.Errorf("workspaces: %s is not registered", root)
	}
	return writeRegistry(path, kept)
}

func writeRegistry(path string, list []Registered) error {
	var b strings.Builder
	b.WriteString("# Workspaces nodwyn lists by name, and the folders outside each its tasks may use.\n")
	b.WriteString("# Removing one here forgets the name and its mounts, not the folder.\n")
	for _, r := range list {
		fmt.Fprintf(&b, "\n[[workspace]]\nname = %s\npath = %s\n", quote(r.Name), quote(r.Path))
		for _, m := range r.Mounts {
			fmt.Fprintf(&b, "\n[[workspace.mount]]\npath = %s\naccess = %s\n", quote(m.Path), quote(m.Access))
		}
	}
	return WriteFile(path, []byte(b.String()))
}
