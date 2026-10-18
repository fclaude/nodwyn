package prim

import (
	"strings"
)

// The recurring fs-edit miss of the lean profile's sessions was an old with a
// literal backslash-n where a newline was meant (a model that escaped its JSON
// twice): five sessions, three or four calls each, and fs-edit given up for a
// Python script that made the change. An old that matches nowhere as written
// but holds \n, \t or \" sequences is tried once more unescaped, and when that
// matches exactly once the edit is made so, with new unescaped the same way,
// and the answer says so (ADR 32, amended 2026-10-17).

// escapeNames are the sequences unescape undoes that the note names, and what
// each was taken for.
var escapeNames = []struct{ seq, as string }{{`\n`, "newlines"}, {`\t`, "tabs"}, {`\"`, "quotes"}}

// unescape undoes one level of a string's escaping: \n, \t, \" and \; any
// other backslash stays as it is. held lists the sequences of escapeNames it
// undid.
func unescape(s string) (out string, held []string) {
	if !strings.Contains(s, `\`) {
		return s, nil
	}
	seen := map[string]bool{}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
			seen[`\n`] = true
		case 't':
			b.WriteByte('\t')
			seen[`\t`] = true
		case '"':
			b.WriteByte('"')
			seen[`\"`] = true
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte(s[i])
			continue
		}
		i++
	}
	for _, e := range escapeNames {
		if seen[e.seq] {
			held = append(held, e.seq)
		}
	}
	return b.String(), held
}

// unescapedEdit is the edit e of content with old and new unescaped, when old
// held escape sequences and matches exactly once so (whitespace as exact as
// ever); ok is false otherwise. note says what was done.
func unescapedEdit(content string, e edit) (next string, sp []splice, note string, ok bool) {
	old, held := unescape(e.old)
	if len(held) == 0 || old == "" {
		return "", nil, "", false
	}
	starts := occurrences(content, old)
	if len(starts) != 1 {
		return "", nil, "", false
	}
	repl, _ := unescape(e.new)
	at := starts[0]
	var as []string
	for _, h := range held {
		for _, n := range escapeNames {
			if n.seq == h {
				as = append(as, n.as)
			}
		}
	}
	note = "old held literal " + strings.Join(held, " and ") + " sequences; they were matched as real " + strings.Join(as, " and ")
	return content[:at] + repl + content[at+len(old):], []splice{{at: at, del: len(old), ins: len(repl)}}, note, true
}
