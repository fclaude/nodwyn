package rpc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
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
	// EarlierTasks: the model may search this folder's other tasks (ADR 50).
	EarlierTasks bool `json:"earlier_tasks"`
}
type workspaceSaveParams struct {
	Path   string         `json:"path"`
	Name   string         `json:"name"`
	Mounts []config.Mount `json:"mounts"`
	// EarlierTasks, when given, turns searching earlier tasks on or off;
	// absent keeps what was saved, so a client that does not know it (an
	// older console, Home's naming of a folder) never turns it off.
	EarlierTasks *bool `json:"earlier_tasks,omitempty"`
}
type workspacePathParams struct {
	Path string `json:"path"`
}
type workspaceActiveParams struct {
	Path string `json:"path,omitempty"` // absent reads the pick
}
type workspaceActiveResult struct {
	Path string `json:"path"`
}
type newTaskLastParams struct {
	Choices *config.NewTaskChoices `json:"choices,omitempty"` // absent reads them
}
type versionsParams struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
}
type versionDiffParams struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
	Hash string `json:"hash"`
}
type versionDiffResult struct {
	Label string `json:"label"`
	Diff  string `json:"diff"`
}

const maxVersionDiffBytes = 128 << 10

// A review's changes together are kept to half the 1 MiB frame as JSON, which
// leaves room for the rest of the reply; the files after that are counted,
// not listed. The read as a whole is bounded in time too, as it runs git for
// each file.
const (
	maxReviewDiffBytes = 512 << 10
	reviewDiffTimeout  = 30 * time.Second
)

type restoreParams struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Hash string `json:"hash"`
}

func describeWorkspace(r config.Registered) workspaceInfo {
	info := workspaceInfo{Name: r.Name, Path: r.Path, Mounts: []config.Mount{}, EarlierTasks: r.EarlierTasks}
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
	case "workspace.forgotten":
		// The folders the console leaves out of its suggestions, a method of
		// its own so that workspace.list keeps one result type and a console
		// on an older daemon, which lacks it, still lists its workspaces.
		if err := params(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		forgotten, err := config.LoadForgotten(registry)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		if forgotten == nil {
			forgotten = []string{}
		}
		return forgotten, nil
	case "workspace.active":
		// The folder the console last picked for a new task, which a window
		// or tab that has not picked yet starts in. The window's console is
		// on a new loopback port every launch (ADR 30), so the browser's own
		// memory cannot carry the pick across a restart; the daemon's can.
		var p workspaceActiveParams
		if err := params(req.Params, &p, "path"); err != nil {
			return nil, err
		}
		last := config.LastWorkspaceFile(s.options.Paths)
		if p.Path == "" {
			folder, err := config.LoadLastWorkspace(last)
			if err != nil {
				return nil, failure(-32000, err.Error())
			}
			return workspaceActiveResult{Path: folder}, nil
		}
		if err := absolute("path", p.Path); err != nil {
			return nil, err
		}
		p.Path = config.CanonicalPath(p.Path)
		if info, err := os.Stat(p.Path); err != nil || !info.IsDir() {
			return nil, failure(-32602, "path must be an existing folder")
		}
		if err := config.SaveLastWorkspace(last, p.Path); err != nil {
			return nil, failure(-32000, err.Error())
		}
		return workspaceActiveResult(p), nil
	case "workspace.remove":
		var p workspacePathParams
		if err := params(req.Params, &p, "path"); err != nil {
			return nil, err
		}
		if err := absolute("path", p.Path); err != nil {
			return nil, err
		}
		p.Path = config.CanonicalPath(p.Path)
		last := config.LastWorkspaceFile(s.options.Paths)
		folder, err := config.LoadLastWorkspace(last)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		if folder == p.Path {
			if err := config.SaveLastWorkspace(last, ""); err != nil {
				return nil, failure(-32000, err.Error())
			}
		}
		if err := config.RemoveRegistered(registry, p.Path); err != nil {
			return nil, failure(-32000, err.Error())
		}
		return struct{}{}, nil
	}
	var p workspaceSaveParams
	if err := params(req.Params, &p, "path", "name", "mounts", "earlier_tasks"); err != nil {
		return nil, err
	}
	if err := s.folder("path", p.Path); err != nil {
		return nil, err
	}
	p.Path = config.CanonicalPath(p.Path)
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = filepath.Base(p.Path)
	}
	earlier := false
	if p.EarlierTasks != nil {
		earlier = *p.EarlierTasks
	} else {
		ws, err := config.LoadWorkspace(registry, p.Path)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		earlier = ws.EarlierTasks
	}
	saved, err := config.SaveRegistered(registry, config.Registered{Name: name, Path: p.Path, Mounts: p.Mounts, EarlierTasks: earlier})
	if err != nil {
		return nil, failure(-32602, err.Error())
	}
	return describeWorkspace(saved), nil
}

