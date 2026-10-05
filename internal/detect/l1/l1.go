// Package l1 is the fast-path secret scanner (SPEC E1/E2, Phase 4).
//
// It classifies strings that look like credentials by shape and by entropy,
// cheaply and deterministically, so it can sit in the guard's hot path
// (p99 < 1 ms on 64 KB, Phase 4 accept) and later in the ingress redactor
// (Phase 7). It is a *candidate* detector, not an authority: a finding names
// a class, it does not decide. The guard turns a finding into a verdict
// (bound host ⇒ allow; otherwise deny), and L3 (Phase 8) later scores the
// "unknown-entropy" residual this package emits.
//
// The package is pure and depends on nothing in this module, so both the
// guard and the redactor consume it without an import cycle.
package l1

import (
	"cmp"
	"math"
	"regexp"
	"slices"
	"strings"
)

// Known secret classes. "unknown" is the entropy-only residual: a string
// that carries no recognised prefix but is long, high-entropy and has at
// least one word-joiner (dash or underscore). That class is intentionally
// its own finding so L3 (Phase 8) can take over the residual with a
// calibrated P(secret) without re-scanning.
const (
	ClassAnthropic      = "anthropic-key"
	ClassOpenAI         = "openai-key"
	ClassGitHub         = "github-token"
	ClassAWS            = "aws-access-key"
	ClassSlack          = "slack-token"
	ClassPEM            = "pem"
	ClassJWT            = "jwt"
	ClassUnknownEntropy = "unknown-entropy"
)

// Finding is one candidate secret in s: the class it was classified as and
// its [Start,End) byte range. Findings never overlap; Scan returns them in
// order of appearance, whatever their class, so a caller may walk them once
// from left to right (Redact does).
type Finding struct {
	Class string
	Start int
	End   int
}

// Value returns the matched substring.
func (f Finding) Value(s string) string { return s[f.Start:f.End] }

// Scanner classifies secrets in text. A zero Scanner is usable (no
// allowlist); New returns one with the built-in defaults.
type Scanner struct {
	// Allow holds exact secret values (or class names) to skip. See
	// AllowValue / AllowClass.
	Allow map[string]bool

	// Entropy is the minimum Shannon entropy (bits per byte) for the
	// unknown-entropy residual class. Defaults to DefEntropy.
	Entropy float64

	// MinLen is the minimum byte length for the unknown-entropy residual
	// class. Defaults to DefMinLen.
	MinLen int
}

// Defaults for the entropy/length gate.
const (
	DefEntropy = 3.7
	DefMinLen  = 24
)

// ClassNames returns the canonical class names, in the order the rules are
// tried. Useful for config validation and UI hints.
func ClassNames() []string {
	out := make([]string, 0, len(rules)+2)
	seen := map[string]bool{}
	for _, r := range rules {
		if !seen[r.class] {
			out = append(out, r.class)
			seen[r.class] = true
		}
	}
	out = append(out, ClassPEM, ClassUnknownEntropy)
	return out
}

// IsClass reports whether name is a known class.
func IsClass(name string) bool {
	for _, c := range ClassNames() {
		if c == name {
			return true
		}
	}
	return false
}

// New returns a Scanner with default thresholds and an empty allowlist.
func New() *Scanner {
	return &Scanner{Allow: map[string]bool{}, Entropy: DefEntropy, MinLen: DefMinLen}
}

// AllowValue skips any finding whose matched text equals value.
func (s *Scanner) AllowValue(value string) { s.ensureAllow(); s.Allow[value] = true }

// AllowClass suppresses every finding of class.
func (s *Scanner) AllowClass(class string) { s.ensureAllow(); s.Allow["@"+class] = true }

func (s *Scanner) ensureAllow() {
	if s.Allow == nil {
		s.Allow = map[string]bool{}
	}
}

// rule is one known-shape detector. joins is the separators (see
// isSeparator) a credential of the class can hold; any other separator in a
// token run is where one of the class begins or ends. prefix is the text
// every credential of the class begins with, so most points in a run are
// passed over without running the regex.
type rule struct {
	class  string
	joins  string
	prefix string
	re     *regexp.Regexp
}

