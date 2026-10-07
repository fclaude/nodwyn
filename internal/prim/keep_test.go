package prim

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// keepRepo is a git work tree with a commit, and the keeper for it with a
// scratch directory outside it.
func keepRepo(t *testing.T) (root string, k *Keeper, git func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root = t.TempDir()
	git = func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	for name, content := range map[string]string{
		"plants.txt": "tomato\n", "app.go": "one\n", "gone.txt": "x\n", "Makefile": "all:\n",
		".env": "TOKEN=old\n", "sub/deep.txt": "d\n",
	} {
		write(t, root, name, content, 0o644)
	}
	git("add", ".")
	git("commit", "-q", "-m", "first")
	o := DefaultOptions(root)
	o.Scratch = t.TempDir()
	return root, NewKeeper(o), git
}

func write(t *testing.T, root, name, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func absent(root, name string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name)))
	return errors.Is(err, os.ErrNotExist)
}

// A turn stopped past the state its tests passed on gets that state back:
// kept files as they were, files changed since as HEAD has them, files made
// since removed. What it replaces is saved. A credential file and a path the
// guard protects are never touched, in either direction.
func TestKeeperPutsBackTheKeptState(t *testing.T) {
	root, k, _ := keepRepo(t)
	ctx := context.Background()

	// The state a test run passes on: a changed file, a new one, a new
	// executable, a deleted one.
	write(t, root, "plants.txt", "tomato\nbasil\n", 0o644)
	write(t, root, "new.txt", "new\n", 0o644)
	write(t, root, "script.sh", "#!/bin/sh\n", 0o755)
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	n, err := k.Keep(ctx)
	if err != nil || n != 4 {
		t.Fatalf("kept %d files, %v; want 4", n, err)
	}

	// Then the turn goes on and breaks things.
	write(t, root, "plants.txt", "tomato\nbasil\nbroken\n", 0o644)
	os.Remove(filepath.Join(root, "new.txt"))
	write(t, root, "app.go", "two\n", 0o644)
	write(t, root, "late.txt", "late\n", 0o644)
	write(t, root, "sub/late/more.txt", "more\n", 0o644)
	write(t, root, "gone.txt", "back\n", 0o644)
	write(t, root, "script.sh", "#!/bin/sh\nexit 1\n", 0o644)
	write(t, root, "Makefile", "all: broken\n", 0o644)
	write(t, root, ".env", "TOKEN=new\n", 0o644)

	res, err := k.Restore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"plants.txt": "tomato\nbasil\n", "new.txt": "new\n", "app.go": "one\n", "script.sh": "#!/bin/sh\n",
		"Makefile": "all: broken\n", ".env": "TOKEN=new\n", "sub/deep.txt": "d\n",
	} {
		if got := read(t, root, name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"late.txt", "sub/late/more.txt", "sub/late", "gone.txt"} {
		if !absent(root, name) {
			t.Errorf("%s is still there", name)
		}
	}
	if info, err := os.Stat(filepath.Join(root, "script.sh")); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("script.sh lost its executable bit: %v", err)
	}
	if res.Put != 4 || res.Removed != 3 || len(res.Skipped) != 0 {
		t.Errorf("%+v, want 4 written (plants, new, app, script) and 3 removed (late, more, gone)", res)
	}
	// What the restore replaced is saved, in the same relative paths.
	for name, want := range map[string]string{"plants.txt": "tomato\nbasil\nbroken\n", "app.go": "two\n", "late.txt": "late\n", "gone.txt": "back\n"} {
		b, err := os.ReadFile(filepath.Join(res.Discarded, name))
		if err != nil || string(b) != want {
			t.Errorf("discarded %s: %q, %v; want %q", name, b, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(res.Discarded, "Makefile")); err == nil {
		t.Error("a protected file was saved as if it had been replaced")
	}

	// Asked again, it finds nothing to do.
	res, err = k.Restore(ctx)
	if err != nil || res.Put+res.Removed != 0 {
		t.Errorf("a second restore did %+v, %v", res, err)
	}
}

// A credential file and a path the guard protects are not copied into what
// is kept: the scratch directory is the model's to read, and what the
// denylist refuses must not reach it by this way.
func TestKeeperDoesNotCopyCredentialFilesOrProtectedPaths(t *testing.T) {
	root, k, _ := keepRepo(t)
	write(t, root, ".env", "TOKEN=canary-secret-value\n", 0o644)
	write(t, root, "deploy.pem", "canary-secret-value\n", 0o644)
	write(t, root, "Makefile", "all: canary-secret-value\n", 0o644)
	write(t, root, ".nodwyn/init.scm", ";; canary-secret-value\n", 0o644)
	write(t, root, "plants.txt", "tomato\nbasil\n", 0o644)
	n, err := k.Keep(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("kept %d files, %v; want only plants.txt", n, err)
	}
	filepath.WalkDir(k.dir(), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(path); strings.Contains(string(b), "canary-secret-value") {
				t.Errorf("%s holds a file the denylist or the guard keeps out", path)
			}
		}
		return nil
	})
}

