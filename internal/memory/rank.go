package memory

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Ranking by words (ADR 51): BM25 over the entries' text. Identifiers carry
// most of the meaning in code work, so a token is also split where its case
// changes and at its underscores: TestPagerOrder is found by "pager", and
// still by "testpagerorder".

// BM25's usual constants.
const (
	k1 = 1.2
	b  = 0.75
)

// Index ranks a set of texts against a query.
type Index struct {
	docs []map[string]int
	lens []int
	df   map[string]int
	avg  float64
}

// Hit is a text's place in the ranking: its index among the texts and its
// score.
type Hit struct {
	Doc   int
	Score float64
}

// NewIndex indexes texts.
func NewIndex(texts []string) *Index {
	ix := &Index{df: map[string]int{}}
	total := 0
	for _, text := range texts {
		tf := map[string]int{}
		n := 0
		for _, t := range Terms(text) {
			tf[t]++
			n++
		}
		for t := range tf {
			ix.df[t]++
		}
		ix.docs = append(ix.docs, tf)
		ix.lens = append(ix.lens, n)
		total += n
	}
	if len(texts) > 0 {
		ix.avg = float64(total) / float64(len(texts))
	}
	return ix
}

// Search ranks the texts that share a term with query, best first, at most
// n of them.
func (ix *Index) Search(query string, n int) []Hit {
	terms := uniq(Terms(query))
	var hits []Hit
	N := float64(len(ix.docs))
	for i, tf := range ix.docs {
		score := 0.0
		for _, t := range terms {
			f := float64(tf[t])
			if f == 0 {
				continue
			}
			df := float64(ix.df[t])
			idf := math.Log(1 + (N-df+0.5)/(df+0.5))
			score += idf * f * (k1 + 1) / (f + k1*(1-b+b*float64(ix.lens[i])/ix.avg))
		}
		if score > 0 {
			hits = append(hits, Hit{i, score})
		}
	}
	sort.SliceStable(hits, func(a, c int) bool { return hits[a].Score > hits[c].Score })
	if len(hits) > n {
		hits = hits[:n]
	}
	return hits
}

// stop are words too common to say anything about which entry is meant.
var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields("a an and are as at be but by did do does for from had has have how i if in into is it its of on or so than that the then there these this those to was we were what when where which who why will with you your") {
		stop[w] = true
	}
}

// Terms are the words of text that ranking counts: lower-cased runs of
// letters and digits, each also split at its case changes and underscores,
// without the commonest English words.
func Terms(text string) []string {
	var out []string
	add := func(w string) {
		w = strings.ToLower(w)
		if len([]rune(w)) > 1 && !stop[w] {
			out = append(out, w)
		}
	}
	for _, word := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }) {
		parts := splitIdentifier(word)
		if len(parts) > 1 {
			add(strings.ReplaceAll(word, "_", ""))
		}
		for _, p := range parts {
			add(p)
		}
	}
	return out
}

// splitIdentifier splits a word at its underscores and where its case
// changes: TestPagerOrder, test_pager_order and HTTPServer.
func splitIdentifier(word string) []string {
	var parts []string
	for _, piece := range strings.Split(word, "_") {
		rs := []rune(piece)
		start := 0
		for i := 1; i < len(rs); i++ {
			lowerToUpper := unicode.IsLower(rs[i-1]) && unicode.IsUpper(rs[i])
			acronymEnd := i+1 < len(rs) && unicode.IsUpper(rs[i-1]) && unicode.IsUpper(rs[i]) && unicode.IsLower(rs[i+1])
			letterDigit := unicode.IsLetter(rs[i-1]) != unicode.IsLetter(rs[i])
			if lowerToUpper || acronymEnd || letterDigit {
				parts = append(parts, string(rs[start:i]))
				start = i
			}
		}
		if start < len(rs) {
			parts = append(parts, string(rs[start:]))
		}
	}
	return parts
}

func uniq(terms []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range terms {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// Snippet is the part of text around the first of query's words it holds,
// at most n bytes, or its start when it holds none.
func Snippet(text, query string, n int) string {
	lower := strings.ToLower(text)
	at := -1
	for _, w := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }) {
		if len(w) < 2 || stop[w] {
			continue
		}
		if i := strings.Index(lower, w); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	if at < 0 || len(text) <= n {
		return clip(oneLine(text), n)
	}
	start := max(0, at-n/3)
	for start > 0 && start < len(text) && !isBoundary(text, start) {
		start--
	}
	return "…" + clip(oneLine(text[start:]), n)
}

func isBoundary(s string, i int) bool { return i == 0 || (s[i]&0xC0) != 0x80 }
