package retrieval

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWordsSplitNamesAsCodeWritesThem(t *testing.T) {
	for text, want := range map[string][]string{
		"ScrubEnv":                 {"scrubenv", "scrub", "env"},
		"index-grep":               {"index-grep", "index", "grep"},
		"max_result_tokens":        {"max_result_tokens", "max", "result", "tokens"},
		"HTTPServer":               {"httpserver", "http", "server"},
		"parseHTTPHeader2":         {"parsehttpheader2", "parse", "http", "header"},
		"--force a, B; 812":        {"force", "812"},
		"élan Ñandú":               {"élan", "ñandú"},
		"go test ./internal/core/": {"go", "test", "internal", "core"},
	} {
		if got := Words(text); !slices.Equal(got, want) {
			t.Errorf("Words(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestSearchRanksRareWordsAndWeightedFields(t *testing.T) {
	var x Index
	plain := x.Add(Field{Text: "shell-exec go build"}, Field{Text: "test ok"})
	rare := x.Add(Field{Text: "the binding check refused the key"})
	called := x.Add(Field{Text: "shell-exec go test", Weight: 3}, Field{Text: "build ok"})
	x.Add(Field{Text: "nothing of interest here"})
	if got := x.Search("binding test", 10, nil); len(got) != 3 || got[0].Doc != rare {
		t.Fatalf("a word one document holds outranks one several hold: %+v", got)
	}
	got := x.Search("test", 10, nil)
	if len(got) != 2 || got[0].Doc != called || got[1].Doc != plain {
		t.Fatalf("a word in a weighted field counts more: %+v", got)
	}
	if !slices.Equal(got[0].Matched, []string{"test"}) {
		t.Fatalf("matched words: %q", got[0].Matched)
	}
	if got := x.Search("binding binding", 10, nil); len(got) != 1 || len(got[0].Matched) != 1 {
		t.Fatalf("a repeated query word counts once: %+v", got)
	}
	if got := x.Search("test", 1, nil); len(got) != 1 {
		t.Fatalf("limit: %+v", got)
	}
	if got := x.Search("absent", 10, nil); len(got) != 0 {
		t.Fatalf("no match: %+v", got)
	}
	var empty Index
	if got := empty.Search("test", 10, nil); got != nil {
		t.Fatalf("empty index: %+v", got)
	}
}

func TestSearchBreaksTiesInTheOrderAdded(t *testing.T) {
	var x Index
	for range 3 {
		x.Add(Field{Text: "same words"})
	}
	got := x.Search("same", 10, nil)
	if len(got) != 3 || got[0].Doc != 0 || got[1].Doc != 1 || got[2].Doc != 2 {
		t.Fatalf("ties: %+v", got)
	}
}

func TestSearchKeepsWhatTheFilterAllows(t *testing.T) {
	var x Index
	x.Add(Field{Text: "binding one"})
	x.Add(Field{Text: "binding two"})
	x.Add(Field{Text: "unrelated"})
	got := x.Search("binding", 10, func(doc int) bool { return doc == 1 })
	if len(got) != 1 || got[0].Doc != 1 {
		t.Fatalf("filtered: %+v", got)
	}
}

// A chunk ends at a line beginning a declaration or a heading once it has
// 20 lines, and at 80 whatever follows; lines count as fs-read counts them,
// and a chunk's first line weighs three times.
func TestChunksCutAtDeclarationsAndHeadings(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n")
	for range 10 {
		b.WriteString("// ten lines\n")
	}
	b.WriteString("func tooSoon() {}\n") // line 12: the chunk has only 11
	for range 10 {
		b.WriteString("x := 1\n")
	}
	b.WriteString("func later() {}\n") // line 23: the chunk has 22
	for range 100 {
		b.WriteString("y := 2\n")
	}
	b.WriteString("# heading without a newline")
	got := Chunks(b.String())
	var bounds [][2]int
	for _, c := range got {
		bounds = append(bounds, [2]int{c.Start, c.End})
	}
	// 1-22, then from 23 at most 80 lines (23-102), then 103-124: a
	// heading at 124 is cut only once a chunk has 20 lines, and this one
	// has 21 when it comes.
	want := [][2]int{{1, 22}, {23, 102}, {103, 123}, {124, 124}}
	if !slices.Equal(bounds, want) {
		t.Fatalf("chunks %v, want %v", bounds, want)
	}
	if got[1].Words["later"] != 3 || got[0].Words["toosoon"] != 1 {
		t.Fatalf("a first line counts three times, another once: %v %v", got[1].Words["later"], got[0].Words["toosoon"])
	}
	if Chunks("") != nil || len(Chunks("one line\n")) != 1 {
		t.Fatal("empty text, one line")
	}
}

func TestStoreKeepsChunksByTextAndAManifestPerTree(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	hash := Hash("some text")
	if s.Has(hash) || Hash("some text") != hash || Hash("other text") == hash {
		t.Fatal("a hash is the text's")
	}
	chunks := Chunks("func a() {}\n")
	if err := s.Put(hash, chunks); err != nil {
		t.Fatal(err)
	}
	if back, ok := s.Chunks(hash); !ok || !s.Has(hash) || len(back) != 1 || back[0].Words["a"] != 0 || back[0].Words["func"] != 3 {
		t.Fatalf("chunks back: %+v %v", back, ok)
	}
	root := t.TempDir()
	m := ParseManifest(nil, root)
	m.Files["a.go"] = FileState{Size: 1, Hash: hash}
	raw, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if back := ParseManifest(raw, root); back.Files["a.go"].Hash != hash {
		t.Fatalf("a manifest back: %+v", back)
	}
	if other := ParseManifest(raw, root+"x"); len(other.Files) != 0 {
		t.Fatal("another tree's manifest is read as this one's")
	}
	if s.ManifestPath(root) == s.ManifestPath(root+"x") {
		t.Fatal("two trees share a manifest path")
	}
	// Collect keeps chunks a manifest names and new ones, removes old
	// unnamed ones and the manifest of a tree that is gone.
	write := func(path string, raw []byte) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(s.ManifestPath(root), raw)
	gone := filepath.Join(t.TempDir(), "gone")
	goneRaw, _ := Manifest{Version: ChunkVersion, Root: gone, Files: map[string]FileState{}}.Encode()
	write(s.ManifestPath(gone), goneRaw)
	old, young := Hash("old"), Hash("young")
	for _, h := range []string{old, young} {
		if err := s.Put(h, nil); err != nil {
			t.Fatal(err)
		}
	}
	long := time.Now().Add(-30 * 24 * time.Hour)
	for _, h := range []string{old, hash} {
		if err := os.Chtimes(s.chunkPath(h), long, long); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Collect(time.Now().Add(-7 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !s.Has(hash) || !s.Has(young) || s.Has(old) {
		t.Fatalf("named %v, young %v, old %v", s.Has(hash), s.Has(young), s.Has(old))
	}
	if _, err := os.Stat(s.ManifestPath(gone)); !os.IsNotExist(err) {
		t.Fatal("a gone tree's manifest stays")
	}
}
