package prim

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/guard"
	nlog "github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/retrieval"
)

const indexCanary = "sk-ant-abcdefgh1234"

// indexRepo is a git repository with a file of each kind the index treats
// apart: tracked, untracked, ignored, denied, nodwyn's own, and one with a
// credential.
func indexRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	for name, text := range map[string]string{
		"egress.go":                   "package egress\n\n// checkBinding refuses a credential whose binding does not name the host.\nfunc checkBinding() {}\n",
		"docs/deploy.md":              "# Deploying\n\nCopy the binary and restart the service; the binding table moves with it.\n",
		"key.txt":                     "the deploy key " + indexCanary + " binding\n",
		".gitignore":                  "vendor/\n",
		".env":                        "TOKEN=binding-value\n",
		"vendor/lib/lib.go":           "package lib\n\nfunc vendoredBinding() {}\n",
		"helper.go":                   "package egress\n\nfunc untrackedHelper() {}\n",
		".nodwyn/worktrees/a/copy.go": "package egress\n\nfunc copiedBinding() {}\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "egress.go", "docs", "key.txt", ".gitignore", ".env"}, {"commit", "-q", "-m", "first"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

type indexHarness struct {
	in      *scheme.Interp
	root    string
	dir     string
	updates []nlog.IndexUpdate
	log     *bytes.Buffer
	prompts int
}

// newIndexHarness registers the primitives over root with the index in dir,
// behind the guard with checkers added.
func newIndexHarness(t *testing.T, root, dir string, checkers ...guard.Checker) *indexHarness {
	t.Helper()
	h := &indexHarness{in: scheme.New(), root: root, dir: dir, log: &bytes.Buffer{}}
	o := DefaultOptions(root)
	o.IndexDir = dir
	o.IndexRecord = func(u nlog.IndexUpdate) { h.updates = append(h.updates, u) }
	if err := Register(h.in, o); err != nil {
		t.Fatal(err)
	}
	asked := guard.ConfirmFunc(func(context.Context, guard.Prompt) (bool, error) { h.prompts++; return true, nil })
	h.in.SetGate(guard.New(asked, recorder{nlog.New(h.log, "test")}, append(Checkers(o, nil, nil), checkers...)...))
	return h
}

func (h *indexHarness) eval(t *testing.T, src string) string {
	t.Helper()
	v, err := h.in.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginModel), src)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return v.S
}

// The index holds the files git lists, tracked or not, and not those git
// ignores, the denylist refuses or nodwyn keeps under .nodwyn/; it stores
// no credential; each file read is a guarded fs-read; a search shows the
// file read again, and records what its build did.
func TestRepoIndexSearchesWhatGitLists(t *testing.T) {
	root := indexRepo(t)
	h := newIndexHarness(t, root, t.TempDir())
	got := h.eval(t, `(index-search "credential binding host" "repo")`)
	if !strings.HasPrefix(got, "[the ") || !strings.Contains(got, "\negress.go:1-4: package egress\n  3: // checkBinding refuses a credential whose binding does not name the host.\n") {
		t.Fatalf("the search: %q", got)
	}
	for _, absent := range []string{".env", "vendor/", ".nodwyn/", "copiedBinding", "vendoredBinding"} {
		if strings.Contains(got, absent) {
			t.Errorf("found %s: %q", absent, got)
		}
	}
	if !strings.Contains(h.eval(t, `(index-search "untrackedHelper" "repo")`), "helper.go:1-3") {
		t.Fatal("an untracked file git does not ignore is indexed")
	}
	if len(h.updates) != 1 || h.updates[0].Read != 5 || h.updates[0].Files != 5 || h.updates[0].Root != root {
		t.Fatalf("one build of five files recorded: %+v", h.updates)
	}
	if n := strings.Count(h.log.String(), `"prim":"fs-read"`); n < 5 {
		t.Fatalf("%d guarded reads logged for five files", n)
	}
	err := filepath.WalkDir(h.dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if raw, _ := os.ReadFile(path); bytes.Contains(raw, []byte(indexCanary)) || bytes.Contains(raw, []byte("abcdefgh1234")) {
				t.Errorf("%s holds the credential", path)
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.eval(t, `(index-search "binding" "repo" 8 "docs")`); !strings.Contains(got, "docs/deploy.md:1-3") || strings.Contains(got, "egress.go") {
		t.Fatalf("a search of one directory: %q", got)
	}
	if v, err := h.in.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginModel), `(index-search "binding" "repo")`); err != nil || !v.Taint.Has(scheme.TagFile) {
		t.Fatalf("a repository search's provenance: %v, %v", v.Taint, err)
	}
	if got := h.eval(t, `(index-search "zzqqxx" "repo")`); !strings.HasPrefix(got, "No chunk of the 5 files indexed") {
		t.Fatalf("no match: %q", got)
	}
}

