package prim

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/retrieval"
)

// Every edit of a file leaves its old chunks in the store, and the sessions
// that only search never called index-update, the one place that collected
// them. A search that read a file now does, at most once an hour.
func TestSearchCollectsChunksNothingNamesAnyMore(t *testing.T) {
	root := indexRepo(t)
	dir := t.TempDir()
	h := newIndexHarness(t, root, dir)
	h.eval(t, `(index-search "binding" "repo")`)
	edit := func(text string) {
		path := filepath.Join(root, "egress.go")
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		later := time.Now().Add(time.Minute)
		if err := os.Chtimes(path, later, later); err != nil {
			t.Fatal(err)
		}
	}
	chunkFiles := func() map[string]bool {
		out := map[string]bool{}
		_ = filepath.WalkDir(filepath.Join(dir, "files"), func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				out[strings.TrimSuffix(d.Name(), ".json")] = true
			}
			return nil
		})
		return out
	}
	for _, text := range []string{"one", "two", "three"} {
		edit("package egress\n\nfunc " + text + "Edit() {}\n")
		h.eval(t, `(index-search "`+text+`Edit" "repo")`)
	}
	live := func() map[string]bool {
		m := retrieval.ParseManifest(mustRead(t, retrieval.Store{Dir: dir}.ManifestPath(root)), root)
		out := map[string]bool{}
		for _, f := range m.Files {
			out[f.Hash] = true
		}
		return out
	}
	if len(chunkFiles()) <= len(live()) {
		t.Fatalf("the test needs orphans: %d chunk files, %d live", len(chunkFiles()), len(live()))
	}
	// A week on, with the last collection long ago.
	old := time.Now().Add(-8 * 24 * time.Hour)
	for hash := range chunkFiles() {
		if err := os.Chtimes(filepath.Join(dir, "files", hash[:2], hash+".json"), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(dir, "collected"), old, old); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	edit("package egress\n\nfunc fourEdit() {}\n")
	h.eval(t, `(index-search "fourEdit" "repo")`)
	files, want := chunkFiles(), live()
	for hash := range files {
		if !want[hash] {
			t.Errorf("chunk %s... is named by no manifest and was not collected", hash[:8])
		}
	}
	for hash := range want {
		if !files[hash] {
			t.Errorf("chunk %s... is named by the manifest and was collected", hash[:8])
		}
	}
}

// A search of a tree nothing changed in does not walk the store again, and
// one an hour has not passed since does not either.
func TestCollectDueWaitsForItsInterval(t *testing.T) {
	dir := t.TempDir()
	s := retrieval.Store{Dir: dir}
	if err := s.Put(retrieval.Hash("x"), nil); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	path := filepath.Join(dir, "files", retrieval.Hash("x")[:2], retrieval.Hash("x")+".json")
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(time.Now().Add(-100 * 24 * time.Hour)); err != nil { // marks a collection, removes nothing
		t.Fatal(err)
	}
	if err := s.CollectDue(time.Now().Add(-7*24*time.Hour), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a collection an hour has not passed since removed a chunk: %v", err)
	}
	marker := filepath.Join(dir, "collected")
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.CollectDue(time.Now().Add(-7*24*time.Hour), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("an old collection left an old unnamed chunk: %v", err)
	}
}
