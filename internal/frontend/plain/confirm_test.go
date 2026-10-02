package plain

import (
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
)

func TestEgressSummary(t *testing.T) {
	if EgressSummary(nil) != "" {
		t.Fatal("ordinary prompt has egress scope")
	}
	got := EgressSummary(&guard.EgressScope{Class: "service", Host: "api.example.test", Method: "POST", Position: "query:key"})
	for _, want := range []string{`Credential class: "service"`, `Destination host: "api.example.test"`, `Method: "POST"`, `Position: "query:key"`, "scope facts for this call"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	got = EgressSummary(&guard.EgressScope{Class: "service\nspoof\x1b[2J"})
	if strings.Count(got, "not specified by primitive") != 3 || strings.Contains(got, "service\nspoof") || strings.Contains(got, "\x1b") {
		t.Fatal(got)
	}
}

func TestAttachSummary(t *testing.T) {
	text := AttachSummary(log.Attach{Name: "notes.md", Type: "text", Bytes: 120, Redactions: []string{"github"}})
	if text != "[attached notes.md · text · 120 bytes · 1 secret-like string(s) hidden]" {
		t.Fatal(text)
	}
	image := AttachSummary(log.Attach{Name: "shot.png", Type: "image", Width: 1200, Height: 800, Tokens: 1105, Destination: "127.0.0.1:11434"})
	if image != "[attached shot.png · image · 1200×800 · ~1,105 tokens · goes to 127.0.0.1:11434 only]" {
		t.Fatal(image)
	}
}
