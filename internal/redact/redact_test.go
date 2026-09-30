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
