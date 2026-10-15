package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/prim"
)

// Sessions in git worktrees (ADR 34). A session asked for one gets a linked
// worktree of its own, on a branch of its own, made by the host before
// anything of the session loads: the model cannot move its own pin, and no
// primitive makes one. The worktree is the session's root; what concerns
// the project (notes, MCP servers' workspaces, the history strategies read)
// keys on the repository it belongs to.

// worktreeTimeout bounds one git command on a worktree: making one checks
// out the tree and runs the repository's hooks and filters (LFS).
const worktreeTimeout = 2 * time.Minute

// worktreesDir is where a repository's session worktrees live: inside it,
// so a backup of the folder holds them too, and hidden, so the repository's
// own sessions do not search them.
func worktreesDir(repository string) string {
	return filepath.Join(repository, config.FolderDir, "worktrees")
}

var errNoGit = errors.New("git is not installed")

// gitIn runs git with fixed arguments in dir, with the scrubbed environment
// and no prompt for credentials, and returns what it printed, or an error
// carrying git's own message. Unlike the git tools it runs the repository's
// hooks and filters, as the person's own git worktree add would.
func gitIn(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), worktreeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(prim.ScrubEnv(prim.DefaultOptions(dir).EnvAllow), "GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// An error names the command, past the -c settings before it.
	verb := args[0]
	for i := 0; i+2 < len(args) && args[i] == "-c"; i += 2 {
		verb = args[i+2]
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s took longer than %s", verb, worktreeTimeout)
		}
		if errors.Is(err, exec.ErrNotFound) {
			return "", errNoGit
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", verb, msg)
	}
	return stdout.String(), nil
}

// gitLine is gitIn's output as one line.
func gitLine(dir string, args ...string) (string, error) {
	out, err := gitIn(dir, args...)
	return strings.TrimSpace(out), err
}