// A search reads again only what changed, and drops what is gone.
func TestRepoIndexFollowsChanges(t *testing.T) {
	root := indexRepo(t)
	h := newIndexHarness(t, root, t.TempDir())
	h.eval(t, `(index-search "binding" "repo")`)
	write := func(name, text string) {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		later := time.Now().Add(time.Minute)
		if err := os.Chtimes(path, later, later); err != nil {
			t.Fatal(err)
		}
	}
	write("egress.go", "package egress\n\nfunc admitTraveller() {}\n")
	if err := os.Remove(filepath.Join(root, "helper.go")); err != nil {
		t.Fatal(err)
	}
	got := h.eval(t, `(index-search "admitTraveller" "repo")`)
	if !strings.Contains(got, "egress.go:1-3") {
		t.Fatalf("the changed file: %q", got)
	}
	if last := h.updates[len(h.updates)-1]; len(h.updates) != 2 || last.Read != 1 || last.Removed != 1 || last.Files != 4 {
		t.Fatalf("the refresh read one file and removed one: %+v", h.updates)
	}
	h.eval(t, `(index-search "binding" "repo")`)
	if len(h.updates) != 2 {
		t.Fatalf("a search with nothing changed records nothing: %+v", h.updates)
	}
}

// Two working trees of the same text share what is stored: the second
// reads its files (each through its own guard) and stores nothing new.
func TestRepoIndexIsSharedByTrees(t *testing.T) {
	dir := t.TempDir()
	first := indexRepo(t)
	newIndexHarness(t, first, dir).eval(t, `(index-update)`)
	count := func() int {
		n := 0
		_ = filepath.WalkDir(filepath.Join(dir, "files"), func(_ string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				n++
			}
			return nil
		})
		return n
	}
	stored := count()
	second := indexRepo(t)
	h := newIndexHarness(t, second, dir)
	h.eval(t, `(index-update)`)
	if count() != stored || stored == 0 {
		t.Fatalf("the second tree stored %d files' chunks beside the first's %d", count()-stored, stored)
	}
	if len(h.updates) != 1 || h.updates[0].Read != 5 {
		t.Fatalf("the second tree read its own files: %+v", h.updates)
	}
	if trees, _ := filepath.Glob(filepath.Join(dir, "trees", "*.json")); len(trees) != 2 {
		t.Fatalf("a manifest per tree: %v", trees)
	}
	// A tree that is gone takes its manifest with it at the next update.
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	h.eval(t, `(index-update)`)
	if trees, _ := filepath.Glob(filepath.Join(dir, "trees", "*.json")); len(trees) != 1 {
		t.Fatalf("the gone tree's manifest stays: %v", trees)
	}
}

