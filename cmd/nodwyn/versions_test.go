package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/test/fakes/must"
)

// A task in a worktree keeps a file's versions in its repository's store
// (ADR 34), but nodwyn versions and nodwyn undo read the store of the folder
// they run in, so run in the worktree, as the task says to work, they found
// none. They read the repository's store too.
func TestVersionsAndUndoFindAWorktreeTasksVersions(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	repo, tree := filepath.Join(dir, "repo"), filepath.Join(dir, "tree")
	git := func(in string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = in
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	must.MkdirAll(t, repo, 0o755)
	git(repo, "init", "-q")
	must.WriteFile(t, filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "first")
	git(repo, "worktree", "add", "-q", "-b", "task", tree)
	file := filepath.Join(tree, "new.txt")
	must.WriteFile(t, file, []byte("v1\n"), 0o644)
	store := &prim.Versions{Dir: prim.VersionsDir(config.CanonicalPath(repo))}
	if err := store.Keep(file); err != nil {
		t.Fatal(err)
	}
	must.WriteFile(t, file, []byte("v2\n"), 0o644)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLIHelper$", "--"}, args...)...)
		cmd.Dir = tree
		cmd.Env = append(os.Environ(), "NODWYN_TEST_CLI=1", quickExit, "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_DATA_HOME="+filepath.Join(dir, "data"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if out := run("versions", "new.txt"); strings.Contains(out, "No kept versions") || !strings.Contains(out, "3 bytes") {
		t.Fatalf("versions in the worktree:\n%s", out)
	}
	if out := run("undo", "new.txt"); !strings.Contains(out, "Restored") {
		t.Fatalf("undo in the worktree:\n%s", out)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "v1\n" {
		t.Fatalf("new.txt is %q (%v)", b, err)
	}
}