// newTaskLast reads, or replaces, what the console chose last for a new task
// (ADR 41, amended 2026-10-27): the next task starts with it. Like the folder
// picked last, it is the daemon's to keep, so every console starts with it,
// whatever browser and address it is in. It only preselects:
// session.open checks each choice again, and refuses to a paired device the
// ones that are this computer's, so a device does not keep them either.
func (s *server) newTaskLast(req request, client *peer) (any, *rpcError) {
	var p newTaskLastParams
	if err := params(req.Params, &p, "choices"); err != nil {
		return nil, err
	}
	file := config.NewTaskFile(s.options.Paths)
	if p.Choices == nil {
		choices, err := config.LoadNewTask(file)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		return choices, nil
	}
	if why := client.refuseDevice("The choices a new task starts with are made on the computer nodwyn runs on, not from a paired device"); why != nil {
		return nil, why
	}
	if err := config.CheckNewTask(*p.Choices); err != nil {
		return nil, failure(-32602, err.Error())
	}
	for i := range p.Choices.Hosts {
		for j := range p.Choices.Hosts[i].Worktree {
			w := &p.Choices.Hosts[i].Worktree[j]
			// A linked host's folders are its own: this computer cannot
			// resolve them, so only its own are made canonical.
			if p.Choices.Hosts[i].Host == "" {
				w.Folder = config.CanonicalPath(w.Folder)
			}
		}
	}
	if p.Choices.Hosts == nil {
		p.Choices.Hosts = []config.NewTaskForHost{}
	}
	if err := config.SaveNewTask(file, *p.Choices); err != nil {
		return nil, failure(-32000, err.Error())
	}
	return *p.Choices, nil
}

func (s *server) versions(req request, client *peer) (any, *rpcError) {
	if req.Method == "versions.diff" {
		return s.versionDiff(req)
	}
	if req.Method == "versions.list" {
		var p versionsParams
		if err := params(req.Params, &p, "repo", "path"); err != nil {
			return nil, err
		}
		root := config.CanonicalPath(p.Repo)
		store, known := s.versionStore(root)
		if !filepath.IsAbs(p.Repo) || !known {
			return nil, failure(-32602, "repo must be a folder nodwyn works in")
		}
		if reason := s.rootRefusal(root); reason != "" {
			return nil, failure(-32602, reason)
		}
		abs := p.Path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(p.Repo, abs)
		}
		list, err := prim.ListVersions(store, config.CanonicalPath(abs))
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
	owned, rerr := s.idle(p.ID, "restoring a file")
	if rerr != nil {
		return nil, rerr
	}
	operation, err := owned.session.Restore(p.Path, p.Hash, client.actor)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return inspectionStarted{operation}, nil
}

func (s *server) versionDiff(req request) (any, *rpcError) {
	var p versionDiffParams
	if err := params(req.Params, &p, "repo", "path", "hash"); err != nil {
		return nil, err
	}
	root := config.CanonicalPath(p.Repo)
	store, known := s.versionStore(root)
	if !filepath.IsAbs(p.Repo) || !known {
		return nil, failure(-32602, "repo must be a folder nodwyn works in")
	}
	if reason := s.rootRefusal(root); reason != "" {
		return nil, failure(-32602, reason)
	}
	abs := p.Path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs = config.CanonicalPath(abs)
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, failure(-32602, "path must be inside repo")
	}
	realRel, err := filepath.Rel(evalOr(root), evalOr(abs))
	if err != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
		return nil, failure(-32602, "path escapes repo")
	}
	deny := guard.DefaultDenylist()
	deny.Root = root
	deny.Dirs = append(deny.Dirs, s.options.Paths.ConfigDir, s.options.Paths.DataDir)
	if reason := deny.Denies(abs); reason != "" {
		return nil, failure(-32602, reason)
	}
	list, err := prim.ListVersions(store, abs)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	var kept *prim.Version
	for i := range list {
		if list[i].Hash == p.Hash {
			kept = &list[i]
			break
		}
	}
	if kept == nil {
		return nil, failure(-32602, "hash must identify a kept version of this file")
	}
	// The index is repository-written data, not authority to read arbitrary objects.
	if len(p.Hash) != 64 || strings.Trim(p.Hash, "0123456789abcdef") != "" {
		return nil, failure(-32602, "invalid version hash")
	}
	label := "Kept version → current file (read-only comparison)"
	large := versionDiffResult{Label: label, Diff: "(diff too large to show)"}
	object, err := os.Lstat(filepath.Join(store, "objects", p.Hash))
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	if !object.Mode().IsRegular() {
		return nil, failure(-32602, "version object must be a regular file")
	}
	if kept.Bytes > maxVersionDiffBytes || object.Size() > maxVersionDiffBytes {
		return large, nil
	}
	before, err := prim.ReadVersion(store, p.Hash)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	if len(before) > maxVersionDiffBytes {
		return large, nil
	}
	pin, err := os.OpenRoot(root)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	defer pin.Close()
	var after []byte
	current, statErr := pin.Stat(rel)
	if statErr == nil && !current.Mode().IsRegular() {
		return nil, failure(-32602, "current path must be a regular file")
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, failure(-32000, statErr.Error())
	}
	file, err := pin.Open(rel)
	if err == nil {
		defer file.Close()
		info, statErr := file.Stat()
		if statErr != nil {
			return nil, failure(-32000, statErr.Error())
		}
		if !info.Mode().IsRegular() {
			return nil, failure(-32602, "current path must be a regular file")
		}
		after, err = io.ReadAll(io.LimitReader(file, maxVersionDiffBytes+1))
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		if len(after) > maxVersionDiffBytes {
			return large, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, failure(-32000, err.Error())
	}
	return versionDiffResult{Label: label, Diff: prim.Unified(rel, string(before), string(after), 3)}, nil
}

