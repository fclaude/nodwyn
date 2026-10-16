package retrieval

import (
	"cmp"
	"encoding/binary"
	"errors"
	"maps"
	"math"
	"slices"
)

// Field is text added to a document, each of its words counted Weight
// times: the call a tool result answers counts more than its output.
type Field struct {
	Text   string
	Weight int
}

// Index ranks the documents added to it by BM25 over their words. What it
// indexes is derived, and can be indexed again (SPEC, invariant 1): it is
// built in memory, and kept, if at all, in a derived file of the caller's
// (AppendBinary). The zero Index is empty.
type Index struct {
	postings map[string][]posting
	lengths  []int
	total    int
}

// posting is one document's count of a word.
type posting struct{ doc, count int32 }

// Hit is a document a search found: its number (the order it was added
// in, from 0), its score, and the query's words it holds.
type Hit struct {
	Doc     int
	Score   float64
	Matched []string
}

// BM25's usual constants: how soon a word's count stops adding, and how
// much a long document's counts are discounted.
const (
	k1 = 1.2
	b  = 0.75
)

// Add adds a document made of fields and returns its number. A field's
// Weight below 1 counts once.
func (x *Index) Add(fields ...Field) int {
	counts := map[string]int{}
	for _, f := range fields {
		Count(counts, f.Text, f.Weight)
	}
	return x.AddCounts(counts)
}

// Count adds text's words to counts, each weight times (once below 1).
func Count(counts map[string]int, text string, weight int) {
	for _, word := range Words(text) {
		counts[word] += max(weight, 1)
	}
}

// AddCounts adds a document whose words are already counted, as a
// repository's chunks are stored, and returns its number.
func (x *Index) AddCounts(counts map[string]int) int {
	if x.postings == nil {
		x.postings = map[string][]posting{}
	}
	n := 0
	doc := int32(len(x.lengths))
	for word, c := range counts {
		x.postings[word] = append(x.postings[word], posting{doc, int32(c)})
		n += c
	}
	x.lengths = append(x.lengths, n)
	x.total += n
	return int(doc)
}

// Len is the number of documents added.
func (x *Index) Len() int { return len(x.lengths) }

// AppendBinary appends the index to b as Decode reads it: the length of
// each document, then each word, in order, with its postings. A log's
// words are kept so beside it (core's .mem, ADR 51), so that a search
// reads the postings of its own words and skips the others'.
func (x *Index) AppendBinary(b []byte) []byte {
	b = binary.AppendUvarint(b, uint64(len(x.lengths)))
	for _, n := range x.lengths {
		b = binary.AppendUvarint(b, uint64(n))
	}
	b = binary.AppendUvarint(b, uint64(len(x.postings)))
	var p []byte
	for _, word := range slices.Sorted(maps.Keys(x.postings)) {
		// Postings are added in document order, so each is kept as the
		// distance from the one before.
		p = p[:0]
		prev := int32(0)
		for _, q := range x.postings[word] {
			p = binary.AppendUvarint(p, uint64(q.doc-prev))
			p = binary.AppendUvarint(p, uint64(q.count))
			prev = q.doc
		}
		b = binary.AppendUvarint(b, uint64(len(word)))
		b = append(b, word...)
		b = binary.AppendUvarint(b, uint64(len(x.postings[word])))
		b = binary.AppendUvarint(b, uint64(len(p)))
		b = append(b, p...)
	}
	return b
}

var errMalformed = errors.New("retrieval: a malformed index")

