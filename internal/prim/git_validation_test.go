package prim

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitContentStateTracksContentsNotIndex(t *testing.T) {
	root, in := gitRepo(t)
	state := func() string {
		t.Helper()
		v, err := in.EvalString(context.Background(), `(git-content-state)`)
		if err != nil {
			t.Fatal(err)
		}
		return v.S
	}
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := state()
	write(".env", "TOKEN=changed\n")
	if state() != before {
		t.Fatal("read-denied contents contributed to fingerprint")
	}
	write("new.txt", "first\n")
	first := state()
	write("new.txt", "other\n")
	second := state()
	if first == before || second == first {
		t.Fatal("untracked contents were not fingerprinted")
	}
	cmd := exec.Command("git", "-C", root, "add", "new.txt")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stage: %v %s", err, out)
	}
	if state() != second {
		t.Fatal("staging changed workspace fingerprint")
	}
	if err := os.Chmod(filepath.Join(root, "new.txt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if state() == second {
		t.Fatal("executable-bit change was missed")
	}
	if err := os.Symlink("/outside/never-read", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	linked := state()
	if err := os.Remove(filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/another/never-read", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if state() == linked {
		t.Fatal("link target change was missed")
	}
}

func TestGitContentStateLeavesOversizeAndCancelledReadsUnknown(t *testing.T) {
	root, in := gitRepo(t)
	if _, err := in.EvalString(context.Background(), `(git-content-state)`); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(root, "large.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(257 << 20); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := in.EvalString(context.Background(), `(git-content-state)`); err == nil {
		t.Fatal("oversized workspace was certified")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := in.EvalString(ctx, `(git-content-state)`); err == nil {
		t.Fatal("cancelled read was certified")
	}
}