// index-update with a directory git ignores indexes it whole from then
// on, and says what the index holds.
func TestIndexUpdateAddsAnIgnoredDirectory(t *testing.T) {
	root := indexRepo(t)
	h := newIndexHarness(t, root, t.TempDir())
	got := h.eval(t, `(index-update "vendor")`)
	if !strings.HasPrefix(got, "The index holds 6 files in ") || !strings.Contains(got, "Indexed whole besides what git lists: vendor.") {
		t.Fatalf("index-update: %q", got)
	}
	if !strings.Contains(h.eval(t, `(index-search "vendoredBinding" "repo")`), "vendor/lib/lib.go:1-3") {
		t.Fatal("the ignored directory is not searched")
	}
	m := retrieval.ParseManifest(mustRead(t, retrieval.Store{Dir: h.dir}.ManifestPath(root)), root)
	if len(m.Extra) != 1 || m.Extra[0] != "vendor" {
		t.Fatalf("the manifest's extra directories: %v", m.Extra)
	}
	if _, err := h.in.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginModel), `(index-update "../elsewhere")`); err == nil {
		t.Fatal("a path outside the repository is indexed")
	}
}

// pathChecker gives verdict to fs-read of one file.
type pathChecker struct {
	file    string
	verdict guard.Verdict
}

func (c pathChecker) Name() string { return "only-" + c.file }
func (c pathChecker) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) guard.Decision {
	if p.Name == "fs-read" && len(args) > 0 && strings.HasSuffix(args[0].S, "/"+c.file) {
		return guard.Decision{Verdict: c.verdict, Reason: "a checker of " + c.file}
	}
	return guard.Decision{Verdict: guard.Allow}
}

// A file the guard would ask about is left out without asking, and named;
// one indexed before that the guard now refuses is not shown, the refusal
// is the answer, and the file leaves the index.
func TestRepoIndexLeavesOutWhatTheGuardRefuses(t *testing.T) {
	root := indexRepo(t)
	dir := t.TempDir()
	h := newIndexHarness(t, root, dir, pathChecker{"key.txt", guard.Confirm})
	got := h.eval(t, `(index-update)`)
	if !strings.Contains(got, "The guard refused 1, left out: key.txt.") || h.prompts != 0 {
		t.Fatalf("a confirm during a build: %q, %d asked", got, h.prompts)
	}
	if len(h.updates) != 1 || len(h.updates[0].Refused) != 1 {
		t.Fatalf("the refusal recorded: %+v", h.updates)
	}

	open := newIndexHarness(t, root, dir)
	if got := open.eval(t, `(index-search "deploy key" "repo")`); !strings.Contains(got, "key.txt:1-1") {
		t.Fatalf("an index built without the checker: %q", got)
	}
	strict := newIndexHarness(t, root, dir, pathChecker{"key.txt", guard.Deny})
	if _, err := strict.in.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginModel), `(index-search "deploy key" "repo")`); err == nil || !strings.Contains(err.Error(), "a checker of key.txt") {
		t.Fatalf("the refusal is the answer: %v", err)
	}
	m := retrieval.ParseManifest(mustRead(t, retrieval.Store{Dir: dir}.ManifestPath(root)), root)
	if _, ok := m.Files["key.txt"]; ok {
		t.Fatal("a refused file stays in the index")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A file nobody can read (a root-owned build artefact, a mode-000 lock) is
// skipped, as a binary one is: it used to fail every index-search,
// index-update and index-grep of the repository.
func TestAnUnreadableFileIsSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of any mode")
	}
	root := indexRepo(t)
	locked := filepath.Join(root, "build.lock")
	if err := os.WriteFile(locked, []byte("locked\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	h := newIndexHarness(t, root, t.TempDir())
	if got := h.eval(t, `(index-search "deploying binary" "repo")`); !strings.Contains(got, "docs/deploy.md") {
		t.Fatalf("index-search with an unreadable file: %q", got)
	}
	v, err := h.in.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginModel), `(index-grep "." "untrackedHelper")`)
	if err != nil || !strings.Contains(scheme.Write(v), "helper.go") {
		t.Fatalf("index-grep with an unreadable file: %s %v", scheme.Write(v), err)
	}
}
