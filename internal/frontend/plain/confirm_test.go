package plain

import (
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/guard"
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
