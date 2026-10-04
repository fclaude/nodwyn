package rpc

import (
	"os"

	"github.com/fclaude/nodwyn/internal/guard"
	"path/filepath"
	"strings"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/prim"
)

// Workspaces (ADR 22 W1) as the console manages them: the named list in
// workspaces.toml in the config directory, each with the folders outside it
// its tasks may use. Tasks opened afterwards load them. Nothing is written
// into the folder itself.

type workspaceInfo struct {
	Name   string         `json:"name"`
	Path   string         `json:"path"`
	Mounts []config.Mount `json:"mounts"`
	Git    bool           `json:"git"`
}
type workspaceSaveParams struct {
	Path   string         `json:"path"`
	Name   string         `json:"name"`
	Mounts []config.Mount `json:"mounts"`
}
type workspacePathParams struct {
	Path string `json:"path"`
}
type versionsParams struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
}
type restoreParams struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Hash string `json:"hash"`
}

func describeWorkspace(r config.Registered) workspaceInfo {
	info := workspaceInfo{Name: r.Name, Path: r.Path, Mounts: []config.Mount{}}
	_, err := os.Stat(filepath.Join(r.Path, ".git"))
	info.Git = err == nil
	if r.Mounts != nil {
		info.Mounts = r.Mounts
	}
	return info
}

func (s *server) workspaces(req request, _ *peer) (any, *rpcError) {
	registry := config.RegistryFile(s.options.Paths)
	switch req.Method {
	case "workspace.list":
		if err := params(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		list, err := config.LoadRegistry(registry)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		result := []workspaceInfo{}
		for _, r := range list {
			result = append(result, describeWorkspace(r))
		}
		return result, nil
	case "workspace.remove":
		var p workspacePathParams
		if err := params(req.Params, &p, "path"); err != nil {
			return nil, err
		}
		if err := config.RemoveRegistered(registry, p.Path); err != nil {
			return nil, failure(-32000, err.Error())
		}
		return struct{}{}, nil
	}
	var p workspaceSaveParams
	if err := params(req.Params, &p, "path", "name", "mounts"); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(p.Path) {
		return nil, failure(-32602, "path must be absolute")
	}
	p.Path = filepath.Clean(p.Path)
	if info, err := os.Stat(p.Path); err != nil || !info.IsDir() {
		return nil, failure(-32602, "path must be an existing folder")
	}
	if why := s.rootRefusal(p.Path); why != "" {
		return nil, failure(-32602, why)
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = filepath.Base(p.Path)
	}
	saved, err := config.SaveRegistered(registry, config.Registered{Name: name, Path: p.Path, Mounts: p.Mounts})
	if err != nil {
		return nil, failure(-32602, err.Error())
	}
	return describeWorkspace(saved), nil
}

func (s *server) versions(req request, client *peer) (any, *rpcError) {
	if req.Method == "versions.list" {
		var p versionsParams
		if err := params(req.Params, &p, "repo", "path"); err != nil {
			return nil, err
		}
		store, known := s.versionStore(filepath.Clean(p.Repo))
		if !filepath.IsAbs(p.Repo) || !known {
			return nil, failure(-32602, "repo must be a folder nodwyn works in")
		}
		abs := p.Path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(p.Repo, abs)
		}
		list, err := prim.ListVersions(store, filepath.Clean(abs))
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		if list == nil {
			list = []prim.Version{}
		}
		return list, nil
	}
	var p restoreParams
	if err := params(req.Params, &p, "id", "path", "hash"); err != nil {
		return nil, err
	}
	owned, ok := s.sessions[p.ID]
	if !ok {
		return nil, failure(-32002, "Session is not open in this daemon")
	}
	if owned.driver.Active() {
		return nil, failure(-32000, "Stop the active run before restoring a file")
	}
	operation, err := owned.session.Restore(p.Path, p.Hash, client.actor)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return inspectionStarted{operation}, nil
}

type attachParams struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

// attach adds a local file to an idle session's conversation. The path is
// read with the daemon's authority, which is the connecting user's; the
// browser bridge never forwards a path a page chose (it stages uploads).
func (s *server) attach(req request, client *peer) (any, *rpcError) {
	var p attachParams
	if err := params(req.Params, &p, "id", "path", "name"); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(p.Path) {
		return nil, failure(-32602, "path must be absolute")
	}
	owned, ok := s.sessions[p.ID]
	if !ok {
		return nil, failure(-32002, "Session is not open in this daemon")
	}
	if owned.driver.Active() {
		return nil, failure(-32000, "Stop the active run before attaching")
	}
	// An image waits for its confirm, which a client answers with
	// session.answer, so the attachment is made without the lifecycle lock
	// (as mcp.inspect is). The session itself refuses a turn meanwhile, and
	// closing it cancels the attachment.
	session := owned.session
	s.mu.Unlock()
	receipt, err := session.Attach(p.Path, p.Name, client.actor)
	s.mu.Lock()
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return receipt, nil
}

// rootRefusal says why a folder cannot be a task's root, or "". A root is
// where every relative path points and what a task may write: never the
// whole disk, the home directory itself, nodwyn's own configuration and data
// (config, keys, bindings, logs) or the credential directories the guard
// denies, nor a folder that contains any of them.
func (s *server) rootRefusal(root string) string {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "the folder does not exist"
	}
	home, _ := os.UserHomeDir()
	if real == "/" || (home != "" && real == evalOr(home)) {
		return "choose a folder inside your home directory, not the whole of it"
	}
	protected := append([]string{s.options.Paths.ConfigDir, s.options.Paths.DataDir}, guard.DefaultDenylist().Dirs...)
	for _, dir := range protected {
		dir = evalOr(dir)
		if dir == "" {
			continue
		}
		if real == dir || strings.HasPrefix(real, dir+string(filepath.Separator)) || strings.HasPrefix(dir, real+string(filepath.Separator)) {
			return "that folder holds (or is inside) " + dir + ", which nodwyn never works in"
		}
	}
	return ""
}

// evalOr resolves p's symbolic links, as far as p exists: a directory that
// does not exist yet (a fresh config directory) is its nearest existing
// parent, resolved, joined with the rest. Comparing it with a resolved root
// must not depend on whether the directory was created (on macOS /var and
// /tmp are links into /private).
func evalOr(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	rest := ""
	for dir := p; ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// versionStore is where the versions of files under root are kept, and
// whether root is a named workspace or a folder a task used: what
// versions.list may read. A task's worktree keeps them with its
// repository's (ADR 34).
func (s *server) versionStore(root string) (string, bool) {
	infos, _ := s.store().List()
	for _, i := range infos {
		if i.Repo == root {
			return prim.VersionsDir(i.Project()), true
		}
	}
	if list, err := config.LoadRegistry(config.RegistryFile(s.options.Paths)); err == nil {
		for _, r := range list {
			if r.Path == root {
				return prim.VersionsDir(root), true
			}
		}
	}
	return "", false
}
