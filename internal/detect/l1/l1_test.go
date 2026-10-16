package l1

import (
	"regexp/syntax"
	"slices"
	"strings"
	"testing"
)

// sampleSecrets: one per known class (Phase 4 accept: precision/recall on a
// curated set). Each must be detected and named with its class.
var sampleSecrets = []struct {
	secret string
	want   string
}{
	{"sk-ant-api03-AbCdEfGhIjKlMnOp", ClassAnthropic},
	{"sk-aBcDeFgHiJkLmNoPqRsTuVwXyZ", ClassOpenAI},
	{"sk-proj-Ab12Cd34Ef56Gh78Ij90Kl12Mn34Op56Qr78", ClassOpenAI},
	{"sk-svcacct-Ab12Cd34Ef56Gh78Ij90Kl12Mn34", ClassOpenAI},
	{"ghp_AbCdEfGhIjKlMnOpQrStUvWxYz", ClassGitHub},
	{"github_pat_AbCdEfGhIjKlMnOpQrStUv", ClassGitHub},
	{"AKIAIOSFODNN7EXAMPLE", ClassAWS},
	{"xoxb-1234567890-abcdefghijklmnop", ClassSlack},
	{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dozjgNryP4J3j3c8sxYm3d", ClassJWT},
	{"eyJhbGciOiJub25lIn0.eyJzdWIiOiIxIn0.", ClassJWT}, // unsigned
}

func TestKnownClasses(t *testing.T) {
	s := New()
	for _, c := range sampleSecrets {
		fs := s.Scan(c.secret)
		if len(fs) == 0 {
			t.Errorf("%s: no finding for %q", c.want, c.secret)
			continue
		}
		if fs[0].Class != c.want {
			t.Errorf("%s: got class %q for %q", c.want, fs[0].Class, c.secret)
		}
		if fs[0].Start != 0 || fs[0].End != len(c.secret) {
			t.Errorf("%s: %+v does not cover all of %q", c.want, fs[0], c.secret)
		}
	}
}

func TestEmbeddedInContext(t *testing.T) {
	s := New()
	cases := []struct {
		text string
		want string
	}{
		{"Authorization: Bearer sk-ant-api03-AbCdEfGhIjKlMnOp", ClassAnthropic},
		{`{"api_key": "sk-aBcDeFgHiJkLmNoPqRsTuVwXyZ"}`, ClassOpenAI},
		{"AKIAIOSFODNN7EXAMPLE", ClassAWS},
		{"ghp_AbCdEfGhIjKlMnOpQrStUvWxYz", ClassGitHub},
	}
	for _, c := range cases {
		fs := s.Scan(c.text)
		found := false
		for _, f := range fs {
			if f.Class == c.want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q: did not find class %s in %q", c.want, c.want, c.text)
		}
	}
}

// TestKeyEndingASentenceIsRedacted: the texts a probe on 2026-10-05 found
// unredacted. The full stop joined the key into one token run, the run as a
// whole matched no rule, and so the key was neither redacted nor checked.
func TestKeyEndingASentenceIsRedacted(t *testing.T) {
	s := New()
	for in, want := range map[string]string{
		"The token is ghp_A1b2C3d4A1b2C3d4A1b2C3d4A1b2C3d4A1b2C3d4.": "The token is [REDACTED github-token].",
		"ghp_A1b2C3d4A1b2C3d4A1b2C3d4A1b2C3d4A1b2C3d4.":              "[REDACTED github-token].",
		"The bucket key is AKIAABCDEFGHIJKLMNOP.":                    "The bucket key is [REDACTED aws-access-key].",
	} {
		if got, _ := s.Redact(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// TestKeyJoinedToItsNeighbours: every class is found where a separator joins
// it to the text around it, at the end of a sentence, in a path, between "+"
// signs or in a URL, and the finding covers the key.
func TestKeyJoinedToItsNeighbours(t *testing.T) {
	s := New()
	for _, c := range sampleSecrets {
		for _, form := range []string{"The token is %s.", "(%s).", "%s...", "keys/%s", "%s/backup", "a+%s+b", "https://%s@github.com/o/r", "%s-"} {
			text := strings.Replace(form, "%s", c.secret, 1)
			at := strings.Index(text, c.secret)
			fs := s.Scan(text)
			if len(fs) != 1 || fs[0].Class != c.want || fs[0].Start != at || fs[0].End < at+len(c.secret) {
				t.Errorf("%q: %+v, want one %s covering [%d,%d)", text, fs, c.want, at, at+len(c.secret))
			}
		}
	}
}

// TestSeparatorsMakeNoKeys: looking for a key inside a token run must not
// find one in text that only has separators in it. A separator a class can
// hold is no boundary for it, so a base64url blob holding "-sk-" is not an
// OpenAI key.
func TestSeparatorsMakeNoKeys(t *testing.T) {
	s := New()
	for _, text := range []string{
		"/usr/lib/python3.12/site-packages/sklearn/utils/_testing.py",
		"src/AKIAstuff/sk-learn-wrapper/ghp_notes.md",
		"https://example.com/AKIA/sk-/ghp_/eyJ.ey.x",
		"github.com/fclaude/nodwyn/internal/detect/l1.(*Scanner).Scan",
		"v1.2.3-rc.1+build.20241005.e3b0c44298fc1c14",
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
		"h1:Q8mtSL7SzHKkLBq0tzjr2DGJmnZh1x2cn14Ow=",
		"Zm9vYmFy-sk-0123456789abcdefghij_Zm9vYmFy",
	} {
		for _, f := range s.Scan(text) {
			if f.Class != ClassUnknownEntropy {
				t.Errorf("%q: %+v", text, f)
			}
		}
	}
}

// TestJoinsAreWhatTheShapesHold: a rule's joins must be the separators its
// shape can match. One it can match that joins leaves out would split its
// credentials apart; one joins names that it cannot match would hide a
// boundary.
func TestJoinsAreWhatTheShapesHold(t *testing.T) {
	var holds func(re *syntax.Regexp, c rune) bool
	holds = func(re *syntax.Regexp, c rune) bool {
		switch re.Op {
		case syntax.OpLiteral:
			return slices.Contains(re.Rune, c)
		case syntax.OpCharClass:
			for i := 0; i < len(re.Rune); i += 2 {
				if re.Rune[i] <= c && c <= re.Rune[i+1] {
					return true
				}
			}
			return false
		}
		return slices.ContainsFunc(re.Sub, func(sub *syntax.Regexp) bool { return holds(sub, c) })
	}
	for _, r := range rules {
		re, err := syntax.Parse(r.re.String(), syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		held := ""
		for _, c := range separators {
			if holds(re, c) {
				held += string(c)
			}
		}
		if held != r.joins {
			t.Errorf("%s %s: joins %q, the shape holds %q", r.class, r.re, r.joins, held)
		}
	}
}

func TestPEM(t *testing.T) {
	s := New()
	pem := "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END PRIVATE KEY-----\n"
	fs := s.Scan(pem)
	if len(fs) == 0 || fs[0].Class != ClassPEM {
		t.Fatalf("PEM: got %+v, want one pem finding", fs)
	}
}

// TestUnknownEntropy covers the L3 residual hook: long, high-entropy, with a
// joiner, no recognised prefix.
func TestUnknownEntropy(t *testing.T) {
	s := New()
	// A 40-char high-entropy blob with dashes, no known prefix.
	blob := "A1b2-C3d4_E5f6-G7h8I9j0-kLmN0oPq"
	fs := s.Scan(blob)
	found := false
	for _, f := range fs {
		if f.Class == ClassUnknownEntropy {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("blob %q (entropy %.2f): expected unknown-entropy finding, got %+v", blob, entropy(blob), fs)
	}
	// A short or low-entropy joiner string is not flagged.
	if fs := s.Scan("hello-world"); len(fs) != 0 {
		t.Errorf("low-entropy joiner string falsely flagged: %+v", fs)
	}
	// A long base64 blob with no joiner is not flagged.
	if fs := s.Scan("A1b2C3d4E5f6G7h8I9j0kLmN0oPqR"); len(fs) != 0 {
		t.Errorf("joinerless blob falsely flagged: %+v", fs)
	}
}

// TestFalsePositives is the Phase 4 FP sweep: SHAs, UUIDs, base64 (images)
// and version strings must NOT be flagged.
func TestFalsePositives(t *testing.T) {
	s := New()
	notSecrets := []string{
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", // SHA-256
		"deadbeef-dead-4bad-9bad-4deadbeefcaf",                             // UUID
		"eyJhbGciOiJSUzI1NiJ9.eyJ0eXAiOiJKV1QiLCJ0eXoi:JWT.x",              // malformed (has a colon)
		"1.2.3",                               // version
		"api.openai.com",                      // host (3 short parts)
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQ", // base64 (no joiner, image-ish)
		"https://example.com/path?x=1234567890abcdef",                      // URL
		"abcdef1234567890abcdef1234567890abcdef12",                         // 40 hex, no joiner
		"release-2024-05-01",                                               // low entropy
		"1234567890123456789012345678901234567890123456789012345678901234", // long digits
	}
	for _, ts := range notSecrets {
		if fs := s.Scan(ts); len(fs) != 0 {
			t.Errorf("false positive on %q: %+v", ts, fs)
		}
	}
}

// TestDottedNamesAreNotSecrets: test labels, module paths and file paths
// have the JWT's three-part shape and are long, but they are short words
// joined. Reported as a named class they denied benchmark test commands;
// they are not even the unknown-entropy residual.
func TestDottedNamesAreNotSecrets(t *testing.T) {
	s := New()
	for _, name := range []string{
		"model_fields.test_jsonfield.TestQuerying",
		"invalid_models_tests.test_models.OtherModelTests",
		"migrations.test_operations.OperationTests",
		"xarray.tests.test_computation",
		"com.example.MyApplicationTests",
		"test_serialization.TestJsonSerializer.test_roundtrip_unicode",
		"tests/model_fields/test_jsonfield.py",
		"test_roundtrip_unicode_characters_in_nested_keys",
	} {
		if fs := s.Scan("python tests/runtests.py -v 2 " + name); len(fs) != 0 {
			t.Errorf("%q reported: %+v", name, fs)
		}
	}
	// A random key is still the residual, alone or at the end of a path.
	for _, key := range []string{"A1b2-C3d4_E5f6-G7h8I9j0-kLmN0oPq", "vault/A1b2-C3d4_E5f6-G7h8I9j0-kLmN0oPq"} {
		fs := s.Scan(key)
		if len(fs) != 1 || fs[0].Class != ClassUnknownEntropy {
			t.Errorf("%q: %+v", key, fs)
		}
	}
}

// TestAllowlist: an exact value or a whole class can be suppressed.
func TestAllowlist(t *testing.T) {
	s := New()
	s.AllowValue("AKIAIOSFODNN7EXAMPLE")
	for _, text := range []string{"AKIAIOSFODNN7EXAMPLE", "The key is AKIAIOSFODNN7EXAMPLE."} {
		if fs := s.Scan(text); len(fs) != 0 {
			t.Errorf("AllowValue did not suppress %q: %+v", text, fs)
		}
	}

	s2 := New()
	s2.AllowClass(ClassGitHub)
	if fs := s2.Scan("ghp_AbCdEfGhIjKlMnOpQrStUvWxYz"); len(fs) != 0 {
		t.Errorf("AllowClass did not suppress: %+v", fs)
	}
}

// TestRedact: Phase 7's redactor uses this; verify the substitution.
func TestRedact(t *testing.T) {
	s := New()
	in := `header "Authorization: Bearer sk-ant-api03-AbCdEfGhIjKlMnOp" and aws AKIAIOSFODNN7EXAMPLE`
	out, fs := s.Redact(in)
	if len(fs) != 2 {
		t.Fatalf("Redact: got %d findings, want 2 (%+v)", len(fs), fs)
	}
	if !strings.Contains(out, "[REDACTED anthropic-key]") || !strings.Contains(out, "[REDACTED aws-access-key]") {
		t.Errorf("Redact: output missing placeholders: %q", out)
	}
	if strings.Contains(out, "sk-ant") || strings.Contains(out, "AKIA") {
		t.Errorf("Redact: secret still present: %q", out)
	}
}

// TestFindingsAreOrderedAndDisjoint: a credential before a PEM block used to
// come after it in Scan's result, and Redact sliced backwards over it and
// panicked. Whatever the mix, findings are in order of appearance and none
// overlaps the next.
func TestFindingsAreOrderedAndDisjoint(t *testing.T) {
	s := New()
	pem := "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END PRIVATE KEY-----"
	for _, in := range []string{
		"AKIAIOSFODNN7EXAMPLE\n" + pem,
		pem + "\nAKIAIOSFODNN7EXAMPLE",
		"AKIAIOSFODNN7EXAMPLE\n" + pem + "\nghp_AbCdEfGhIjKlMnOpQrStUvWxYz",
		pem + "\n" + pem,
		pem + "\nAKIAIOSFODNN7EXAMPLE\n" + pem,
	} {
		fs := s.Scan(in)
		if len(fs) < 2 {
			t.Fatalf("%q: %+v, want a finding for each credential", in, fs)
		}
		for i := 1; i < len(fs); i++ {
			if fs[i-1].End > fs[i].Start {
				t.Errorf("%q: findings %+v and %+v are out of order or overlap", in, fs[i-1], fs[i])
			}
		}
		if out, _ := s.Redact(in); strings.Contains(out, "AKIA") || strings.Contains(out, "MIIEvQ") || strings.Contains(out, "ghp_") {
			t.Errorf("%q: Redact left a credential in %q", in, out)
		}
	}

	got := s.Scan("AKIAIOSFODNN7EXAMPLE\n" + pem)
	want := []Finding{{ClassAWS, 0, 20}, {ClassPEM, 21, 21 + len(pem)}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("a key before a block: got %+v, want %+v", got, want)
	}
	out, _ := s.Redact("AKIAIOSFODNN7EXAMPLE\n" + pem)
	if out != "[REDACTED aws-access-key]\n[REDACTED pem]" {
		t.Errorf("Redact: %q", out)
	}
}

// TestTokensInsideAPEMBlockAreItsOwn: a run of token characters inside a
// block (the header lines of a PGP key) is part of the block, not a finding
// beside it, and the block still carries the guard's verdict by its class.
func TestTokensInsideAPEMBlockAreItsOwn(t *testing.T) {
	s := New()
	in := "-----BEGIN PGP PRIVATE KEY BLOCK-----\nComment: AKIAIOSFODNN7EXAMPLE\nComment: A1b2-C3d4_E5f6-G7h8I9j0-kLmN0oPq\nMIIEvQ\n-----END PGP PRIVATE KEY BLOCK-----"
	fs := s.Scan(in)
	if len(fs) != 1 || fs[0].Class != ClassPEM || fs[0].Start != 0 || fs[0].End != len(in) {
		t.Fatalf("got %+v, want the block alone", fs)
	}
	// With the class allowed the block is not a finding, and what is inside
	// it is, as it was before blocks were joined with tokens.
	s.AllowClass(ClassPEM)
	fs = s.Scan(in)
	if len(fs) != 2 || fs[0].Class != ClassAWS || fs[1].Class != ClassUnknownEntropy {
		t.Errorf("block allowed: got %+v, want the key and the residual", fs)
	}
}

// TestATokenAcrossAPEMEdge: a dash is a token character, so a key pasted up
// against "-----BEGIN" is one run with it. The key is a credential, and the
// block's span must cover it or Redact would leave it behind.
func TestATokenAcrossAPEMEdge(t *testing.T) {
	s := New()
	pem := "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END PRIVATE KEY-----"
	in := "KEY=sk-ant-api03-AbCdEfGhIj" + pem
	fs := s.Scan(in)
	if len(fs) != 1 || fs[0].Class != ClassPEM || fs[0].Start != len("KEY=") || fs[0].End != len(in) {
		t.Fatalf("got %+v, want one block from the start of the key", fs)
	}
	if out, _ := s.Redact(in); out != "KEY=[REDACTED pem]" {
		t.Errorf("Redact: %q", out)
	}
	// The residual that touches a block is dropped, not made part of it:
	// nothing redacts it, so the text beside the block stays as it was.
	for _, in := range []string{pem + "A1b2-C3d4_E5f6-G7h8I9j0-kLmN0oPq", "A1b2-C3d4_E5f6-G7h8I9j0-kLmN0oPq" + pem} {
		fs := s.Scan(in)
		if len(fs) != 1 || fs[0].Class != ClassPEM || fs[0].End-fs[0].Start != len(pem) {
			t.Errorf("%q: got %+v, want the block alone", in, fs)
		}
	}
}

// TestEntropy sanity-checks the helper.
func TestEntropy(t *testing.T) {
	if e := entropy("aaaa"); e != 0 {
		t.Errorf("entropy(\"aaaa\"): got %f, want 0", e)
	}
	// A uniform-ish high-entropy string should be near 4 bits/char.
	if e := entropy("abcd"); e < 1.9 || e > 2.1 {
		t.Errorf("entropy(\"abcd\"): got %f, want ~2", e)
	}
}

// TestScanBounds guards against the fuzz invariants: indices in range, no
// negative lengths, Value() reconstructs a substring.
func TestScanBounds(t *testing.T) {
	s := New()
	for _, text := range []string{"", "a", "sk-ant-api03-AbCdEfGhIjKlMnOp", "x sk-ant-api03-AbCdEfGhIjKlMnOp y"} {
		for _, f := range s.Scan(text) {
			if f.Start < 0 || f.End > len(text) || f.Start >= f.End {
				t.Errorf("%q: bad bounds %+v", text, f)
				break
			}
			if f.Value(text) != text[f.Start:f.End] {
				t.Errorf("%q: Value mismatch", text)
			}
		}
	}
}

// BenchmarkScan64KB is the Phase 4 perf gate (p99 < 1 ms on 64 KB).
func BenchmarkScan64KB(b *testing.B) {
	s := New()
	// 64 KB of mixed content with a few embedded secrets.
	chunk := strings.Repeat("some ordinary text with a dash-like token and numbers 123456 ", 1000)
	in := chunk
	if len(in) < 64<<10 {
		in = strings.Repeat(in, (64<<10)/len(in)+1)[:64<<10]
	}
	in = "sk-ant-api03-AbCdEfGhIjKlMnOp " + in + " AKIAIOSFODNN7EXAMPLE"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if s.Scan(in) == nil {
			b.Fatal("expected findings in the benchmark input")
		}
	}
}

// FuzzScan guards the scanner against panics and index out-of-range on
// arbitrary input; the reader and L1 are fuzzed because they take untrusted
// text.
func FuzzScan(f *testing.F) {
	f.Add("sk-ant-api03-AbCdEfGhIjKlMnOp")
	f.Add("x=1.2.3 y")
	f.Add("Bearer ghp_AbCdEfGhIjKlMnOpQrStUvWxYz")
	f.Add("")
	f.Add("\x00\x01\xff")
	f.Add("AKIAIOSFODNN7EXAMPLE\n-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END PRIVATE KEY-----")
	f.Add("sk-ant-api03-AbCdEfGhIj-----BEGIN PRIVATE KEY-----\nMIIEvQ\n-----END PRIVATE KEY-----A1b2-C3d4_E5f6-G7h8I9j0-kLmN0oPq")
	s := New()
	f.Fuzz(func(t *testing.T, data string) {
		prev := 0
		for _, fnd := range s.Scan(data) {
			if fnd.Start < 0 || fnd.End > len(data) || fnd.Start >= fnd.End {
				t.Fatalf("bad bounds %+v on %q", fnd, data)
			}
			if fnd.Start < prev {
				t.Fatalf("finding %+v starts before the one before it ends (%d) on %q", fnd, prev, data)
			}
			prev = fnd.End
		}
		_, _ = s.Redact(data)
	})
}
