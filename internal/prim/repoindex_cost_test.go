package prim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/guard"
)

// A search of a tree nothing changed in used to run the path and denylist
// checks (a link resolved, the protected directories resolved, twice) on
// every file, as 0.5 s for 5,000 files. The checks a file has passed in the
// state it is in are not repeated by the session: the second search costs a
// fraction of the checks alone.
func TestSearchOfAnUnchangedTreeDoesNotCheckEveryFileAgain(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	const files = 1500
	for i := 0; i < files; i++ {
		dir := filepath.Join(root, fmt.Sprintf("pkg%d", i%30))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.go", i)), []byte(fmt.Sprintf("package p\n\nfunc widget%d() {}\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := newIndexHarness(t, root, t.TempDir())
	h.eval(t, `(index-search "widget7" "repo")`)

	o := DefaultOptions(root)
	read, _ := h.in.Lookup("fs-read")
	paths, _ := filepath.Glob(filepath.Join(root, "pkg*", "*.go"))
	began := time.Now()
	for _, p := range paths {
		searchableFile(context.Background(), guard.RepoPin{Root: root}, denylist(o), read, p, o.MaxReadBytes)
	}
	checks := time.Since(began)

	began = time.Now()
	h.eval(t, `(index-search "widget7" "repo")`)
	again := time.Since(began)
	if again > checks/2 {
		t.Fatalf("a search of an unchanged tree took %s, and the checks of its %d files alone take %s", again, len(paths), checks)
	}
}
