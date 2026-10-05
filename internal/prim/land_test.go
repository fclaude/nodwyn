package prim

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// landRig is a repository with a bare remote its main tracks, a clean
// checkout of main (the person's), and a worktree on nodwyn/task made from
// main, as a session's is; land runs (git-land ...) in the worktree.
type landRig struct {
	t                  *testing.T
	remote, repo, tree string
	base               string
	in                 *scheme.Interp
}

func newLandRig(t *testing.T) *landRig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	r := &landRig{t: t, remote: filepath.Join(dir, "remote.git"), repo: filepath.Join(dir, "repo"), tree: filepath.Join(dir, "repo", ".nodwyn", "worktrees", "task")}
	r.git(dir, "init", "-q", "--bare", "-b", "main", r.remote)
	r.git(dir, "clone", "-q", r.remote, r.repo)
	// The identity is the repository's own, as git-land runs git with the
	// session's environment.
	r.git(r.repo, "config", "user.name", "t")
	r.git(r.repo, "config", "user.email", "t@t")
	r.write(r.repo, "app.txt", "one\ntwo\nthree\n")
	r.git(r.repo, "add", "-A")
	r.git(r.repo, "commit", "-q", "-m", "base")
	r.git(r.repo, "push", "-q", "origin", "main")
	r.base = r.git(r.repo, "rev-parse", "HEAD")
	r.git(r.repo, "worktree", "add", "-q", "-b", "nodwyn/task", r.tree, "main")
	o := DefaultOptions(r.tree)
	o.Worktree = &Worktree{Repository: r.repo, Branch: "nodwyn/task", Base: "main", BaseCommit: r.base}
	r.in = scheme.New()
	if err := Register(r.in, o); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *landRig) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *landRig) write(dir, name, text string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *landRig) land(message string) (string, error) {
	v, err := r.in.EvalString(context.Background(), `(git-land `+scheme.Write(scheme.Str(message))+`)`)
	return v.S, err
}

// git-land commits the session's change with the message given, pushes it
// to main's upstream and fast-forwards the person's clean checkout of main:
// the one step "commit and push to main" took 34 messages to ask for in the
// local sessions, done by hand each time (ADR 34, amended 2026-10-05).
func TestGitLandCommitsPushesAndMovesMain(t *testing.T) {
	r := newLandRig(t)
	r.write(r.tree, "app.txt", "one\n2\nthree\n")
	r.write(r.tree, "new.txt", "new\n")
	r.write(r.tree, ".env", "TOKEN=secret-value\n")
	got, err := r.land("Two is a digit\n\nThe list reads better.")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"committed", "Two is a digit", "left out, as files fs-read refuses: .env", "pushed 1 commit(s) to origin main", "fast-forwarded"} {
		if !strings.Contains(got, want) {
			t.Errorf("the answer lacks %q:\n%s", want, got)
		}
	}
	head := r.git(r.tree, "rev-parse", "HEAD")
	if remote := r.git(r.repo, "ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(remote, head) {
		t.Fatalf("the remote's main is %q, want %s", remote, head)
	}
	if local := r.git(r.repo, "rev-parse", "main"); local != head {
		t.Fatalf("the local main is %s, want %s", local, head)
	}
	if b, _ := os.ReadFile(filepath.Join(r.repo, "app.txt")); string(b) != "one\n2\nthree\n" {
		t.Fatalf("the person's checkout was not fast-forwarded: %q", b)
	}
	if files := r.git(r.tree, "show", "--name-only", "--format=", "HEAD"); strings.Contains(files, ".env") || !strings.Contains(files, "new.txt") {
		t.Fatalf("committed: %s", files)
	}
	// Nothing left to land.
	if got, err := r.land(""); err != nil || !strings.Contains(got, "nothing to land") {
		t.Fatalf("again: %q %v", got, err)
	}
}

// When main moved on the remote, the session's commits are rebased onto it;
// a conflict stops with the files named, nothing pushed, and once the model
// resolves it, git-land goes on and lands.
func TestGitLandRebasesAndStopsOnAConflict(t *testing.T) {
	r := newLandRig(t)
	other := filepath.Join(filepath.Dir(r.repo), "other")
	r.git(filepath.Dir(r.repo), "clone", "-q", r.remote, other)
	r.write(other, "app.txt", "one\nTWO\nthree\n")
	r.git(other, "commit", "-qam", "upper case two")
	r.git(other, "push", "-q", "origin", "main")
	r.write(r.tree, "app.txt", "one\n2\nthree\n")
	_, err := r.land("Two is a digit")
	if err == nil || !strings.Contains(err.Error(), "conflict in: app.txt") || !strings.Contains(err.Error(), "nothing was pushed") {
		t.Fatalf("a conflict: %v", err)
	}
	if remote := r.git(r.repo, "ls-remote", "origin", "refs/heads/main"); strings.HasPrefix(remote, r.git(r.tree, "rev-parse", "nodwyn/task")) {
		t.Fatal("pushed through a conflict")
	}
	// Markers left in: it says so and goes on with nothing.
	if _, err := r.land(""); err == nil || !strings.Contains(err.Error(), "still has conflict markers") {
		t.Fatalf("markers left: %v", err)
	}
	r.write(r.tree, "app.txt", "one\nTWO, or 2\nthree\n")
	got, err := r.land("")
	if err != nil || !strings.Contains(got, "pushed") {
		t.Fatalf("after resolving: %q %v", got, err)
	}
	log := r.git(r.repo, "log", "--format=%s", "origin/main")
	if !strings.HasPrefix(log, "Two is a digit\nupper case two\nbase") {
		t.Fatalf("the remote's history:\n%s", log)
	}
}

// What leaves is scanned: a change holding a credential-shaped text is not
// pushed. A session with no worktree of its own has nothing to land with
// it, and changes to commit need a message.
func TestGitLandRefusesWhatItMustNot(t *testing.T) {
	r := newLandRig(t)
	r.write(r.tree, "config.txt", "key = sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789\n")
	if _, err := r.land("Add the key"); err == nil || !strings.Contains(err.Error(), "nothing was pushed") {
		t.Fatalf("a credential: %v", err)
	}
	if remote := r.git(r.repo, "ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(remote, r.base) {
		t.Fatal("the credential was pushed")
	}
	r.write(r.tree, "plain.txt", "x\n")
	if _, err := r.land(""); err == nil || !strings.Contains(err.Error(), "give the commit message") {
		t.Fatalf("no message: %v", err)
	}
	in := scheme.New()
	if err := Register(in, DefaultOptions(r.tree)); err != nil {
		t.Fatal(err)
	}
	if _, ok := in.Lookup("git-land"); ok {
		t.Fatal("git-land is offered to a session with no worktree of its own")
	}
}

// The person's checkout of main is moved only when it is clean.
func TestGitLandLeavesADirtyCheckoutAlone(t *testing.T) {
	r := newLandRig(t)
	r.write(r.repo, "app.txt", "one\ntwo\nthree\nmine\n")
	r.write(r.tree, "new.txt", "new\n")
	got, err := r.land("A new file")
	if err != nil || !strings.Contains(got, "has changes of its own") {
		t.Fatalf("%q %v", got, err)
	}
	if local := r.git(r.repo, "rev-parse", "main"); local != r.base {
		t.Fatal("a dirty checkout's main moved")
	}
}
