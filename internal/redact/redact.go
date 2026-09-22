// Package redact is the single text-redaction controller: given a string and
// an L1 scanner, it replaces every recognised secret with a
// "[REDACTED <class>]" placeholder and reports what it removed.
//
// ADR 19: Jev is an egress edge and its payload "passes through the same
// redaction controller as the model context (E2/E4): L1, later L3,
// [REDACTED class]". Phase 5 ships this L1-only function; Phase 7 installs
// it in front of every context ingress and the log writer. It is deliberately
// a pure function of (text, scanner) so the Jev send path (P5) and the
// context ingress (P7) share exactly one implementation.
package redact

import (
	"github.com/fclaude/nibb/internal/detect/l1"
)

// Placeholder is what a redacted secret is replaced by. It names the class so
// the operator sees what left without seeing the value.
func Placeholder(class string) string { return "[REDACTED " + class + "]" }

// Event is one substitution Redact made.
type Event struct {
	Class string
	Start int // byte offset in the original text
	End   int // exclusive
}

// Result is the redacted text and the substitutions made, in order.
type Result struct {
	Text   string
	Events []Event
}

// Redact replaces every finding s makes in text with its placeholder. The
// unknown-entropy residual is NOT redacted: by the ladder principle it "feeds
// the decision with binding, never decides alone" (L3's job), and a bare
// high-entropy string is not a confirmed secret. Only known classes are
// redacted.
func Redact(text string, s *l1.Scanner) Result {
	if s == nil {
		s = l1.New()
	}
	fs := s.Scan(text)
	var out []byte
	last := 0
	var res Result
	for _, f := range fs {
		if f.Class == l1.ClassUnknownEntropy {
			continue
		}
		out = append(out, text[last:f.Start]...)
		out = append(out, Placeholder(f.Class)...)
		last = f.End
		res.Events = append(res.Events, Event{Class: f.Class, Start: f.Start, End: f.End})
	}
	res.Text = string(append(out, text[last:]...))
	return res
}
