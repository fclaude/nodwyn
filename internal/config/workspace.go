package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

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
	root = CanonicalPath(root)
	for _, r := range list {
		if CanonicalPath(r.Path) == root {
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
		real := CanonicalPath(path)
		if real == "/" || (home != "" && real == CanonicalPath(home)) {
			return Workspace{}, fmt.Errorf("workspace: mount %s is too broad: name a folder inside it", m.Path)
		}
		if root != "" && (real == root || strings.HasPrefix(real, root+string(filepath.Separator)) || strings.HasPrefix(root, real+string(filepath.Separator))) {
			return Workspace{}, fmt.Errorf("workspace: mount %s overlaps the workspace itself", m.Path)
		}
		if seen[real] {
			continue
		}
		for _, o := range out {
			if strings.HasPrefix(real, CanonicalPath(o.Path)+string(filepath.Separator)) || strings.HasPrefix(CanonicalPath(o.Path), real+string(filepath.Separator)) {
				return Workspace{}, fmt.Errorf("workspace: mounts %s and %s overlap: mount one of them", o.Path, path)
			}
		}
		seen[real] = true
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
	Forgotten []string     `toml:"forgotten"`
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
	return CanonicalPath(last.Folder), nil
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
	fmt.Fprintf(&b, "folder = %s\n", quote(CanonicalPath(folder)))
	return WriteFile(path, []byte(b.String()))
}

// LoadRegistry reads the named workspaces; missing is none.
func LoadRegistry(path string) ([]Registered, error) {
	r, err := loadRegistry(path)
	return r.Workspace, err
}

// LoadForgotten reads folders hidden from suggestions, not from task history.
func LoadForgotten(path string) ([]string, error) {
	r, err := loadRegistry(path)
	return r.Forgotten, err
}

func loadRegistry(path string) (registry, error) {
	var r registry
	_, err := toml.DecodeFile(path, &r)
	if errors.Is(err, os.ErrNotExist) {
		return registry{}, nil
	}
	if err != nil {
		return registry{}, fmt.Errorf("workspaces: %w", err)
	}
	out := make([]Registered, 0, len(r.Workspace))
	for _, entry := range r.Workspace {
		entry.Path = CanonicalPath(entry.Path)
		for _, previous := range out {
			if previous.Path == entry.Path {
				return registry{}, fmt.Errorf("workspaces: duplicate aliases for %s; keep one entry with the intended mounts", entry.Path)
			}
		}
		out = append(out, entry)
	}
	r.Workspace = out
	// A marker only hides a suggestion, and every session start reads this
	// file for its mounts: one that is not an absolute path is skipped, not
	// a reason to refuse every task.
	forgotten := make([]string, 0, len(r.Forgotten))
	for _, folder := range r.Forgotten {
		if !filepath.IsAbs(folder) {
			continue
		}
		folder = CanonicalPath(folder)
		if !slices.Contains(forgotten, folder) {
			forgotten = append(forgotten, folder)
		}
	}
	r.Forgotten = forgotten
	return r, nil
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
	entry.Path = CanonicalPath(entry.Path)
	ws, err := Workspace{Mounts: entry.Mounts}.normalize(entry.Path)
	if err != nil {
		return Registered{}, err
	}
	entry.Mounts = ws.Mounts
	unlock, err := lockRegistry(path)
	if err != nil {
		return Registered{}, err
	}
	defer unlock()
	r, err := loadRegistry(path)
	if err != nil {
		return Registered{}, err
	}
	r.Workspace = slices.DeleteFunc(r.Workspace, func(w Registered) bool { return w.Path == entry.Path })
	r.Workspace = append(r.Workspace, entry)
	r.Forgotten = slices.DeleteFunc(r.Forgotten, func(folder string) bool { return folder == entry.Path })
	return entry, writeRegistry(path, r)
}

// RemoveRegistered forgets even an unnamed folder, without touching its files
// or tasks. The marker keeps task history from offering the folder again.
func RemoveRegistered(path, root string) error {
	if !filepath.IsAbs(root) {
		return errors.New("workspaces: path must be absolute")
	}
	unlock, err := lockRegistry(path)
	if err != nil {
		return err
	}
	defer unlock()
	r, err := loadRegistry(path)
	if err != nil {
		return err
	}
	root = CanonicalPath(root)
	r.Workspace = slices.DeleteFunc(r.Workspace, func(w Registered) bool { return w.Path == root })
	if !slices.Contains(r.Forgotten, root) {
		r.Forgotten = append(r.Forgotten, root)
	}
	return writeRegistry(path, r)
}

// ClearForgotten brings a forgotten folder back to the suggestions: a task
// started or resumed there is the person using it again. It writes only when
// root has a marker, so a session start leaves the file as it was otherwise.
// An empty registry path is no registry.
func ClearForgotten(path, root string) error {
	if path == "" {
		return nil
	}
	root = CanonicalPath(root)
	if r, err := loadRegistry(path); err != nil || !slices.Contains(r.Forgotten, root) {
		return err
	}
	unlock, err := lockRegistry(path)
	if err != nil {
		return err
	}
	defer unlock()
	r, err := loadRegistry(path)
	if err != nil || !slices.Contains(r.Forgotten, root) {
		return err
	}
	r.Forgotten = slices.DeleteFunc(r.Forgotten, func(folder string) bool { return folder == root })
	return writeRegistry(path, r)
}

// lockRegistry serializes changes to the registry across processes: the
// daemon's console writes it, and any session start, in the daemon or a
// terminal, may clear a marker. Each change rereads the file under the lock,
// so none is lost to another made meanwhile.
func lockRegistry(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path+".lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("workspaces: lock: %w", err)
	}
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("workspaces: lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = syscall.Close(fd)
	}, nil
}

func writeRegistry(path string, r registry) error {
	var b strings.Builder
	b.WriteString("# Workspaces nodwyn lists by name, and the folders outside each its tasks may use.\n")
	b.WriteString("# Removing one here forgets the name and its mounts, not the folder.\n")
	b.WriteString("# Forgotten folders stay out of suggestions until one is added or used again.\n")
	fmt.Fprintf(&b, "forgotten = %s\n", quoteList(r.Forgotten))
	for _, r := range r.Workspace {
		fmt.Fprintf(&b, "\n[[workspace]]\nname = %s\npath = %s\n", quote(r.Name), quote(r.Path))
		for _, m := range r.Mounts {
			fmt.Fprintf(&b, "\n[[workspace.mount]]\npath = %s\naccess = %s\n", quote(m.Path), quote(m.Access))
		}
	}
	return WriteFile(path, []byte(b.String()))
}
