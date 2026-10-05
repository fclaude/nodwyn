package redact

import (
	"testing"

	"github.com/fclaude/nodwyn/internal/detect/l1"
)

func TestRedactKnownClasses(t *testing.T) {
	text := "key is sk-ant-abcdefgh1234 and also AKIA0123456789ABCDEF"
	got := Redact(text, l1.New())
	for _, want := range []string{"[REDACTED anthropic-key]", "[REDACTED aws-access-key]"} {
		if !contains(got.Text, want) {
			t.Errorf("missing %q in %q", want, got.Text)
		}
	}
	for _, bad := range []string{"sk-ant-abcdefgh1234", "AKIA0123456789ABCDEF"} {
		if contains(got.Text, bad) {
			t.Errorf("secret %q still present in %q", bad, got.Text)
		}
	}
	if len(got.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got.Events))
	}
}

func TestRedactSkipsUnknownEntropy(t *testing.T) {
	// A long high-entropy token with no known prefix: residual, not redacted.
	text := "value Zx9kQm2vLpRtWnYbUcE4aD8fHjGiKlMnOpQrSt is here"
	got := Redact(text, l1.New())
	if got.Text != text {
		t.Errorf("residual was altered: %q", got.Text)
	}
	if len(got.Events) != 0 {
		t.Errorf("expected no events, got %d", len(got.Events))
	}
}

// A key ahead of a private key came out of Scan after it, and Redact sliced
// the text backwards over it and panicked: in whatever logged, displayed or
// sent text held both, such as a .env file.
func TestRedactAKeyAheadOfAPrivateKey(t *testing.T) {
	pem := "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END PRIVATE KEY-----"
	text := "AWS_KEY=AKIAIOSFODNN7EXAMPLE\nSSH_KEY=\"" + pem + "\"\nTOKEN=ghp_AbCdEfGhIjKlMnOpQrStUvWxYz\n"
	got := Redact(text, l1.New())
	want := "AWS_KEY=[REDACTED aws-access-key]\nSSH_KEY=\"[REDACTED pem]\"\nTOKEN=[REDACTED github-token]\n"
	if got.Text != want {
		t.Errorf("got %q, want %q", got.Text, want)
	}
	if len(got.Events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(got.Events), got.Events)
	}
	for i, e := range got.Events {
		if i > 0 && got.Events[i-1].End > e.Start {
			t.Errorf("events %+v and %+v overlap or are out of order", got.Events[i-1], e)
		}
		if text[e.Start:e.End] == "" {
			t.Errorf("event %+v is empty", e)
		}
	}
}

// A credential inside a private key's block is the block's, and is replaced
// once with it.
func TestRedactACredentialInsideAPrivateKey(t *testing.T) {
	text := "-----BEGIN PGP PRIVATE KEY BLOCK-----\nComment: AKIAIOSFODNN7EXAMPLE\nMIIEvQ\n-----END PGP PRIVATE KEY BLOCK-----\nafter"
	got := Redact(text, l1.New())
	if got.Text != "[REDACTED pem]\nafter" || len(got.Events) != 1 {
		t.Errorf("got %q with %+v", got.Text, got.Events)
	}
}

func TestRedactNone(t *testing.T) {
	got := Redact("nothing secret here", l1.New())
	if got.Text != "nothing secret here" || len(got.Events) != 0 {
		t.Errorf("unexpected result: %q %v", got.Text, got.Events)
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
