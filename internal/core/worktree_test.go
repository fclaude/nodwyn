package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/internal/scheme"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// gitRepo is a repository on main with README committed, and the git that
// runs in it; every check of a worktree uses real git.
func gitRepo(t *testing.T) (string, func(dir string, args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	repo := t.TempDir()
	git(repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", "README")
	git(repo, "commit", "-q", "-m", "first")
	return repo, git
}

// worktreeOptions opens a session on repo in a worktree of its own, with
// its log on disk so it can be resumed.
func worktreeOptions(t *testing.T, repo string, turns ...fake.Turn) Options {
	t.Helper()
	srv := fake.NewT(t, turns...)
	t.Cleanup(srv.Close)
	return Options{Repo: repo, Paths: config.PathsUnder(t.TempDir(), t.TempDir()), Config: config.Default(), Provider: llm.NewOllama(srv.URL), Worktree: true}
}

func readLog(t *testing.T, o Options, id string) []log.Event {
	t.Helper()
	evs, err := log.ReadFile(log.SessionPath(o.Paths.SessionsDir, id))
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func countKind(evs []log.Event, kind log.Kind) int {
	n := 0
	for _, ev := range evs {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

// "Run validate first" is said only where the session has a validate tool, as
// in nodwyn's own repository: in any other project the model called a tool
// that does not exist (P2-065).
func TestWorktreePromptNamesValidateOnlyWhereTheToolExists(t *testing.T) {
	t.Parallel()
	repo, _ := gitRepo(t)
	o := worktreeOptions(t, repo)
	o.InitFile = filepath.Join(t.TempDir(), "init.scm")
	if err := os.WriteFile(o.InitFile, []byte(`(register-tool "validate" (lambda () "ok") '((doc . "the repository's check") (params)))`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	section := evalText(t, &rig{t: t, s: s}, "(worktree-section)")
	if !strings.Contains(section, "run validate first, then call git-land") {
		t.Fatalf("worktree section:\n%s", section)
	}
}

// A session asked for a worktree works in one of its own, on its own
// branch: what it writes lands there, never in the repository, whose git
// status stays clean, and the model is told where it is and not to stash.
func TestWorktreeSessionWritesInItsOwnCheckout(t *testing.T) {
	t.Parallel()
	repo, git := gitRepo(t)
	o := worktreeOptions(t, repo,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w1", Name: "fs-write", Args: map[string]any{"path": "new.txt", "content": "mine\n"}}}},
		fake.Turn{Content: "written"},
	)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	w := s.Worktree()
	if w == nil {
		t.Fatal("no worktree")
	}
	suffix := s.ID()[strings.LastIndexByte(s.ID(), '-')+1:]
	if want := filepath.Join(config.CanonicalPath(repo), ".nodwyn", "worktrees", suffix); s.Repo() != want || w.Path != want {
		t.Fatalf("root %s, worktree %s; want %s", s.Repo(), w.Path, want)
	}
	if w.Branch != "nodwyn/"+suffix || w.Base != "main" || w.BaseCommit != git(repo, "rev-parse", "HEAD") || !samePath(w.Repository, repo) {
		t.Fatalf("worktree %+v", w)
	}
	if got := git(w.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != w.Branch {
		t.Fatalf("the worktree is on %s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, ".nodwyn", "worktrees", ".gitignore")); string(b) != "*\n" {
		t.Fatalf(".nodwyn/worktrees/.gitignore: %q", b)
	}
	if err := s.Send("write new.txt"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("y")
	if b, err := os.ReadFile(filepath.Join(w.Path, "new.txt")); err != nil || string(b) != "mine\n" {
		t.Fatalf("new.txt in the worktree: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "new.txt")); err == nil {
		t.Fatal("the write reached the repository's own checkout")
	}
	if st := git(repo, "status", "--porcelain"); st != "" {
		t.Fatalf("the repository's status is not clean:\n%s", st)
	}
	section := evalText(t, &rig{t: t, s: s}, "(worktree-section)")
	if !strings.Contains(section, w.Branch) || !strings.Contains(section, "from main") || !strings.Contains(section, "Never use git stash here") || !strings.Contains(section, "git archive HEAD") ||
		!strings.Contains(section, "to push it to main), call git-land") || strings.Contains(section, "validate") {
		t.Fatalf("worktree section:\n%s", section)
	}
	// A worktree session is offered git-land, to land its work in one step.
	offered := false
	for _, tool := range s.Tools() {
		offered = offered || tool.Name == "git-land"
	}
	if !offered {
		t.Fatal("git-land is not offered in a worktree session")
	}
	s.Close()
	// What was written is not lost: the worktree stays, and its branch.
	if _, err := os.Stat(filepath.Join(w.Path, "new.txt")); err != nil {
		t.Fatal("close removed a worktree with work in it")
	}
	evs := readLog(t, o, s.ID())
	var start log.SessionStart
	if evs[0].Kind != log.KindSessionStart || evs[0].Decode(&start) != nil || start.Worktree == nil || *start.Worktree != *w {
		t.Fatalf("session.start: %s", evs[0].Payload)
	}
	if countKind(evs, log.KindWorktreeCreate) != 1 || countKind(evs, log.KindWorktreeRemove) != 0 {
		t.Fatalf("worktree records: %d created, %d removed", countKind(evs, log.KindWorktreeCreate), countKind(evs, log.KindWorktreeRemove))
	}
	info, err := (Store{Dir: o.Paths.SessionsDir}).Info(s.ID())
	if err != nil || info.Worktree == nil || info.Project() != w.Repository || info.Repo != w.Path {
		t.Fatalf("stored: %+v %v", info, err)
	}
}

// A worktree nothing happened in goes on close, with its branch, and a
// resume makes it again; a worktree with an ignored file (an upload) or a
// commit its base lacks stays.
func TestWorktreeGoesOnCloseOnlyWhenNothingIsLost(t *testing.T) {
	t.Parallel()
	repo, git := gitRepo(t)
	o := worktreeOptions(t, repo)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	w := *s.Worktree()
	s.Close()
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatalf("an untouched worktree was kept: %v", err)
	}
	if branchExists(repo, w.Branch) {
		t.Fatal("its branch was kept")
	}
	if countKind(readLog(t, o, s.ID()), log.KindWorktreeRemove) != 1 {
		t.Fatal("no worktree.remove")
	}

	o.Repo = ""
	resumed, err := Resume(o, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Repo() != w.Path || resumed.Worktree() == nil {
		t.Fatalf("resumed in %s", resumed.Repo())
	}
	if got := git(w.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != w.Branch {
		t.Fatalf("the restored worktree is on %s", got)
	}
	// An upload is ignored by its folder's .gitignore, as attachments are.
	uploads := filepath.Join(w.Path, ".nodwyn", "attachments")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{".gitignore": "*\n", "receipt.pdf": "%PDF"} {
		if err := os.WriteFile(filepath.Join(uploads, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resumed.Close()
	if _, err := os.Stat(filepath.Join(uploads, "receipt.pdf")); err != nil {
		t.Fatal("close removed a worktree holding an upload")
	}
	evs := readLog(t, o, s.ID())
	if countKind(evs, log.KindWorktreeRestore) != 1 || countKind(evs, log.KindWorktreeRemove) != 1 {
		t.Fatalf("worktree records: %d restored, %d removed", countKind(evs, log.KindWorktreeRestore), countKind(evs, log.KindWorktreeRemove))
	}

	if err := os.RemoveAll(uploads); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Path, "done.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(w.Path, "add", "done.txt")
	git(w.Path, "commit", "-q", "-m", "done")
	again, err := Resume(o, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
	if _, err := os.Stat(filepath.Join(w.Path, "done.txt")); err != nil || !branchExists(repo, w.Branch) {
		t.Fatal("close removed a worktree whose branch has a commit main lacks")
	}
	// Once main has it, nothing would be lost.
	git(repo, "merge", "-q", "--ff-only", w.Branch)
	last, err := Resume(o, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	last.Close()
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) || branchExists(repo, w.Branch) {
		t.Fatal("a landed worktree was kept")
	}
}

// nodwyn's own undo is not work left in a worktree: a file written twice
// keeps its first content as a version, and once the branch has landed the
// worktree still goes on close. The version is kept with the repository's,
// outside the worktree, so it outlasts it and a restore still finds it.
func TestWorktreeGoesOnCloseWithItsVersionsKept(t *testing.T) {
	t.Parallel()
	repo, git := gitRepo(t)
	o := worktreeOptions(t, repo,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w1", Name: "fs-write", Args: map[string]any{"path": "new.txt", "content": "one\n"}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w2", Name: "fs-write", Args: map[string]any{"path": "new.txt", "content": "two\n"}}}},
		fake.Turn{Content: "written"},
	)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	w := *s.Worktree()
	if err := s.Send("write new.txt twice"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("y")
	kept, err := prim.ListVersions(prim.VersionsDir(repo), filepath.Join(w.Path, "new.txt"))
	if err != nil || len(kept) != 1 {
		t.Fatalf("versions of new.txt in the repository's store: %+v %v", kept, err)
	}
	if b, err := prim.ReadVersion(prim.VersionsDir(repo), kept[0].Hash); err != nil || string(b) != "one\n" {
		t.Fatalf("the kept version: %q %v", b, err)
	}
	git(w.Path, "add", "new.txt")
	git(w.Path, "commit", "-q", "-m", "new")
	git(repo, "merge", "-q", "--ff-only", w.Branch)
	s.Close()
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) || branchExists(repo, w.Branch) {
		t.Fatalf("a landed worktree was kept for nodwyn's own versions: %v", err)
	}
	if st := git(repo, "status", "--porcelain"); st != "" {
		t.Fatalf("the repository's status is not clean:\n%s", st)
	}
}

// A resume that makes a worktree deleted by hand again clears only that
// worktree's registration: a worktree of the person's whose folder is away
// for now (on a disk not mounted) stays registered, its branch with it.
func TestWorktreeRestoreLeavesOtherWorktreesRegistered(t *testing.T) {
	t.Parallel()
	repo, git := gitRepo(t)
	o := worktreeOptions(t, repo)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	w := *s.Worktree()
	// Work in it keeps it on close; then a person deletes its folder.
	if err := os.WriteFile(filepath.Join(w.Path, "draft.txt"), []byte("draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Close()
	mine := filepath.Join(t.TempDir(), "mine")
	git(repo, "worktree", "add", "-q", "-b", "mine", mine)
	for _, dir := range []string{w.Path, mine} {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	o.Repo = ""
	resumed, err := Resume(o, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if got := git(w.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != w.Branch {
		t.Fatalf("the restored worktree is on %s", got)
	}
	if list := git(repo, "worktree", "list", "--porcelain"); !strings.Contains(list, "worktree "+config.CanonicalPath(mine)+"\n") {
		t.Fatalf("the person's worktree is no longer registered:\n%s", list)
	}
}

// Nothing uncommitted comes along to a worktree, and the session is told how
// many uncommitted files it does not have. Its mounted folders are its
// repository's, from the workspace list (ADR 22): nothing is copied for them.
func TestWorktreeCopiesNothingAndUsesTheRepositorysMounts(t *testing.T) {
	t.Parallel()
	repo, _ := gitRepo(t)
	ref := t.TempDir()
	for path, text := range map[string]string{".env": "TOKEN=x\n", "README": "changed\n"} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	o := worktreeOptions(t, repo)
	if err := os.MkdirAll(o.Paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := config.SaveRegistered(config.RegistryFile(o.Paths), config.Registered{Name: "repo", Path: repo, Mounts: []config.Mount{{Path: ref, Access: "read"}}}); err != nil {
		t.Fatal(err)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	w := *s.Worktree()
	if got := s.Workspace().Mounts; len(got) != 1 || got[0] != (config.Mount{Path: ref, Access: "read"}) {
		t.Fatalf("the worktree's mounts: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(w.Path, ".env")); err == nil {
		t.Fatal(".env was copied")
	}
	if b, _ := os.ReadFile(filepath.Join(w.Path, "README")); string(b) != "hello\n" {
		t.Fatalf("README carried an uncommitted change: %q", b)
	}
	var made log.WorktreeChange
	for _, ev := range readLog(t, o, s.ID()) {
		if ev.Kind == log.KindWorktreeCreate && ev.Decode(&made) != nil {
			t.Fatal(ev.Payload)
		}
	}
	// .env and the change to README are not in the worktree.
	if made.Uncommitted != 2 {
		t.Fatalf("uncommitted: %d", made.Uncommitted)
	}
	s.Close()
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatal("an untouched worktree was kept")
	}
}

// The project is the repository: a worktree's session reads the notes the
// repository's sessions keep, and lists them in its history.
func TestWorktreeSessionSharesTheRepositorysProject(t *testing.T) {
	t.Parallel()
	repo, _ := gitRepo(t)
	srv := fake.NewT(t)
	t.Cleanup(srv.Close)
	o := Options{Repo: repo, Paths: config.PathsUnder(t.TempDir(), t.TempDir()), Config: config.Default(), Provider: llm.NewOllama(srv.URL)}
	plain, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	plain.Close()
	notes := notesFile(o.Paths.DataDir, repo)
	if err := os.MkdirAll(filepath.Dir(notes), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notes, []byte("tests run with make test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.Worktree = true
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := &rig{t: t, s: s}
	if got := evalText(t, r, "(project-notes)"); got != "tests run with make test\n" {
		t.Fatalf("notes: %q", got)
	}
	v, err := s.Eval(t.Context(), "(map (lambda (e) (cdr (assq 'id e))) (session-list))")
	if err != nil {
		t.Fatal(err)
	}
	if got := scheme.Write(v); !strings.Contains(got, plain.ID()) || !strings.Contains(got, s.ID()) {
		t.Fatalf("session-list: %s", got)
	}
}

// Only a whole repository with a commit gets a worktree, made only inside
// it; a refused session leaves no log, branch or folder behind.
func TestWorktreeRefusals(t *testing.T) {
	t.Parallel()
	repo, git := gitRepo(t)
	sub := filepath.Join(repo, "docs")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	git(empty, "init", "-q")
	linked, _ := gitRepo(t)
	if err := os.Mkdir(filepath.Join(linked, ".nodwyn"), 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(linked, ".nodwyn", "worktrees")); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ dir, says string }{
		"not git":   {t.TempDir(), "is not a git repository"},
		"subfolder": {sub, "is a folder inside the repository"},
		"no commit": {empty, "has no commit yet"},
		"a link":    {linked, "is not a folder"},
	} {
		o := worktreeOptions(t, c.dir)
		_, err := New(o)
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
		if logs, _ := os.ReadDir(o.Paths.SessionsDir); len(logs) != 0 {
			t.Errorf("%s: a log was left", name)
		}
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatal("a worktree was made through the link")
	}
	if branches := git(repo, "branch", "--list", "nodwyn/*"); branches != "" {
		t.Fatalf("branches left: %s", branches)
	}
}

// A worktree made again on a new branch (close removed it: nothing would be
// lost) starts from where main is now, and that commit is its base: Review
// compares with it and the listing says it. With the first base kept, every
// commit main gained since showed in Review as the task's own change, which
// a scheduled task, resumed from a removed worktree each run, met every day
// (ADR 49 §3). A worktree kept with its branch keeps its base.
func TestWorktreeMadeAgainHasTheBaseItStartsFrom(t *testing.T) {
	t.Parallel()
	repo, git := gitRepo(t)
	o := worktreeOptions(t, repo)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	w := *s.Worktree()
	id := s.ID()
	s.Close()
	if err := os.WriteFile(filepath.Join(repo, "landed.txt"), []byte("from main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", "landed.txt")
	git(repo, "commit", "-q", "-m", "main moves on")
	head := git(repo, "rev-parse", "HEAD")
	if head == w.BaseCommit {
		t.Fatal("main did not move")
	}

	o.Repo = ""
	resumed, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := resumed.Worktree().BaseCommit; got != head {
		t.Fatalf("the worktree made again from main's %s has base %s", head, got)
	}
	changes, _, err := prim.ChangedSince(t.Context(), prim.DefaultOptions(w.Path), resumed.Worktree().BaseCommit, 1<<20, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("Review shows main's commit as the task's change: %+v", changes)
	}
	// Kept: a commit of its own keeps the worktree and its branch, so the
	// next resume goes on with them and the base stays.
	if err := os.WriteFile(filepath.Join(w.Path, "mine.txt"), []byte("the task's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(w.Path, "add", "mine.txt")
	git(w.Path, "commit", "-q", "-m", "the task's work")
	resumed.Close()
	info, err := Store{Dir: o.Paths.SessionsDir}.Info(id)
	if err != nil {
		t.Fatal(err)
	}
	if info.Worktree == nil || info.Worktree.BaseCommit != head {
		t.Fatalf("the stored task's worktree: %+v", info.Worktree)
	}
	again, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got := again.Worktree().BaseCommit; got != head {
		t.Fatalf("a kept worktree's base moved to %s", got)
	}
	var restores []string
	for _, ev := range readLog(t, o, id) {
		if ev.Kind == log.KindWorktreeRestore {
			var c log.WorktreeChange
			if err := ev.Decode(&c); err != nil {
				t.Fatal(err)
			}
			restores = append(restores, c.BaseCommit)
		}
	}
	if len(restores) != 1 || restores[0] != head {
		t.Fatalf("worktree.restore records %v, want one with base %s", restores, head)
	}
}