// rules are the known-shape catalog (SPEC Phase 4: "prefix/shape regexes for
// Anthropic, OpenAI, GitHub, AWS, Slack, PEM, JWT"). Each is matched at a
// point in a whitespace-free token run (see isTokenChar and match), so it
// needs no word-boundary anchors. Order matters only where prefixes overlap
// (Anthropic "sk-ant-" is checked before OpenAI "sk-").
var rules = []rule{
	known(ClassAnthropic, "-_.", `sk-ant-[\w.-]{8,}`),
	// OpenAI: legacy sk-XXXX and current sk-proj-... / sk-svcacct-... shapes.
	known(ClassOpenAI, "-_", `sk-[A-Za-z0-9_-]{20,}`),
	known(ClassGitHub, "_", `(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}`),
	known(ClassGitHub, "_", `github_pat_[A-Za-z0-9_]{16,}`),
	known(ClassAWS, "", `AKIA[0-9A-Z]{12,}`),
	known(ClassSlack, "-", `xox[baprs]-[A-Za-z0-9-]{8,}`),
	// JWT: three dot-separated base64url parts whose header and payload are
	// encoded JSON objects, so they begin "eyJ" ({") and "ey" ({). Without
	// that anchor, dotted names such as the Django test label
	// "model_fields.test_jsonfield.TestQuerying" were taken for tokens and
	// the commands and judge requests carrying them were denied. The
	// signature may be empty (an unsigned "alg":"none" token).
	known(ClassJWT, "-_.", `eyJ[A-Za-z0-9_-]{5,}\.ey[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]*`),
}

// known makes the rule for a class's shape, which begins with literal text
// (its prefix, which starts reads). Its regex is anchored where it is tried
// and takes the longest text it can, since match decides where the
// credential ends.
func known(class, joins, shape string) rule {
	prefix, _ := regexp.MustCompile(shape).LiteralPrefix()
	re := regexp.MustCompile(`^(?:` + shape + `)`)
	re.Longest()
	return rule{class: class, joins: joins, prefix: prefix, re: re}
}

// starts marks the bytes a rule's prefix begins with: a point in a run that
// begins with any other byte begins no credential.
var starts = func() (t [256]bool) {
	for _, r := range rules {
		t[r.prefix[0]] = true
	}
	return t
}()