// Decode reads an index AppendBinary wrote. keep, when not nil, says which
// words' postings to read: a search needs only its own, and the documents'
// lengths weigh them as in the whole index.
func Decode(data []byte, keep func(word string) bool) (*Index, error) {
	at := 0
	// A structural size (a count of documents, words or postings, a length
	// in bytes) cannot exceed the bytes that hold it, which bounds what is
	// allocated for it. A document's word count and a word's frequency are
	// quantities of the text, and are not bounded by the index's size: 40,000
	// words of test output are a few KB of index.
	read := func(limit uint64) (int, error) {
		v, n := binary.Uvarint(data[at:])
		if n <= 0 || v > limit {
			return 0, errMalformed
		}
		at += n
		return int(v), nil
	}
	next := func() (int, error) { return read(uint64(len(data))) }
	quantity := func() (int, error) { return read(math.MaxInt32) }
	docs, err := next()
	if err != nil {
		return nil, err
	}
	x := &Index{postings: map[string][]posting{}, lengths: make([]int, 0, docs)}
	for range docs {
		n, err := quantity()
		if err != nil {
			return nil, err
		}
		x.lengths = append(x.lengths, n)
		x.total += n
	}
	words, err := next()
	if err != nil {
		return nil, err
	}
	for range words {
		size, err := next()
		if err != nil || at+size > len(data) {
			return nil, errMalformed
		}
		word := string(data[at : at+size])
		at += size
		count, err := next()
		if err != nil {
			return nil, err
		}
		size, err = next()
		if err != nil || at+size > len(data) {
			return nil, errMalformed
		}
		end := at + size
		if keep != nil && !keep(word) {
			at = end
			continue
		}
		list, doc := make([]posting, 0, count), 0
		for range count {
			delta, err := quantity()
			if err != nil {
				return nil, err
			}
			c, err := quantity()
			if err != nil {
				return nil, err
			}
			if doc += delta; doc >= docs {
				return nil, errMalformed
			}
			list = append(list, posting{int32(doc), int32(c)})
		}
		if at != end {
			return nil, errMalformed
		}
		x.postings[word] = list
	}
	if at != len(data) {
		return nil, errMalformed
	}
	return x, nil
}

// Search returns at most limit documents that hold words of query, best
// first: by score, then in the order they were added. A word that many
// documents hold weighs little and one that few hold weighs much, so a
// query can be plain words and names together. keep, when not nil, says
// which documents may be returned (a search of one directory); the others
// still weigh the words.
func (x *Index) Search(query string, limit int, keep func(doc int) bool) []Hit {
	var only func(int, int) bool
	if keep != nil {
		only = func(_, doc int) bool { return keep(doc) }
	}
	found := SearchAll([]*Index{x}, query, limit, only)
	if len(found) == 0 {
		return nil
	}
	hits := make([]Hit, len(found))
	for i, f := range found {
		hits[i] = f.Hit
	}
	return hits
}

// Found is a document SearchAll found: the index it is in, by its place in
// the list searched, and the document there.
type Found struct {
	Index int
	Hit
}

// SearchAll ranks the documents of several indexes as one collection, as
// Search ranks one: how many documents hold a word, and how long they are
// on average, are counted over all of them, so a score in one index weighs
// the same as in another (each earlier task of a project has an index of
// its own). Ties go to the earlier index, then the earlier document.
func SearchAll(xs []*Index, query string, limit int, keep func(index, doc int) bool) []Found {
	n, total := 0, 0
	for _, x := range xs {
		n += len(x.lengths)
		total += x.total
	}
	if n == 0 || total == 0 || limit < 1 {
		return nil
	}
	avg := float64(total) / float64(n)
	type key struct{ index, doc int }
	scores := map[key]float64{}
	matched := map[key][]string{}
	seen := map[string]bool{}
	for _, word := range Words(query) {
		if seen[word] {
			continue
		}
		seen[word] = true
		df := 0
		for _, x := range xs {
			df += len(x.postings[word])
		}
		if df == 0 {
			continue
		}
		idf := math.Log(1 + (float64(n)-float64(df)+0.5)/(float64(df)+0.5))
		for i, x := range xs {
			for _, p := range x.postings[word] {
				tf := float64(p.count)
				norm := 1 - b + b*float64(x.lengths[p.doc])/avg
				k := key{i, int(p.doc)}
				scores[k] += idf * tf * (k1 + 1) / (tf + k1*norm)
				matched[k] = append(matched[k], word)
			}
		}
	}
	found := make([]Found, 0, len(scores))
	for k, score := range scores {
		if keep == nil || keep(k.index, k.doc) {
			found = append(found, Found{Index: k.index, Hit: Hit{Doc: k.doc, Score: score, Matched: matched[k]}})
		}
	}
	slices.SortFunc(found, func(p, q Found) int {
		if c := cmp.Compare(q.Score, p.Score); c != 0 {
			return c
		}
		if c := cmp.Compare(p.Index, q.Index); c != 0 {
			return c
		}
		return cmp.Compare(p.Doc, q.Doc)
	})
	return found[:min(limit, len(found))]
}
