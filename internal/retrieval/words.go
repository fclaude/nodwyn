// Package retrieval ranks texts by the words they share with a query (ADR
// 50): the conversation's events now, and the repository's chunks later.
// It is words only, BM25 over them; a ranking by embeddings would be a
// second list fused with this one, and is not built.
package retrieval

import (
	"strings"
	"unicode"
)

// Words splits text into the words a search matches, as code names them:
// each run of letters, digits, '_' and '-' gives itself, lowercased, then
// its parts between '_' and '-', then each part's camel-case words. So
// "ScrubEnv" gives scrubenv, scrub
// and env; "index-grep" gives index-grep, index and grep; "HTTPServer"
// gives httpserver, http and server. Single characters are dropped: they
// match nearly everything.
func Words(text string) []string {
	var out []string
	add := func(w string) {
		if len([]rune(w)) > 1 {
			out = append(out, strings.ToLower(w))
		}
	}
	for _, token := range tokens(text) {
		add(token)
		parts := strings.FieldsFunc(token, func(r rune) bool { return r == '_' || r == '-' })
		for _, part := range parts {
			camel := camelWords(part)
			if len(parts) > 1 {
				add(part)
			}
			if len(camel) > 1 {
				for _, w := range camel {
					add(w)
				}
			}
		}
	}
	return out
}

// tokens are text's runs of letters, digits, '_' and '-', with the '_'
// and '-' at either end trimmed ("--force" is force).
func tokens(text string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-'
	}) {
		if f = strings.Trim(f, "_-"); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// camelWords splits a name at its case and digit changes: "parseHTTPHeader2"
// is parse, HTTP, Header, 2.
func camelWords(s string) []string {
	r := []rune(s)
	var out []string
	start := 0
	for i := 1; i < len(r); i++ {
		prev, cur := r[i-1], r[i]
		split := unicode.IsLower(prev) && unicode.IsUpper(cur) ||
			unicode.IsDigit(prev) != unicode.IsDigit(cur) ||
			// The last capital of a run starts the next word: HTTPServer.
			unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(r) && unicode.IsLower(r[i+1])
		if split {
			out = append(out, string(r[start:i]))
			start = i
		}
	}
	return append(out, string(r[start:]))
}
