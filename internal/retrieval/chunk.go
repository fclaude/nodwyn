package retrieval

import (
	"regexp"
	"strings"
)

// A chunk of a file starts at a line that begins, at its first column, a
// declaration or a heading: Go's func, type, var and const, Scheme's
// (define, JavaScript's function, class and export, Python's def and
// class, a # heading in Markdown, TeX's \chapter and \section. Nothing
// parses the language, so any file is read the same way; this is the rule
// ADR 50's replay of the person's searches measured.
var chunkStart = regexp.MustCompile(`^(func |type |var |const |\(define|#|\\section|\\chapter|export |function |class |def )`)

const (
	chunkMin = 20 // a chunk ends at a new start only once it has this many lines
	chunkMax = 80 // and ends here whatever follows
	// ChunkVersion names this chunker and Words: a change to either
	// changes what a stored chunk means, so it is part of a chunk's key.
	ChunkVersion = 1
)

// Chunk is lines Start to End of a file, from 1 and inclusive, with its
// words counted. The first line's count three times: a declaration or a
// heading names what follows it.
type Chunk struct {
	Start int            `json:"start"`
	End   int            `json:"end"`
	Words map[string]int `json:"words"`
}

// Chunks cuts text into chunks (ADR 50 §6). Lines are counted as fs-read
// counts them: a last line without a newline is a line, and nothing after
// a final newline is.
func Chunks(text string) []Chunk {
	if text == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	var out []Chunk
	start := 0
	cut := func(end int) {
		c := Chunk{Start: start + 1, End: end, Words: map[string]int{}}
		Count(c.Words, lines[start], 3)
		for _, line := range lines[start+1 : end] {
			Count(c.Words, line, 1)
		}
		out = append(out, c)
		start = end
	}
	for i := 1; i < len(lines); i++ {
		if i-start >= chunkMax || i-start >= chunkMin && chunkStart.MatchString(lines[i]) {
			cut(i)
		}
	}
	cut(len(lines))
	return out
}
