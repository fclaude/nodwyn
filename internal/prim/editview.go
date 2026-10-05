package prim

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/fclaude/nodwyn/internal/detect/l1"
	"github.com/fclaude/nodwyn/internal/redact"
)

// ADR 43: fs-edit works on the file as the model reads it. Ingress redaction
// (ADR 25) shows the model a credential-shaped text as "[REDACTED <class>]"
// wherever it reads one, so an old copied from what it read holds a
// placeholder the file does not. The match is therefore made in the redacted
// text, and what it finds is a span of the real file, so a line with a
// credential on it can be edited without the model ever seeing the credential
// and without it being copied anywhere: the text a placeholder stands for
// stays where it is, or goes.

// placeholderRE finds the placeholders redaction writes.
var placeholderRE = func() *regexp.Regexp {
	var classes []string
	for _, c := range l1.ClassNames() {
		classes = append(classes, regexp.QuoteMeta(c))
	}
	return regexp.MustCompile(`\[REDACTED (` + strings.Join(classes, "|") + `)\]`)
}()

// view is a file's text as the model reads it, with where each placeholder in
// it came from.
type view struct {
	file string         // the file's own text
	text string         // the text the model reads: file, redacted
	subs []substitution // the credentials redaction replaced, in order
}

// substitution is one credential: text[vs:ve] is the placeholder that stands
// for file[cs:ce].
type substitution struct {
	vs, ve, cs, ce int
	class          string
}

// newView redacts file as ingress does (ADR 25: always the default scanner).
func newView(file string) view {
	r := redact.Redact(file, nil)
	v := view{file: file, text: r.Text}
	shift := 0 // how far text runs ahead of file at this point
	for _, e := range r.Events {
		n := len(redact.Placeholder(e.Class))
		v.subs = append(v.subs, substitution{vs: e.Start + shift, ve: e.Start + shift + n, cs: e.Start, ce: e.End, class: e.Class})
		shift += n - (e.End - e.Start)
	}
	return v
}

// span is where text[vs:ve] lies in the file. A match that starts or ends
// inside a placeholder is refused: half of one stands for nothing.
func (v view) span(vs, ve int) (cs, ce int, err error) {
	cs, ce = vs, ve
	for _, s := range v.subs {
		if (vs > s.vs && vs < s.ve) || (ve > s.vs && ve < s.ve) {
			return 0, 0, fmt.Errorf("old starts or ends inside a %s placeholder: include all of it, or none", redact.Placeholder(s.class))
		}
		if s.ve <= vs {
			cs += (s.ce - s.cs) - (s.ve - s.vs)
		}
		if s.ve <= ve {
			ce += (s.ce - s.cs) - (s.ve - s.vs)
		}
	}
	return cs, ce, nil
}

// held is a placeholder in a matched span and the file's text behind it.
type held struct {
	class    string
	original string
	redacted bool // a credential redaction replaced, not placeholder text the file holds itself
}

// heldIn lists the placeholders in text[vs:ve] in order, with what each stands
// for. A placeholder the file holds as text (redaction's own tests and docs do)
// reads the same as one redaction wrote, and stands for itself.
func (v view) heldIn(vs, ve int) []held {
	var out []held
	for _, m := range placeholderRE.FindAllStringSubmatchIndex(v.text[vs:ve], -1) {
		h := held{class: v.text[vs+m[2] : vs+m[3]], original: v.text[vs+m[0] : vs+m[1]]}
		for _, s := range v.subs {
			if s.vs == vs+m[0] {
				h.original, h.redacted = v.file[s.cs:s.ce], true
			}
		}
		out = append(out, h)
	}
	return out
}

// replacement is what to write over a matched span. When the span held a
// credential, a new that has the span's placeholders, in the same order,
// keeps what each stands for, and one with none drops them; any other
// placeholders cannot be paired, so they are refused rather than guessed at,
// which could move a credential or lose one. A span with no credential in it
// takes new as text, as every edit did before.
func replacement(span []held, repl string) (string, error) {
	credential := false
	for _, h := range span {
		credential = credential || h.redacted
	}
	if !credential {
		return repl, nil
	}
	ms := placeholderRE.FindAllStringSubmatchIndex(repl, -1)
	if len(ms) == 0 {
		return repl, nil
	}
	var inNew, inOld []string
	for _, m := range ms {
		inNew = append(inNew, repl[m[2]:m[3]])
	}
	for _, h := range span {
		inOld = append(inOld, h.class)
	}
	if !slices.Equal(inNew, inOld) {
		return "", fmt.Errorf("new has %s but old has %s: to keep what a [REDACTED <class>] placeholder stands for, new needs the same placeholders as old, in the same order; to drop it, leave them out",
			placeholderList(inNew), placeholderList(inOld))
	}
	var b strings.Builder
	last := 0
	for i, m := range ms {
		b.WriteString(repl[last:m[0]])
		b.WriteString(span[i].original)
		last = m[1]
	}
	b.WriteString(repl[last:])
	return b.String(), nil
}

// placeholderList names classes' placeholders for an error.
func placeholderList(classes []string) string {
	if len(classes) == 0 {
		return "none"
	}
	return "[REDACTED " + strings.Join(classes, "], [REDACTED ") + "]"
}

// apply is applyEdit for a file that holds credentials: old is matched in the
// text the model reads, and each match is a span of the file. found is false
// when old does not occur there, and the caller looks for it in the file
// itself, as before: a text the model typed that is what the file holds still
// matches, which tells it nothing it could not try anyway.
func (v view) apply(old, repl string, all bool) (next string, found bool, err error) {
	var starts []int
	for at := 0; ; {
		i := strings.Index(v.text[at:], old)
		if i < 0 {
			break
		}
		starts = append(starts, at+i)
		at += i + len(old)
	}
	switch {
	case len(starts) == 0:
		return "", false, nil
	case len(starts) > 1 && !all:
		return "", true, fmt.Errorf("old text occurs %d times; include more context so it is unique, or pass replace_all to replace every one", len(starts))
	}
	var b strings.Builder
	last := 0
	for _, vs := range starts {
		cs, ce, err := v.span(vs, vs+len(old))
		if err != nil {
			return "", true, err
		}
		with, err := replacement(v.heldIn(vs, vs+len(old)), repl)
		if err != nil {
			return "", true, err
		}
		b.WriteString(v.file[last:cs])
		b.WriteString(with)
		last = ce
	}
	b.WriteString(v.file[last:])
	return b.String(), true, nil
}
