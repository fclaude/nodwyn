package rpc

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A client starts a task in a worktree of its own with worktree.open, which
// hello lists: the task's root is the worktree, and the task list and its
// details name the repository it belongs to and the branch, so a console
// groups it under the repository.
func TestWorktreeOpenStartsATaskInAWorktreeOfItsOwn(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "first"}} {
		cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	var hello helloResult
	json.Unmarshal(c.ok("hello", map[string]any{"protocol": 1}), &hello)
	if !slices.Contains(hello.Methods, "worktree.open") {
		t.Fatal("hello does not list worktree.open")
	}
	var details sessionDetails
	if err := json.Unmarshal(c.ok("worktree.open", map[string]any{"repo": repo}), &details); err != nil {
		t.Fatal(err)
	}
	w := details.Worktree
	if w == nil || w.Path != details.Repo || filepath.Dir(details.Repo) != filepath.Join(w.Repository, ".nodwyn", "worktrees") || !strings.HasPrefix(w.Branch, "nodwyn/") || w.Base != "main" || !details.Git {
		t.Fatalf("details: repo %s, worktree %+v, git %v", details.Repo, w, details.Git)
	}
	var list []sessionInfo
	json.Unmarshal(c.ok("session.list", struct{}{}), &list)
	if len(list) != 1 || list[0].Worktree == nil || *list[0].Worktree != *w || list[0].Repo != w.Path {
		t.Fatalf("list: %+v", list)
	}
	// A plain session.open still works in the folder itself.
	var plain sessionDetails
	json.Unmarshal(c.ok("session.open", map[string]any{"repo": repo}), &plain)
	if plain.Repo != repo || plain.Worktree != nil {
		t.Fatalf("session.open: repo %s, worktree %+v", plain.Repo, plain.Worktree)
	}
	if r := c.call("worktree.open", map[string]any{"repo": t.TempDir()}); r.Error == nil || !strings.Contains(r.Error.Message, "is not a git repository") {
		t.Fatalf("a folder without git: %+v", r.Error)
	}
	// Nothing happened in it, so closing the task takes the worktree away.
	c.ok("session.close", map[string]any{"id": details.ID})
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatalf("the worktree is still there after close: %v", err)
	}
}
