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

func gitRepo(t *testing.T) (string, *scheme.Interp) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(root, "plants.txt"), []byte("tomato\n"), 0o600)
	os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=old\n"), 0o600)
	run("add", ".")
	run("commit", "-q", "-m", "first")
	in := scheme.New()
	if err := Register(in, DefaultOptions(root)); err != nil {
		t.Fatal(err)
	}
	return root, in
}

func TestGitToolsReadChanges(t *testing.T) {
	root, in := gitRepo(t)
	eval := func(src string) string {
		t.Helper()
		v, err := in.EvalString(context.Background(), src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		return v.S
	}
	if got := eval(`(git-diff)`); got != "(no changes)" {
		t.Fatalf("clean diff: %q", got)
	}
	os.WriteFile(filepath.Join(root, "plants.txt"), []byte("tomato\nbasil\n"), 0o600)
	os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=new\n"), 0o600)
	os.WriteFile(filepath.Join(root, "new.txt"), []byte("x\n"), 0o600)
	os.WriteFile(filepath.Join(root, "credentials.js"), []byte("export const x = 1;\n"), 0o600)
	// The status names every file, a credential file too: a name is no
	// secret, and the evidence loop must see what changed. The diff leaves
	// out what fs-read refuses, and keeps code named like a credential file.
	status := eval(`(git-status)`)
	if !strings.Contains(status, " M plants.txt") || !strings.Contains(status, "?? new.txt") || !strings.Contains(status, " M .env") || !strings.Contains(status, "?? credentials.js") {
		t.Fatalf("status:\n%s", status)
	}
	diff := eval(`(git-diff ".")`)
	if !strings.Contains(diff, "+basil") || strings.Contains(diff, "TOKEN") {
		t.Fatalf("diff:\n%s", diff)
	}
	if got := eval(`(git-diff ".env")`); got != "(no changes)" {
		t.Fatalf("a credential file's diff: %q", got)
	}
	if got := eval(`(git-diff "plants.txt" #t)`); got != "(no changes)" {
		t.Fatalf("nothing is staged: %q", got)
	}
	cmd := exec.Command("git", "add", "plants.txt")
	cmd.Dir = root
	cmd.Run()
	if got := eval(`(git-diff "plants.txt" #t)`); !strings.Contains(got, "+basil") {
		t.Fatalf("staged: %q", got)
	}
	for _, src := range []string{`(git-diff "..")`, `(git-diff "plants.txt" "yes")`} {
		if _, err := in.EvalString(context.Background(), src); err == nil {
			t.Errorf("%s accepted", src)
		}
	}
	if info, _ := in.EvalString(context.Background(), `(assoc-ref (primitive-info "git-diff") 'irreversible)`); info.IsTrue() {
		t.Fatal("git-diff is irreversible")
	}
}

// Reading a diff never runs a program the repository or the user configured.
func TestGitDiffRunsNoConfiguredProgram(t *testing.T) {
	root, in := gitRepo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	config := "[diff \"evil\"]\n\ttextconv = touch " + marker + "\n[diff]\n\texternal = touch " + marker + "\n[core]\n\tfsmonitor = touch " + marker + "\n\tpager = touch " + marker + "\n"
	f, _ := os.OpenFile(filepath.Join(root, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(config)
	f.Close()
	os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("*.txt diff=evil\n"), 0o600)
	os.WriteFile(filepath.Join(root, "plants.txt"), []byte("tomato\nbasil\n"), 0o600)
	for _, src := range []string{`(git-diff)`, `(git-status)`} {
		if _, err := in.EvalString(context.Background(), src); err != nil {
			t.Fatalf("%s: %v", src, err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a configured program ran")
	}
}

func TestGitToolsOnlyInAWorkTree(t *testing.T) {
	in := scheme.New()
	if err := Register(in, DefaultOptions(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if _, ok := in.Lookup("git-diff"); ok {
		t.Fatal("git-diff offered outside a git work tree")
	}
}
