package prim

import (
	"context"
	"encoding/json"
	"fmt"
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

// A review lists what a worktree changed since its base commit, whether the
// change is committed, staged, unstaged or untracked, and it leaves out what
// the denylist keeps from the model.
func TestChangedFilesSinceBase(t *testing.T) {
	root, _ := gitRepo(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := git("rev-parse", "HEAD")
	write("plants.txt", "tomato\nbasil\n")
	git("commit", "-q", "-am", "second")
	write("new.txt", "fresh\n")
	git("add", "new.txt")
	write("loose.txt", "untracked\n")
	write(".env", "TOKEN=new\n")
	write("big.txt", strings.Repeat("x", 100))
	changes, _, err := ChangedSince(context.Background(), DefaultOptions(root), base, 50, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Change{}
	for _, c := range changes {
		got[c.Path] = c
	}
	if _, ok := got[".env"]; ok || len(got) != 4 {
		t.Fatalf("changes: %+v", changes)
	}
	if c := got["plants.txt"]; c.Status != "M" || !strings.Contains(c.Diff, "+basil") {
		t.Fatalf("plants.txt: %+v", c)
	}
	if c := got["new.txt"]; c.Status != "A" || !strings.Contains(c.Diff, "+fresh") {
		t.Fatalf("new.txt: %+v", c)
	}
	if c := got["loose.txt"]; c.Status != "A" || !strings.Contains(c.Diff, "+untracked") {
		t.Fatalf("loose.txt: %+v", c)
	}
	if c := got["big.txt"]; !strings.Contains(c.Diff, "too large") {
		t.Fatalf("big.txt: %+v", c)
	}
	git("rm", "-q", "plants.txt")
	changes, _, err = ChangedSince(context.Background(), DefaultOptions(root), base, 50, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c.Path == "plants.txt" && (c.Status != "D" || !strings.Contains(c.Diff, "-tomato")) {
			t.Fatalf("deleted plants.txt: %+v", c)
		}
	}
	if _, _, err := ChangedSince(context.Background(), DefaultOptions(root), "--output=x", 50, 1<<20); err == nil {
		t.Fatal("a base that looks like an option was accepted")
	}
}

// reviewRepo is a repository with one commit, and helpers that run git in it
// and write files, each failing the test on an error.
func reviewRepo(t *testing.T) (root, base string, git func(args ...string) string, write func(name, content string)) {
	t.Helper()
	root, _ = gitRepo(t)
	git = func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write = func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root, git("rev-parse", "HEAD"), git, write
}

// A review of a long branch holds only as many bytes as it is given: the
// files after that are counted, not listed, and the call still answers
// rather than growing with the branch.
func TestChangedFilesSinceBaseStopsAtTheTotalLimit(t *testing.T) {
	root, base, _, write := reviewRepo(t)
	for i := 0; i < 20; i++ {
		write(fmt.Sprintf("f%02d.txt", i), strings.Repeat("line\n", 100))
	}
	changes, omitted, err := ChangedSince(context.Background(), DefaultOptions(root), base, 1<<20, 2000)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(changes)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) == 0 || omitted == 0 || len(changes)+omitted != 20 || len(encoded) > 2000 {
		t.Fatalf("%d listed (%d bytes as JSON) and %d omitted, with a limit of 2000 bytes for 20 files", len(changes), len(encoded), omitted)
	}
}

// One file git cannot read does not fail the review of the others: a
// submodule the base had, now gone from the tree, is a note of its own.
func TestChangedFilesSinceBaseNotesAFileItCannotRead(t *testing.T) {
	root, _, git, write := reviewRepo(t)
	git("update-index", "--add", "--cacheinfo", "160000,1111111111111111111111111111111111111111,sub")
	git("commit", "-q", "-m", "a submodule")
	base := git("rev-parse", "HEAD")
	git("rm", "-q", "--cached", "sub")
	write("after.txt", "still shown\n")
	changes, _, err := ChangedSince(context.Background(), DefaultOptions(root), base, 1<<20, 1<<20)
	if err != nil {
		t.Fatalf("one unreadable file failed the whole review: %v", err)
	}
	got := map[string]Change{}
	for _, c := range changes {
		got[c.Path] = c
	}
	if c := got["sub"]; c.Status != "D" || c.Diff != noteUnreadable {
		t.Fatalf("sub: %+v", c)
	}
	if c := got["after.txt"]; !strings.Contains(c.Diff, "+still shown") {
		t.Fatalf("after.txt: %+v", c)
	}
}

// A change with no lines to show says what it is instead of drawing a card
// with nothing in it: a link, an empty file, a change of mode alone.
func TestChangedFilesSinceBaseSaysWhatHasNoLines(t *testing.T) {
	root, _, git, write := reviewRepo(t)
	write("run.sh", "echo hi\n")
	git("add", "run.sh")
	git("commit", "-q", "-m", "a script")
	base := git("rev-parse", "HEAD")
	if err := os.Chmod(filepath.Join(root, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("empty.txt", "")
	write("bin.dat", "a\x00b")
	if err := os.Symlink("/etc/hostname", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	changes, _, err := ChangedSince(context.Background(), DefaultOptions(root), base, 1<<20, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range changes {
		got[c.Path] = c.Diff
	}
	want := map[string]string{"empty.txt": noteNoLines, "link": noteNotRegular, "bin.dat": noteBinary}
	for name, note := range want {
		if got[name] != note {
			t.Errorf("%s: %q, want %q", name, got[name], note)
		}
	}
	// A checkout that ignores file modes reports no mode change to note.
	if git("config", "--get", "core.filemode") == "true" && got["run.sh"] != noteNoLines {
		t.Errorf("run.sh: %q, want %q", got["run.sh"], noteNoLines)
	}
}

// A cancelled review stops reading and says so, rather than returning the
// files it had read as if they were all.
func TestChangedFilesSinceBaseStopsWhenCancelled(t *testing.T) {
	root, base, _, write := reviewRepo(t)
	write("a.txt", "one\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := ChangedSince(ctx, DefaultOptions(root), base, 1<<20, 1<<20); err == nil {
		t.Fatal("a cancelled review returned without an error")
	}
}