type worktreeDiffParams struct {
	ID string `json:"id"`
}
type worktreeDiffResult struct {
	Base    string        `json:"base"`
	Changes []prim.Change `json:"changes"`
	// Omitted counts the changed files after the ones listed, which the reply
	// had no room for.
	Omitted int `json:"omitted"`
}

// reviewComment is one remark on a line of a changed file. Line is the line
// in the file as it is now, 0 for a remark on the whole file. The line's own
// text is not sent: it is file content, which would reach the model in a
// message that carries the user's authority, and the model can read the line.
type reviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
	Text string `json:"text"`
}
type worktreeReviewParams struct {
	ID       string          `json:"id"`
	Comments []reviewComment `json:"comments"`
	Note     string          `json:"note,omitempty"`
}

// worktreeOf is the worktree of an open session, or the failure to report:
// review is for sessions in a worktree of their own (ADR 34), where the base
// commit says what the session changed.
func (s *server) worktreeOf(id string) (*log.Worktree, ownedSession, *rpcError) {
	o, ok := s.sessions[id]
	if !ok {
		return nil, ownedSession{}, failure(-32002, "Session is not open in this daemon")
	}
	w := o.session.Worktree()
	if w == nil {
		return nil, ownedSession{}, failure(-32000, "Review is for sessions in a worktree")
	}
	return w, o, nil
}

// worktreeDiff reads what a session changed in its worktree since its base
// commit. It only reads, with the daemon's authority, and leaves out what the
// denylist keeps from the model. Git runs without the lifecycle lock, as it
// does when a worktree is made, so a large branch does not hold every other
// method.
func (s *server) worktreeDiff(req request, _ *peer) (any, *rpcError) {
	var p worktreeDiffParams
	if err := params(req.Params, &p, "id"); err != nil {
		return nil, err
	}
	w, _, failed := s.worktreeOf(p.ID)
	if failed != nil {
		return nil, failed
	}
	o := prim.DefaultOptions(w.Path)
	o.Private = []string{s.options.Paths.ConfigDir, s.options.Paths.DataDir}
	ctx, cancel := context.WithTimeout(context.Background(), reviewDiffTimeout)
	defer cancel()
	s.mu.Unlock()
	changes, omitted, err := prim.ChangedSince(ctx, o, w.BaseCommit, maxVersionDiffBytes, maxReviewDiffBytes)
	s.mu.Lock()
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, failure(-32000, "Reading the changes took too long")
	}
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	if changes == nil {
		changes = []prim.Change{}
	}
	return worktreeDiffResult{Base: w.BaseCommit, Changes: changes, Omitted: omitted}, nil
}