// pemRe matches a PEM block anywhere in the text (it spans newlines, so it
// is scanned over the whole string, not per token).
var pemRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]+-----.+?-----END [A-Z ]+-----`)

// Scan returns every credential-shaped span in text that the allowlist does
// not suppress, in order of appearance and never overlapping: PEM blocks and
// the tokens matched by the class rules, joined by join.
func (s *Scanner) Scan(text string) []Finding {
	if len(text) < 8 {
		return nil
	}
	var pems []Finding

	// PEM blocks span lines; detect them over the full text.
	if !s.suppressed("@" + ClassPEM) {
		for _, m := range pemRe.FindAllStringIndex(text, -1) {
			pems = append(pems, Finding{Class: ClassPEM, Start: m[0], End: m[1]})
		}
	}

	// Manual tokenization: walk the string and extract maximal runs of
	// secret-token characters. Avoids a regex scan over the whole input.
	toks := make([]Finding, 0, 4)
	i, n := 0, len(text)
	for i < n {
		// Skip separators.
		for i < n && !isTokenChar(text[i]) {
			i++
		}
		if i >= n {
			break
		}
		lo := i
		for i < n && isTokenChar(text[i]) {
			i++
		}
		toks = s.scanRun(toks, text, lo, i)
	}
	return join(pems, toks)
}

// join puts the PEM findings and the token findings, each list in order and
// without overlaps, into one list that is both. A token run can lie inside a
// block (a "Comment:" header line of a PGP key) or run across its edge
// ("xoxb-12345678-----BEGIN", since a dash is a token character), and a
// redactor that walks the list once would slice backwards over either.
//
// A block takes in the tokens that overlap it and keeps its class: its span
// grows to cover a known token that crosses its edge, because the text left
// outside it would still be a credential, and the guard still names the block.
// An unknown-entropy token that overlaps one is dropped instead: it is the
// residual nothing redacts or decides on, and growing a block over it would
// redact text no known class claims.
func join(pems, toks []Finding) []Finding {
	if len(pems) == 0 {
		return toks
	}
	all := append(pems, toks...)
	slices.SortFunc(all, func(a, b Finding) int { return cmp.Compare(a.Start, b.Start) })
	out := all[:0]
	for _, f := range all {
		if len(out) == 0 || f.Start >= out[len(out)-1].End {
			out = append(out, f)
			continue
		}
		last := &out[len(out)-1]
		switch {
		case f.Class == ClassUnknownEntropy:
		case last.Class == ClassUnknownEntropy:
			*last = f
		default:
			last.End = max(last.End, f.End)
			if f.Class == ClassPEM {
				last.Class = ClassPEM
			}
		}
	}
	return out
}

// scanRun appends the findings in the token run text[lo:hi]: the known
// credentials in it, or, when it holds none, the run itself if it is the
// unknown-entropy residual. A run is not one credential: a sentence's full
// stop, a path's slashes or a "+" join a key to its neighbours, and a rule
// matched against the whole run missed, so neither redacted nor checked,
// every key that ended a sentence ("The bucket key is AKIA....").
func (s *Scanner) scanRun(out []Finding, text string, lo, hi int) []Finding {
	if hi-lo < minTokenLen {
		return out
	}
	found := false
	for p := lo; p < hi; p++ {
		if !starts[text[p]] || p > lo && !isSeparator(text[p-1]) {
			continue
		}
		class, end := match(text, lo, p, hi)
		if class == "" {
			continue
		}
		found = true
		if !s.suppressed("@"+class) && !s.suppressed(text[p:end]) {
			out = append(out, Finding{Class: class, Start: p, End: end})
		}
		p = end - 1
	}
	if found {
		return out
	}
	tok := text[lo:hi]
	if s.isUnknownEntropy(tok) && !s.suppressed("@"+ClassUnknownEntropy) && !s.suppressed(tok) {
		out = append(out, Finding{Class: ClassUnknownEntropy, Start: lo, End: hi})
	}
	return out
}

// match returns the class of the known credential that begins at p in the
// token run text[lo:hi], and where it ends, or "" when none does. A
// credential begins at the run's start or after a separator its class does
// not hold, and runs to the next such separator or the run's end, leaving
// before it at most separators (a full stop, a closing dash). A separator
// the class does hold is no boundary, so a path's "/" splits a key from it
// while a base64url blob's "-" does not make "-sk-..." inside it a key.
func match(text string, lo, p, hi int) (string, int) {
	for _, r := range rules {
		if !strings.HasPrefix(text[p:hi], r.prefix) || p > lo && strings.IndexByte(r.joins, text[p-1]) >= 0 {
			continue
		}
		end := p
		for end < hi && (!isSeparator(text[end]) || strings.IndexByte(r.joins, text[end]) >= 0) {
			end++
		}
		m := r.re.FindStringIndex(text[p:end])
		if m == nil || strings.TrimLeft(text[p+m[1]:end], separators) != "" {
			continue
		}
		return r.class, p + m[1]
	}
	return "", 0
}

// isTokenChar reports whether b is a character that appears in secret
// tokens: letters, digits, and the separators - _ . / +.
func isTokenChar(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		isSeparator(b)
}

// separators are the token characters that are not letters or digits.
const separators = "-_./+"

// isSeparator reports whether b is one of separators.
func isSeparator(b byte) bool {
	return b == '-' || b == '_' || b == '.' || b == '/' || b == '+'
}

// suppressed reports whether class ("@"-prefixed) or an exact value is
// allowlisted.
func (s *Scanner) suppressed(key string) bool {
	return s.Allow != nil && s.Allow[key]
}

// minTokenLen is the shortest token that can match any rule (Slack "xoxb-"
// + 8 = 13). Tokens shorter than this are skipped without running any rule.
const minTokenLen = 13

// isUnknownEntropy is the L3 residual hook: a long, high-entropy run with
// a word-joiner (dash or underscore) and a digit. The joiner keeps ordinary
// base64 (e.g. embedded images) and pure-alnum hashes from tripping; the
// digit keeps out long identifiers, which random keys almost never
// resemble. Dotted names and paths are judged a segment at a time:
// model_fields.test_jsonfield.TestQuerying or tests/model_fields/x.py are
// short words joined, not one long random run.
func (s *Scanner) isUnknownEntropy(t string) bool {
	minLen := s.MinLen
	if minLen <= 0 {
		minLen = DefMinLen
	}
	th := s.Entropy
	if th <= 0 {
		th = DefEntropy
	}
	for _, seg := range strings.FieldsFunc(t, func(r rune) bool { return r == '.' || r == '/' }) {
		if len(seg) >= minLen && strings.ContainsAny(seg, "-_") && strings.ContainsAny(seg, "0123456789") && entropy(seg) >= th {
			return true
		}
	}
	return false
}

// Redact replaces each finding in text with "[REDACTED <class>]". Findings
// do not overlap, so substitution is a single left-to-right pass. Phase 7's
// ingress redactor uses this.
func (s *Scanner) Redact(text string) (string, []Finding) {
	fs := s.Scan(text)
	if len(fs) == 0 {
		return text, nil
	}
	var b strings.Builder
	last := 0
	for _, f := range fs {
		b.WriteString(text[last:f.Start])
		b.WriteString("[REDACTED " + f.Class + "]")
		last = f.End
	}
	b.WriteString(text[last:])
	return b.String(), fs
}

// entropy returns the Shannon entropy of b in bits per byte.
func entropy(b string) float64 {
	if len(b) == 0 {
		return 0
	}
	var freq [256]int
	for i := 0; i < len(b); i++ {
		freq[b[i]]++
	}
	total := float64(len(b))
	e := 0.0
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := float64(c) / total
		e -= p * math.Log2(p)
	}
	return e
}