// A commit between the keep and the restore changes what unchanged means:
// nothing is put back.
func TestKeeperRefusesAfterACommit(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "plants.txt", "tomato\nbasil\n", 0o644)
	if _, err := k.Keep(context.Background()); err != nil {
		t.Fatal(err)
	}
	git("commit", "-qam", "second")
	write(t, root, "plants.txt", "tomato\nbroken\n", 0o644)
	if _, err := k.Restore(context.Background()); err == nil || !strings.Contains(err.Error(), "nothing was put back") {
		t.Fatalf("restore after a commit: %v", err)
	}
	if got := read(t, root, "plants.txt"); got != "tomato\nbroken\n" {
		t.Fatalf("plants.txt is %q", got)
	}
}

// A directory the model replaced with a link must not take the restore's
// write outside the repository.
func TestKeeperDoesNotWriteThroughALink(t *testing.T) {
	root, k, _ := keepRepo(t)
	write(t, root, "dir/kept.txt", "kept\n", 0o644)
	if _, err := k.Keep(context.Background()); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	res, err := k.Restore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("the restore wrote through the link: %v", entries)
	}
	if len(res.Skipped) == 0 || !strings.Contains(strings.Join(res.Skipped, " "), "dir/kept.txt (outside the repository)") {
		t.Fatalf("skipped: %v", res.Skipped)
	}
}

// With nothing kept there is nothing to put back, and a keep that cannot be
// made says why and leaves the earlier one.
func TestKeeperNothingKeptAndTooLarge(t *testing.T) {
	root, k, _ := keepRepo(t)
	ctx := context.Background()
	if _, err := k.Restore(ctx); !errors.Is(err, ErrNothingKept) {
		t.Fatalf("restore with nothing kept: %v", err)
	}
	write(t, root, "plants.txt", "tomato\nbasil\n", 0o644)
	if _, err := k.Keep(ctx); err != nil {
		t.Fatal(err)
	}
	write(t, root, "big.bin", strings.Repeat("x", keepMaxFile+1), 0o644)
	if _, err := k.Keep(ctx); err == nil || k.Failed() == "" {
		t.Fatalf("a keep over the limit: %v, failed %q", err, k.Failed())
	}
	write(t, root, "plants.txt", "tomato\nbroken\n", 0o644)
	if _, err := k.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "plants.txt"); got != "tomato\nbasil\n" {
		t.Fatalf("the earlier keep was lost: plants.txt is %q", got)
	}
}

// Outside a git work tree there is nothing to keep: the primitive is there
// for the strategy to call and says it could not.
func TestKeepPassingPrimitive(t *testing.T) {
	root, _, _ := keepRepo(t)
	o := DefaultOptions(root)
	o.Scratch = t.TempDir()
	o.Keeper = NewKeeper(o)
	in := scheme.New()
	if err := Register(in, o); err != nil {
		t.Fatal(err)
	}
	write(t, root, "plants.txt", "tomato\nbasil\n", 0o644)
	v, err := in.EvalString(context.Background(), `(keep-passing)`)
	if err != nil || v.Kind != scheme.KInt || v.I != 1 {
		t.Fatalf("(keep-passing) = %v, %v; want 1", scheme.Write(v), err)
	}

	o.Keeper = nil
	in = scheme.New()
	if err := Register(in, o); err != nil {
		t.Fatal(err)
	}
	if v, err := in.EvalString(context.Background(), `(keep-passing)`); err != nil || !scheme.Equal(v, scheme.False) {
		t.Fatalf("(keep-passing) without a keeper = %v, %v; want #f", scheme.Write(v), err)
	}

	plain := DefaultOptions(t.TempDir())
	plain.Scratch = t.TempDir()
	if _, err := NewKeeper(plain).Keep(context.Background()); err == nil || !strings.Contains(err.Error(), "not a git work tree") {
		t.Fatalf("keep outside a git work tree: %v", err)
	}
}