// worktreeReview sends a review to the session as one message, so the agent
// answers all of it in one turn. A change of direction is not a comment: the
// user sends that as a message. The comments are numbered in the order of the
// file and line they are on, grouped under their file, whatever order they
// were written in.
func (s *server) worktreeReview(req request, _ *peer) (any, *rpcError) {
	var p worktreeReviewParams
	if err := params(req.Params, &p, "id", "comments", "note"); err != nil {
		return nil, err
	}
	if len(p.Comments) == 0 {
		return nil, failure(-32602, "comments must hold at least one comment")
	}
	_, owned, failed := s.worktreeOf(p.ID)
	if failed != nil {
		return nil, failed
	}
	for _, c := range p.Comments {
		if c.Path == "" || strings.TrimSpace(c.Text) == "" {
			return nil, failure(-32602, "each comment needs a path and text")
		}
	}
	slices.SortStableFunc(p.Comments, func(a, b reviewComment) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line))
	})
	var text strings.Builder
	text.WriteString("Code review of your changes in this worktree. Address each comment, then say what you changed.\n")
	if note := strings.TrimSpace(p.Note); note != "" {
		text.WriteString("\n" + note + "\n")
	}
	last := ""
	for i, c := range p.Comments {
		if c.Path != last {
			text.WriteString("\n" + c.Path + "\n")
			last = c.Path
		}
		where := "file"
		if c.Line > 0 {
			where = "line " + strconv.Itoa(c.Line)
		}
		fmt.Fprintf(&text, "%d. (%s) %s\n", i+1, where, strings.TrimSpace(c.Text))
	}
	if owned.driver.Active() {
		return nil, failure(-32000, "Stop the active run before sending a review")
	}
	if err := owned.session.Send(text.String()); err != nil {
		return nil, failure(-32000, err.Error())
	}
	return struct{}{}, nil
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
	if err := absolute("path", p.Path); err != nil {
		return nil, err
	}
	owned, rerr := s.idle(p.ID, "attaching")
	if rerr != nil {
		return nil, rerr
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

// absolute refuses a path that is not absolute: a relative one would mean
// the daemon's own working directory, not the caller's.
func absolute(name, path string) *rpcError {
	if !filepath.IsAbs(path) {
		return failure(-32602, name+" must be an absolute path")
	}
	return nil
}

// folder is the check every method makes of a folder it is to work in: an
// absolute path to an existing directory that rootRefusal does not forbid.
func (s *server) folder(name, path string) *rpcError {
	if err := absolute(name, path); err != nil {
		return err
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return failure(-32602, name+" must be an existing folder")
	}
	if why := s.rootRefusal(path); why != "" {
		return failure(-32602, why)
	}
	return nil
}

// sandboxFolder is folder for a run in sandbox mode (ADR 55), whose person
// declared the whole machine its disposable sandbox: there the folder is
// only where relative paths point, so it may be the whole disk, the home
// directory, or one holding nodwyn's or the denylist's directories, which the
// guard still refuses inside it.
func (s *server) sandboxFolder(name, path string) *rpcError {
	if err := absolute(name, path); err != nil {
		return err
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return failure(-32602, name+" must be an existing folder")
	}
	if why := s.rootRefusalIn(path, true); why != "" {
		return failure(-32602, why)
	}
	return nil
}

// rootRefusal says why a folder cannot be a task's root, or "". A root is
// where every relative path points and what a task may write: never the
// whole disk, the home directory itself, nodwyn's own configuration and data
// (config, keys, bindings, logs) or the credential directories the guard
// denies, nor a folder that contains any of them.
func (s *server) rootRefusal(root string) string { return s.rootRefusalIn(root, false) }

// rootRefusalIn is rootRefusal, or with sandbox the refusal of a sandbox
// run's folder: only nodwyn's and the denylist's directories and the
// folders inside them.
func (s *server) rootRefusalIn(root string, sandbox bool) string {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "the folder does not exist"
	}
	home, _ := os.UserHomeDir()
	if !sandbox && (real == "/" || (home != "" && real == evalOr(home))) {
		return "choose a folder inside your home directory, not the whole of it"
	}
	protected := append([]string{s.options.Paths.ConfigDir, s.options.Paths.DataDir}, guard.DefaultDenylist().Dirs...)
	for _, dir := range protected {
		dir = evalOr(dir)
		if dir == "" {
			continue
		}
		holds := strings.HasPrefix(dir, real+string(filepath.Separator))
		if real == dir || strings.HasPrefix(real, dir+string(filepath.Separator)) || holds && !sandbox {
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
func evalOr(p string) string { return config.CanonicalPath(p) }

// versionStore is where the versions of files under root are kept, and
// whether root is a named workspace or a folder a task used: what
// versions.list may read. A task's worktree keeps them with its
// repository's (ADR 34).
func (s *server) versionStore(root string) (string, bool) {
	infos, _ := s.store().List()
	for _, i := range infos {
		if config.CanonicalPath(i.Repo) == config.CanonicalPath(root) {
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
