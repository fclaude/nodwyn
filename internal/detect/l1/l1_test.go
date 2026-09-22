package l1

import (
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

// TestAllowlist: an exact value or a whole class can be suppressed.
func TestAllowlist(t *testing.T) {
	s := New()
	s.AllowValue("AKIAIOSFODNN7EXAMPLE")
	if fs := s.Scan("AKIAIOSFODNN7EXAMPLE"); len(fs) != 0 {
		t.Errorf("AllowValue did not suppress: %+v", fs)
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
// arbitrary input (plan §7: "reader and L1 from day one").
func FuzzScan(f *testing.F) {
	f.Add("sk-ant-api03-AbCdEfGhIjKlMnOp")
	f.Add("x=1.2.3 y")
	f.Add("Bearer ghp_AbCdEfGhIjKlMnOpQrStUvWxYz")
	f.Add("")
	f.Add("\x00\x01\xff")
	s := New()
	f.Fuzz(func(t *testing.T, data string) {
		for _, fnd := range s.Scan(data) {
			if fnd.Start < 0 || fnd.End > len(data) || fnd.Start >= fnd.End {
				t.Fatalf("bad bounds %+v on %q", fnd, data)
			}
		}
		_, _ = s.Redact(data)
	})
}
