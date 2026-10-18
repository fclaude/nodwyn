package prim

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// When old matches nowhere, even ignoring whitespace, the next try needs the
// file's own text, and the model has to find it. The error used to say to
// read the region again with a tool the two-tool configuration does not
// offer: of the 73 failed edits of its benchmark arms, 24 were this miss, and
// the model recovered through sed -n in 3.9 calls on average (1.6 in the
// six-tool arm). The error now shows the lines old most resembles, numbered,
// or says nothing does (ADR 32, amended 2026-10-17).

const (
	// closestLines is how many lines of the closest text the error shows.
	closestLines = 5
	// closestLineBytes cuts one of them; the lot stays within editHintBytes.
	closestLineBytes = 200
	// closestFollow is how many lines after the first old's tie-break reads.
	closestFollow = 4
	// closestMin is how alike a line must be to be shown (resemblance): in
	// the lean sessions' transcripts the closest text was the right one 3
	// times of 4, and the fourth a line that shared a word or two (ADR 32,
	// amended 2026-10-17). The cases this package's tests show score 0.57 or
	// more; a line with two of old's words among four others scores 0.44.
	closestMin = 0.5
)

// words are the lower-cased runs of letters, digits and underscores in s, by
// how many times each occurs, and their number. Punctuation says little of
// what a line means, and every line has some.
func words(s string) (map[string]int, int) {
	out := map[string]int{}
	n := 0
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }) {
		out[w]++
		n++
	}
	return out, n
}

// resemblance is how alike two lines are by their words: twice the words they
// share over the words they have, 0 to 1, and the number of words shared.
func resemblance(a map[string]int, na int, b string) (score float64, common int) {
	wb, nb := words(b)
	for w, k := range a {
		common += min(k, wb[w])
	}
	if na+nb == 0 {
		return 0, 0
	}
	return float64(2*common) / float64(na+nb), common
}

// closestRegion says where in the file old was probably meant, for a miss:
// the line most like old's first line, with the lines around it numbered as
// the file has them. A line counts only when it holds at least two of old's
// words and half of them, and is at least closestMin alike, so a text that is
// nowhere in the file is not answered with an unrelated line; of equal lines the one whose following lines resemble the rest of
// old's more is taken, then the first. want is old's normalized lines, lines
// the file's, each with its line ending.
func closestRegion(lines, want []string) string {
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	first, nFirst := words(want[0])
	best, bestScore, bestFollow := -1, 0.0, 0.0
	for i, l := range lines {
		score, common := resemblance(first, nFirst, l)
		// A first line of one word counts for a line of that word alone.
		single := nFirst == 1 && score == 1 && len(want[0]) >= 3
		if score < closestMin || (!single && (common < 2 || 2*common < nFirst)) {
			continue
		}
		if best >= 0 && score < bestScore {
			continue
		}
		follow := 0.0
		for k := 1; k <= closestFollow && k < len(want) && i+k < len(lines); k++ {
			wk, nk := words(want[k])
			s, _ := resemblance(wk, nk, lines[i+k])
			follow += s
		}
		if best < 0 || score > bestScore || follow > bestFollow {
			best, bestScore, bestFollow = i, score, follow
		}
	}
	if best < 0 {
		return fmt.Sprintf("there is no close match for the first line of old (`%s`) in this file: the text may be in another file, or may have changed since you looked", cutRunes(strings.TrimSpace(want[0]), 100))
	}
	// A window of closestLines lines around the best, moved rather than
	// shortened at either end of the file.
	lo := best - closestLines/2
	hi := lo + closestLines
	if lo < 0 {
		hi, lo = hi-lo, 0
	}
	if hi > len(lines) {
		lo, hi = lo-(hi-len(lines)), len(lines)
	}
	lo = max(lo, 0)
	var b strings.Builder
	fmt.Fprintf(&b, "the closest text is at lines %d-%d (old's first line resembles line %d), exactly:", lo+1, hi, best+1)
	for i := lo; i < hi; i++ {
		fmt.Fprintf(&b, "\n%d\t%s", i+1, cutRunes(strings.TrimRight(lines[i], "\r\n"), closestLineBytes))
	}
	return b.String()
}

// cutRunes is s cut to at most n bytes at a character boundary, with an
// ellipsis when something went.
func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