// branchExists reports whether repository has the local branch name.
func branchExists(repository, name string) bool {
	_, err := gitIn(repository, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

// newWorktree makes session id's worktree of the repository repo is the
// top folder of: <repository>/.nodwyn/worktrees/<suffix> on the branch
// nodwyn/<suffix>, from repo's HEAD, where <suffix> is the random end of
// the session's id. repo may be a worktree itself; the new one is then its
// sibling, from its HEAD. Uncommitted changes in repo are not carried, and
// the result counts them. It refuses, in a sentence a person reads, a
// folder that is not a repository's top folder, a bare repository and one
// with no commit.
func newWorktree(repo, id string) (log.WorktreeChange, error) {
	var none log.WorktreeChange
	top, err := gitLine(repo, "rev-parse", "--show-toplevel")
	if err != nil {
		if bare, _ := gitLine(repo, "rev-parse", "--is-bare-repository"); bare == "true" {
			return none, fmt.Errorf("%s is a bare repository: a worktree needs one with files checked out", repo)
		}
		if errors.Is(err, errNoGit) {
			return none, err
		}
		return none, fmt.Errorf("%s is not a git repository: only a git repository can have worktrees", repo)
	}
	if !samePath(top, repo) {
		return none, fmt.Errorf("%s is a folder inside the repository %s: a worktree is made of a whole repository, so start the task there", repo, top)
	}
	commit, err := gitLine(repo, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil || commit == "" {
		return none, fmt.Errorf("%s has no commit yet: a worktree starts from one", repo)
	}
	repository, err := mainWorktree(repo)
	if err != nil {
		return none, err
	}
	// "" when HEAD is detached: the base is then only a commit.
	base, _ := gitLine(repo, "symbolic-ref", "--quiet", "--short", "HEAD")
	status, err := gitIn(repo, "-c", "core.fsmonitor=false", "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return none, err
	}
	dir, err := ensureWorktreesDir(repository)
	if err != nil {
		return none, err
	}
	suffix := id[strings.LastIndexByte(id, '-')+1:]
	w := log.Worktree{Repository: repository, Path: filepath.Join(dir, suffix), Branch: "nodwyn/" + suffix, Base: base, BaseCommit: commit}
	if _, err := gitIn(repository, "worktree", "add", "--quiet", "-b", w.Branch, w.Path, commit); err != nil {
		return none, err
	}
	// What the worktree does not have.
	left := 0
	for _, line := range strings.Split(status, "\n") {
		if line != "" {
			left++
		}
	}
	return log.WorktreeChange{Worktree: w, Uncommitted: left}, nil
}

// mainWorktree is the repository a git folder belongs to: its main
// worktree, which git lists first, whether the folder is that or a linked
// worktree of it.
func mainWorktree(dir string) (string, error) {
	out, err := gitIn(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(out, "\n")
	path, ok := strings.CutPrefix(first, "worktree ")
	if !ok || !filepath.IsAbs(path) {
		return "", fmt.Errorf("git did not name the repository %s belongs to", dir)
	}
	return filepath.Clean(path), nil
}

// samePath reports whether a and b are one folder, links resolved.
func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// ensureWorktreesDir makes .nodwyn/worktrees/ in repository, ignoring
// itself with a "*" .gitignore, as .nodwyn/attachments/ does: the
// repository's git status stays clean, and git add -A there never stages a
// worktree as a gitlink. Each folder on the way must be a folder, never a
// link: a clone could otherwise commit .nodwyn/worktrees as a link and have
// the next worktree checked out wherever it points.
func ensureWorktreesDir(repository string) (string, error) {
	dir := worktreesDir(repository)
	for _, d := range []string{filepath.Dir(dir), dir} {
		// Another task's worktree may be made at the same moment: a folder
		// that exists is fine, and a link is refused whoever made it.
		if err := os.Mkdir(d, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		st, err := os.Lstat(d)
		if err != nil {
			return "", err
		}
		if !st.IsDir() {
			return "", fmt.Errorf("%s is not a folder: nodwyn makes worktrees only in a folder of the repository's own", d)
		}
	}
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Lstat(ignore); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(ignore, []byte("*\n"), 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// discardWorktree removes a worktree nothing has happened in yet, with its
// branch: the session it was made for never started.
func discardWorktree(w log.Worktree) {
	_, _ = gitIn(w.Repository, "worktree", "remove", "--force", w.Path)
	_, _ = gitIn(w.Repository, "branch", "-D", w.Branch)
}

// ours reports whether w is one nodwyn made: under its repository's
// .nodwyn/worktrees/. The log recorded it, and the log is nodwyn's own;
// this keeps a removal or a restore from reaching anywhere else.
func ours(w log.Worktree) bool {
	return filepath.IsAbs(w.Repository) && filepath.Dir(w.Path) == worktreesDir(w.Repository) && strings.HasPrefix(w.Branch, "nodwyn/")
}

// removeLanded removes the session's worktree and its branch on close, when
// that loses nothing: nothing changed or left in it, untracked or ignored
// (an upload in .nodwyn/attachments/ is ignored, and git worktree remove
// would take it), and no commit its base lacks, on its branch or at its
// HEAD. Otherwise the worktree stays, and the session resumes in it. It
// reports whether it removed the worktree.
func removeLanded(w log.Worktree) (bool, error) {
	if !ours(w) {
		return false, nil
	}
	if _, err := os.Stat(w.Path); err != nil {
		return false, nil
	}
	// No file system monitor: the worktree's configuration is the
	// repository's, and status runs at close without anyone asking. Any
	// line keeps the worktree, so a folder of untracked or ignored files is
	// one line, not every file in it (a node_modules is thousands).
	status, err := gitIn(w.Path, "-c", "core.fsmonitor=false", "status", "--porcelain", "--ignored=traditional", "--untracked-files=normal")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(status) != "" {
		return false, nil
	}
	tip := w.BaseCommit
	if w.Base != "" && branchExists(w.Repository, w.Base) {
		tip = "refs/heads/" + w.Base
	}
	args := []string{"rev-list", "--count", "HEAD"}
	if branchExists(w.Repository, w.Branch) {
		args = append(args, "refs/heads/"+w.Branch)
	}
	ahead, err := gitLine(w.Path, append(args, "--not", tip)...)
	if err != nil {
		return false, err
	}
	if ahead != "0" {
		return false, nil
	}
	if _, err := gitIn(w.Repository, "worktree", "remove", w.Path); err != nil {
		return false, err
	}
	if branchExists(w.Repository, w.Branch) {
		if _, err := gitIn(w.Repository, "branch", "-D", w.Branch); err != nil {
			return true, err
		}
	}
	return true, nil
}

// restoreWorktree makes a resumed session's worktree again when it is
// missing (close removed it, or a person did): on its branch if that still
// exists, else on a new one of the same name from its base's head. It
// reports whether it made it, and, when it made a new branch, the commit
// that branch starts from: the worktree's base from then on.
func restoreWorktree(w log.Worktree) (made bool, base string, err error) {
	if _, err := os.Stat(w.Path); err == nil {
		return false, "", nil
	}
	if !ours(w) {
		return false, "", fmt.Errorf("the task's worktree %s is not one nodwyn made", w.Path)
	}
	if st, err := os.Stat(w.Repository); err != nil || !st.IsDir() {
		return false, "", fmt.Errorf("the repository %s this task's worktree belongs to is gone", w.Repository)
	}
	if _, err := ensureWorktreesDir(w.Repository); err != nil {
		return false, "", err
	}
	// A worktree deleted by hand is still registered, and its branch counts
	// as checked out there. Only its own registration is cleared: a prune
	// would also drop every other worktree whose folder is away for now,
	// the person's on a disk not mounted included.
	listed, err := gitIn(w.Repository, "worktree", "list", "--porcelain")
	if err != nil {
		return false, "", err
	}
	if slices.Contains(strings.Split(listed, "\n"), "worktree "+w.Path) {
		if _, err := gitIn(w.Repository, "worktree", "remove", w.Path); err != nil {
			return false, "", err
		}
	}
	args := []string{"worktree", "add", "--quiet", w.Path, w.Branch}
	if !branchExists(w.Repository, w.Branch) {
		from := w.BaseCommit
		if w.Base != "" && branchExists(w.Repository, w.Base) {
			from = "refs/heads/" + w.Base
		}
		// The new branch starts where its base is now, not where it was
		// when the task began, so that commit is the base Review compares
		// with and close checks against from here on (ADR 49 §3): kept as
		// the first one, every commit made on main since showed in Review
		// as the task's own change.
		if base, err = gitLine(w.Repository, "rev-parse", "--verify", from+"^{commit}"); err != nil {
			return false, "", err
		}
		args = []string{"worktree", "add", "--quiet", "-b", w.Branch, w.Path, base}
	}
	if _, err := gitIn(w.Repository, args...); err != nil {
		return false, "", err
	}
	return true, base, nil
}

// Worktree is the git worktree the session works in, nil when it works in
// the folder itself (ADR 34).
func (s *Session) Worktree() *log.Worktree {
	if s.worktree == nil {
		return nil
	}
	w := *s.worktree
	return &w
}

// project is the folder what concerns the project keys on: the repository
// the session's worktree belongs to, else its root (ADR 34).
func (s *Session) project() string {
	if s.worktree != nil {
		return config.CanonicalPath(s.worktree.Repository)
	}
	return s.opts.Repo
}

// versionsDir is where the session keeps a file's previous content before
// it writes (ADR 22 §2): the project's store, so a worktree's versions are
// kept with its repository's, outside the worktree. They are nodwyn's undo,
// not work left in it: they keep no worktree from going on close, and they
// outlast it (ADR 34).
func (s *Session) versionsDir() string {
	return prim.VersionsDir(s.project())
}

// Project is the folder the session's project is: the repository its
// worktree belongs to, else its root (ADR 34).
func (i SessionInfo) Project() string {
	if i.Worktree != nil {
		return config.CanonicalPath(i.Worktree.Repository)
	}
	return config.CanonicalPath(i.Repo)
}

// closeWorktree removes the worktree on close when nothing would be lost,
// and records what it did before session.end; a failure keeps it, and says
// why.
func (s *Session) closeWorktree() {
	if s.worktree == nil {
		return
	}
	removed, err := removeLanded(*s.worktree)
	if removed {
		_, _ = s.logw.Append(log.KindWorktreeRemove, log.WorktreeChange{Worktree: *s.worktree})
	}
	if err != nil {
		_, _ = s.logw.Append(log.KindNote, log.Note{Text: "The worktree was not removed: " + err.Error()})
	}
}
