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
	"math"
	"regexp"
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
// order of appearance.
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

// rule is one known-shape detector.
type rule struct {
	class string
	re    *regexp.Regexp
}

// rules are the known-shape catalog (SPEC Phase 4: "prefix/shape regexes for
// Anthropic, OpenAI, GitHub, AWS, Slack, PEM, JWT"). The regexes are
// matched against a single whitespace-free token (see isTokenChar), so they
// need no word-boundary anchors. Order matters only where prefixes overlap
// (Anthropic "sk-ant-" is checked before OpenAI "sk-").
var rules = []rule{
	{ClassAnthropic, regexp.MustCompile(`^sk-ant-[\w.-]{8,}$`)},
	// OpenAI: legacy sk-XXXX and current sk-proj-... / sk-svcacct-... shapes.
	{ClassOpenAI, regexp.MustCompile(`^sk-[A-Za-z0-9_-]{20,}$`)},
	{ClassGitHub, regexp.MustCompile(`^(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}$`)},
	{ClassGitHub, regexp.MustCompile(`^github_pat_[A-Za-z0-9_]{16,}$`)},
	{ClassAWS, regexp.MustCompile(`^AKIA[0-9A-Z]{12,}$`)},
	{ClassSlack, regexp.MustCompile(`^xox[baprs]-[A-Za-z0-9-]{8,}$`)},
	// JWT: three dot-separated base64url parts. Each of the first two parts
	// is required to be >= 8 chars so version strings like "1.2.3" and hosts
	// like "api.openai.com" do not false-positive.
	{ClassJWT, regexp.MustCompile(`^[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{4,}$`)},
}

// pemRe matches a PEM block anywhere in the text (it spans newlines, so it
// is scanned over the whole string, not per token).
var pemRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]+-----.+?-----END [A-Z ]+-----`)

// Scan returns every credential-shaped span in text that the allowlist does
// not suppress: PEM blocks, then tokens matched by the class rules, in order.
func (s *Scanner) Scan(text string) []Finding {
	if len(text) < 8 {
		return nil
	}
	out := make([]Finding, 0, 4)

	// PEM blocks span lines; detect them over the full text.
	for _, m := range pemRe.FindAllStringIndex(text, -1) {
		if !s.suppressed("@" + ClassPEM) {
			out = append(out, Finding{Class: ClassPEM, Start: m[0], End: m[1]})
		}
	}

	// Manual tokenization: walk the string and extract maximal runs of
	// secret-token characters. Avoids a regex scan over the whole input.
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
		tok := text[lo:i]
		class := s.classify(tok)
		if class == "" {
			continue
		}
		if s.suppressed("@"+class) || s.suppressed(tok) {
			continue
		}
		out = append(out, Finding{Class: class, Start: lo, End: i})
	}
	return out
}

// isTokenChar reports whether b is a character that appears in secret
// tokens: letters, digits, and the separators - _ . / +.
func isTokenChar(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '-' || b == '_' || b == '.' || b == '/' || b == '+'
}

// suppressed reports whether class ("@"-prefixed) or an exact value is
// allowlisted.
func (s *Scanner) suppressed(key string) bool {
	return s.Allow != nil && s.Allow[key]
}

// minTokenLen is the shortest token that can match any rule (Slack "xoxb-"
// + 8 = 13). Tokens shorter than this are skipped without running any rule.
const minTokenLen = 13

func (s *Scanner) classify(t string) string {
	if len(t) < minTokenLen {
		return ""
	}
	for _, r := range rules {
		if r.re.MatchString(t) {
			return r.class
		}
	}
	if s.isUnknownEntropy(t) {
		return ClassUnknownEntropy
	}
	return ""
}

// isUnknownEntropy is the L3 residual hook: long, high-entropy, and with at
// least one word-joiner (dash or underscore). The joiner requirement is
// what keeps ordinary base64 (e.g. embedded images) and pure-alnum hashes
// from tripping, while still catching random-looking keys and JWTs that
// slipped past the prefix rules.
func (s *Scanner) isUnknownEntropy(t string) bool {
	minLen := s.MinLen
	if minLen <= 0 {
		minLen = DefMinLen
	}
	th := s.Entropy
	if th <= 0 {
		th = DefEntropy
	}
	if len(t) < minLen {
		return false
	}
	if !strings.ContainsAny(t, "-_") {
		return false
	}
	return entropy(t) >= th
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
