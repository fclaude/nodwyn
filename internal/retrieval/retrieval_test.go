package retrieval

import (
	"math"
	"math/rand"
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

// Several indexes rank as one collection: a word common in one and rare
// in the other weighs by its count over both, and the scores of one index
// are those it would have in the whole.
func TestSearchAllRanksSeveralIndexesAsOne(t *testing.T) {
	var a, b, whole Index
	for _, text := range []string{"binding check", "binding table", "binding moved"} {
		a.Add(Field{Text: text})
		whole.Add(Field{Text: text})
	}
	for _, text := range []string{"deploy binary", "restart service"} {
		b.Add(Field{Text: text})
		whole.Add(Field{Text: text})
	}
	got := SearchAll([]*Index{&a, &b}, "binding deploy", 10, nil)
	want := whole.Search("binding deploy", 10, nil)
	if len(got) != len(want) {
		t.Fatalf("%d found across indexes, %d in one", len(got), len(want))
	}
	for i := range got {
		doc := got[i].Doc
		if got[i].Index == 1 {
			doc += a.Len()
		}
		if doc != want[i].Doc || got[i].Score != want[i].Score {
			t.Fatalf("result %d: %+v, in one index %+v", i, got[i], want[i])
		}
	}
	if got[0].Index != 1 || got[0].Doc != 0 {
		t.Fatalf("the rare word's document first: %+v", got[0])
	}
	if only := SearchAll([]*Index{&a, &b}, "binding", 10, func(index, _ int) bool { return index == 0 }); len(only) != 3 {
		t.Fatalf("filtered: %+v", only)
	}
}

// An index kept as bytes ranks as it did, read whole or with only the
// query's words, after documents are added to it again; bytes that are
// not an index are an error, not an empty index.
func TestIndexKeptAsBytesRanksAsItDid(t *testing.T) {
	texts := []string{
		"the frobnicator failed with code 7731",
		"go test ./pager/ failed: TestPagerOrder",
		"sorted the keys in pager.go",
		"nothing to see here, the frobnicator is fine",
		"pager pager pager order",
	}
	var x Index
	for _, text := range texts[:3] {
		x.Add(Field{Text: text})
	}
	whole, err := Decode(x.AppendBinary(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range texts[3:] {
		x.Add(Field{Text: text})
		whole.Add(Field{Text: text})
	}
	const query = "frobnicator pager order 7731"
	want := x.Search(query, 10, nil)
	if got := whole.Search(query, 10, nil); !slices.EqualFunc(got, want, sameHit) {
		t.Fatalf("read whole and added to: %+v, want %+v", got, want)
	}
	words := map[string]bool{}
	for _, w := range Words(query) {
		words[w] = true
	}
	only, err := Decode(x.AppendBinary(nil), func(w string) bool { return words[w] })
	if err != nil {
		t.Fatal(err)
	}
	if got := only.Search(query, 10, nil); !slices.EqualFunc(got, want, sameHit) {
		t.Fatalf("read with the query's words only: %+v, want %+v", got, want)
	}
	if len(only.postings) != len(words) || only.Len() != x.Len() {
		t.Fatalf("read %d words of %d documents, want %d of %d", len(only.postings), only.Len(), len(words), x.Len())
	}
	raw := x.AppendBinary(nil)
	for _, bad := range [][]byte{nil, raw[:len(raw)-1], append(slices.Clone(raw), 0), {0xff, 0xff, 0xff}} {
		if _, err := Decode(bad, nil); err == nil {
			t.Fatalf("Decode(%x) took it", bad)
		}
	}
}

func sameHit(p, q Hit) bool {
	return p.Doc == q.Doc && math.Abs(p.Score-q.Score) < 1e-9 && slices.Equal(p.Matched, q.Matched)
}

// A document's word count and a word's frequency are quantities of its text,
// not sizes of the index, so they may be larger than the index is in bytes:
// 5,000 words of one word are a few bytes of postings. Decode rejected such
// an index, and a session's .mem was rebuilt from its log on every read.
func TestDecodeTakesCountsLargerThanTheIndex(t *testing.T) {
	var x Index
	x.Add(Field{Text: strings.Repeat("alpha ", 5000) + "beta"})
	x.Add(Field{Text: "alpha gamma"})
	raw := x.AppendBinary(nil)
	if len(raw) > 5000 {
		t.Fatalf("the test needs an index smaller than its counts: %d bytes", len(raw))
	}
	got, err := Decode(raw, nil)
	if err != nil {
		t.Fatalf("a valid index: %v", err)
	}
	want, have := x.Search("alpha beta", 10, nil), got.Search("alpha beta", 10, nil)
	if !slices.EqualFunc(have, want, sameHit) || got.total != x.total {
		t.Fatalf("read back %+v (total %d), want %+v (%d)", have, got.total, want, x.total)
	}
}

// A word longer than maxWordBytes is dropped: one 1 MB line of base64 (with no
// slash or plus in it) made one
// 1 MB word, and an index as large as the text.
func TestWordsDropAWordNobodySearchesFor(t *testing.T) {
	blob := strings.Repeat("x", 2*maxWordBytes)
	if got := Words(blob); len(got) != 0 {
		t.Errorf("a long run of letters gave %q", got)
	}
	if got := Words(strings.Repeat("a", maxWordBytes)); len(got) != 1 {
		t.Errorf("a word at the limit: %q", got)
	}
	if got := Words(strings.Repeat("aB", maxWordBytes)); len(got) != 0 {
		t.Errorf("the pieces of a long blob gave %q", got)
	}
	if got := Words(blob + "_tail"); !slices.Equal(got, []string{"tail"}) {
		t.Errorf("the parts of a long name: %q", got)
	}
	r := rand.New(rand.NewSource(1))
	line := make([]byte, 1<<20)
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	for i := range line {
		line[i] = alphabet[r.Intn(len(alphabet))]
	}
	var x Index
	x.Add(Field{Text: string(line)})
	if size := len(x.AppendBinary(nil)); size > len(line)/4 {
		t.Errorf("a 1 MB base64 line made an index of %d bytes", size)
	}
}
