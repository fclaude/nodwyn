package rpc

import (
	"os"

	"github.com/fclaude/nodwyn/internal/guard"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/prim"
)

// Workspaces (ADR 22 W1) as the console manages them: the named list in
// workspaces.toml, and each folder's kind and mounts in its own
// .nodwyn/workspace.toml. Tasks opened afterwards load them.

type workspaceInfo struct {
	Name   string         `json:"name"`
	Path   string         `json:"path"`
	Kind   string         `json:"kind"`
	Mounts []config.Mount `json:"mounts"`
	Git    bool           `json:"git"`
	Error  string         `json:"error,omitempty"`
}
type workspaceSaveParams struct {
	Path   string         `json:"path"`
	Name   string         `json:"name"`
	Kind   string         `json:"kind"`
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

func describeWorkspace(name, path string) workspaceInfo {
	info := workspaceInfo{Name: name, Path: path, Mounts: []config.Mount{}}
	_, err := os.Stat(filepath.Join(path, ".git"))
	info.Git = err == nil
	_, _ = config.MigrateFolder(path) // a folder named before ADR 31 keeps its settings
	ws, err := config.LoadWorkspace(path)
	if err != nil {
		info.Kind, info.Error = config.KindCode, err.Error()
		return info
	}
	info.Kind = ws.Kind
	if ws.Mounts != nil {
		info.Mounts = ws.Mounts
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
			result = append(result, describeWorkspace(r.Name, r.Path))
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
	if err := params(req.Params, &p, "path", "name", "kind", "mounts"); err != nil {
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
	if p.Kind == "" {
		p.Kind = config.KindCode
	}
	// A plain code repository keeps no workspace file: naming it must not
	// add a file to its diff. Anything else is written to the folder's
	// .nodwyn/workspace.toml, which the model cannot write (.nodwyn/ is protected).
	if _, err := config.MigrateFolder(p.Path); err != nil {
		return nil, failure(-32000, err.Error())
	}
	current, err := config.LoadWorkspace(p.Path)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	_, statErr := os.Stat(config.WorkspaceFile(p.Path))
	if statErr == nil || p.Kind != config.KindCode || len(p.Mounts) > 0 {
		if current.Kind != p.Kind || !slices.Equal(current.Mounts, p.Mounts) || statErr != nil {
			if _, err := config.SaveWorkspace(p.Path, config.Workspace{Kind: p.Kind, Mounts: p.Mounts}); err != nil {
				return nil, failure(-32602, err.Error())
			}
		}
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = filepath.Base(p.Path)
	}
	if err := config.SaveRegistered(registry, config.Registered{Name: name, Path: p.Path}); err != nil {
		return nil, failure(-32602, err.Error())
	}
	return describeWorkspace(name, p.Path), nil
}

func (s *server) versions(req request, client *peer) (any, *rpcError) {
	if req.Method == "versions.list" {
		var p versionsParams
		if err := params(req.Params, &p, "repo", "path"); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(p.Repo) || !s.knownFolder(filepath.Clean(p.Repo)) {
			return nil, failure(-32602, "repo must be a folder nodwyn works in")
		}
		abs := p.Path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(p.Repo, abs)
		}
		list, err := prim.ListVersions(prim.VersionsDir(filepath.Clean(p.Repo)), filepath.Clean(abs))
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
	receipt, err := owned.session.Attach(p.Path, p.Name, client.actor)
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

func evalOr(p string) string {
	if p == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

// knownFolder reports whether root is a named workspace or a folder a task
// used: what versions.list may read.
func (s *server) knownFolder(root string) bool {
	if list, err := config.LoadRegistry(config.RegistryFile(s.options.Paths)); err == nil {
		for _, r := range list {
			if r.Path == root {
				return true
			}
		}
	}
	infos, _ := s.store().List()
	for _, i := range infos {
		if i.Repo == root {
			return true
		}
	}
	return false
}
