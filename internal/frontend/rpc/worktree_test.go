package rpc

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/prim"
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

// A task in a worktree keeps its versions with its repository's (ADR 34),
// so versions.list for the worktree's folder reads them there: a History
// list still has them after close removed the worktree and resume made it
// again.
func TestVersionsListReadsAWorktreesVersionsFromItsRepository(t *testing.T) {
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
	path, _ := start(t, options(t))
	c := connect(t, path)
	c.ok("hello", map[string]any{"protocol": 1})
	var details sessionDetails
	if err := json.Unmarshal(c.ok("worktree.open", map[string]any{"repo": repo}), &details); err != nil {
		t.Fatal(err)
	}
	// What the session's fs-write keeps before replacing a.txt.
	abs := filepath.Join(details.Repo, "a.txt")
	if err := os.WriteFile(abs, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&prim.Versions{Dir: prim.VersionsDir(details.Worktree.Repository)}).Keep(abs); err != nil {
		t.Fatal(err)
	}
	var versions []prim.Version
	if err := json.Unmarshal(c.ok("versions.list", map[string]any{"repo": details.Repo, "path": "a.txt"}), &versions); err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Path != abs {
		t.Fatalf("versions of the worktree's a.txt: %+v", versions)
	}
}

func TestVersionsDiffUsesParentRepositoryStore(t *testing.T) {
	o := options(t)
	socket, _ := start(t, o)
	c := connect(t, socket)
	c.hello()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "first"}} {
		cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	var task sessionDetails
	if err := json.Unmarshal(c.ok("worktree.open", map[string]any{"repo": repo}), &task); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(task.Repo, "a.txt")
	if err := os.WriteFile(abs, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := &prim.Versions{Dir: prim.VersionsDir(repo)}
	if err := v.Keep(abs); err != nil {
		t.Fatal(err)
	}
	list, err := prim.ListVersions(v.Dir, abs)
	if err != nil || len(list) != 1 {
		t.Fatalf("versions: %+v %v", list, err)
	}
	if err := os.WriteFile(abs, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := c.ok("versions.diff", map[string]any{"repo": task.Repo, "path": "a.txt", "hash": list[0].Hash})
	if !strings.Contains(string(result), "+after") || !strings.Contains(string(result), "-before") {
		t.Fatalf("comparison: %s", result)
	}
}

// Checking out a task's worktree runs the repository's hooks and filters,
// which can take a while: the daemon goes on answering other methods
// meanwhile, rather than holding every client until git is done.
func TestWorktreeCheckoutDoesNotHoldOtherMethods(t *testing.T) {
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
	// A post-checkout hook that holds the checkout until the test lets go,
	// and gives up by itself after 20 s.
	marks := t.TempDir()
	started, release := filepath.Join(marks, "started"), filepath.Join(marks, "release")
	hook := "#!/bin/sh\n: > '" + started + "'\ni=0\nwhile [ ! -e '" + release + "' ] && [ $i -lt 400 ]; do sleep 0.05; i=$((i+1)); done\n"
	if err := os.WriteFile(filepath.Join(repo, ".git", "hooks", "post-checkout"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.WriteFile(release, nil, 0o644)
	path, _ := start(t, options(t))
	opener, other := connect(t, path), connect(t, path)
	opener.ok("hello", map[string]any{"protocol": 1})
	other.ok("hello", map[string]any{"protocol": 1})
	opener.conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := opener.enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "worktree.open", "params": map[string]any{"repo": repo}}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the checkout's hook never ran")
		}
	}
	other.conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := other.enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 8, "method": "session.list", "params": struct{}{}}); err != nil {
		t.Fatal(err)
	}
	listed := make(chan error, 1)
	go func() {
		var r reply
		listed <- other.dec.Decode(&r)
	}()
	select {
	case err := <-listed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session.list waited for another task's worktree checkout")
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for {
		var r reply
		if err := opener.dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		if r.Method != "" {
			continue
		}
		if r.Error != nil {
			t.Fatalf("worktree.open: %+v", r.Error)
		}
		break
	}
}
